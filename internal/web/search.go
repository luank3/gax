package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// DefaultSearchResults is the result count used when the model does not ask for one.
	DefaultSearchResults = 5
	// MaxSearchResults bounds one search: every backend charges for the request and the model reads
	// the list, so a larger page is noise.
	MaxSearchResults = 10
)

// Backends name the search path that answered, for the tool's own reporting.
const (
	BackendBrave      = "brave"
	BackendDuckDuckGo = "duckduckgo"
)

// browserUserAgent is used for the DuckDuckGo fallback: its HTML endpoint answers a scripted
// client with HTTP 202 and an anomaly page, and serves the no-JavaScript result list to a browser.
const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const htmlSearchAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// Result is one search hit.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Report is one search response plus the backend that produced it.
type Report struct {
	Backend string
	Results []Result
}

// SearchOptions carries the per-call policy: the result count, the Brave key when the user has
// one, and the request limits.
type SearchOptions struct {
	Count  int
	APIKey string
	Limits Limits
}

// Search queries the web, through the Brave Search API when a key is configured and through
// DuckDuckGo's HTML endpoint otherwise.
func (client *Client) Search(ctx context.Context, query string, options SearchOptions) (Report, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Report{}, errors.New("empty search query")
	}
	count := options.Count
	if count <= 0 {
		count = DefaultSearchResults
	}
	if count > MaxSearchResults {
		count = MaxSearchResults
	}
	if strings.TrimSpace(options.APIKey) != "" {
		return client.searchBrave(ctx, query, count, options)
	}
	return client.searchDuckDuckGo(ctx, query, count, options)
}

func (client *Client) searchBrave(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	endpoint := client.BraveEndpoint
	if endpoint == "" {
		endpoint = DefaultClient.BraveEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Report{}, fmt.Errorf("invalid Brave endpoint %q", endpoint)
	}
	parameters := parsed.Query()
	parameters.Set("q", query)
	parameters.Set("count", strconv.Itoa(count))
	parsed.RawQuery = parameters.Encode()
	body, status, err := client.get(ctx, parsed.String(), []string{
		"Accept: application/json",
		"X-Subscription-Token: " + strings.TrimSpace(options.APIKey),
	}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Report{}, fmt.Errorf("the Brave Search API rejected the key (HTTP %d) — check BRAVE_API_KEY, or unset it to search through DuckDuckGo", status)
	case status == http.StatusTooManyRequests:
		return Report{}, errors.New("the Brave Search API rate limit was reached (HTTP 429)")
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return Report{}, fmt.Errorf("the Brave Search API returned HTTP %d", status)
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Report{}, errors.New("the Brave Search API response is not valid JSON")
	}
	results := make([]Result, 0, len(payload.Web.Results))
	for _, entry := range payload.Web.Results {
		if len(results) == count {
			break
		}
		results = append(results, Result{
			Title:   inlineText(entry.Title),
			URL:     strings.TrimSpace(entry.URL),
			Snippet: inlineText(entry.Description),
		})
	}
	return Report{Backend: BackendBrave, Results: results}, nil
}

func (client *Client) searchDuckDuckGo(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	endpoint := client.HTMLSearchEndpoint
	if endpoint == "" {
		endpoint = DefaultClient.HTMLSearchEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Report{}, fmt.Errorf("invalid DuckDuckGo endpoint %q", endpoint)
	}
	parameters := parsed.Query()
	parameters.Set("q", query)
	parsed.RawQuery = parameters.Encode()
	body, status, err := client.get(ctx, parsed.String(), []string{
		"Accept: " + htmlSearchAccept,
		"User-Agent: " + browserUserAgent,
	}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	// The result page is served with 200; anything else is a redirect to a challenge page (202) or
	// a rate limit, which the caller should hear about rather than see as an empty search.
	if status != http.StatusOK {
		return Report{}, fmt.Errorf("DuckDuckGo returned HTTP %d", status)
	}
	results := parseHTMLResults(body, count)
	if len(results) == 0 {
		return Report{}, errors.New("DuckDuckGo returned no usable results (its HTML endpoint throttles non-browser clients) — set BRAVE_API_KEY to search through the Brave Search API")
	}
	return Report{Backend: BackendDuckDuckGo, Results: results}, nil
}

// parseHTMLResults reads DuckDuckGo's result markup: each hit is an anchor carrying the result__a
// class, followed by its result__snippet text.
func parseHTMLResults(document []byte, limit int) []Result {
	results := make([]Result, 0, limit)
	var pending Result
	havePending := false
	field := ""
	element := ""
	depth := 0
	var captured strings.Builder

	assign := func() {
		value := inlineText(captured.String())
		captured.Reset()
		switch field {
		case "title":
			pending.Title = value
		case "snippet":
			pending.Snippet = value
		}
		field, element, depth = "", "", 0
	}
	commit := func() {
		if havePending {
			results = append(results, pending)
			pending, havePending = Result{}, false
		}
	}
	scanHTML(string(document), func(t token) bool {
		switch t.kind {
		case tokenText:
			if field != "" {
				captured.WriteString(t.text)
			}
		case tokenStartTag:
			if field != "" && t.name == element && !t.selfClosing {
				depth++
				return true
			}
			switch {
			case t.name == "a" && t.hasClass("result__a"):
				if field != "" {
					assign()
				}
				commit()
				if len(results) >= limit {
					return false
				}
				pending = Result{URL: resultURL(t.attr("href"))}
				havePending = true
				field, element, depth = "title", t.name, 1
			case (t.name == "a" || t.name == "td") && (t.hasClass("result__snippet") || t.hasClass("result-snippet")):
				if field != "" {
					assign()
				}
				field, element, depth = "snippet", t.name, 1
			}
		case tokenEndTag:
			if field != "" && t.name == element {
				depth--
				if depth <= 0 {
					assign()
				}
			}
		}
		return len(results) < limit
	})
	if field != "" {
		assign()
	}
	commit()
	return results
}

// resultURL unwraps DuckDuckGo's click-through redirect, which carries the target in `uddg`.
func resultURL(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	if parsed.Host == "duckduckgo.com" || strings.HasSuffix(parsed.Host, ".duckduckgo.com") {
		if target := parsed.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return href
}

// inlineText converts an HTML fragment to a single line: markup dropped, entities decoded, and
// whitespace collapsed.
func inlineText(value string) string {
	if !strings.ContainsAny(value, "<&") {
		return strings.Join(strings.Fields(value), " ")
	}
	return strings.Join(strings.Fields(Text([]byte(value))), " ")
}

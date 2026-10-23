package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultSearchResults is the result count used when the model does not ask for one.
	DefaultSearchResults = 5
	// MaxSearchResults bounds one search: every backend charges for the request and the model reads
	// the list, so a larger page is noise.
	MaxSearchResults = 10
	// MaxSnippetChars bounds one snippet. Backends differ between a description sentence and a page
	// highlight; unbounded, ten highlights would crowd the result list out of the tool output.
	MaxSnippetChars = 400
)

// Backends name the search path that answered, for the tool's own reporting.
const (
	BackendYou        = "you"
	BackendBrave      = "brave"
	BackendOcten      = "octen"
	BackendExa        = "exa"
	BackendFirecrawl  = "firecrawl"
	BackendTinyFish   = "tinyfish"
	BackendKeenable   = "keenable"
	BackendDuckDuckGo = "duckduckgo"
)

// browserUserAgent is used for the DuckDuckGo fallback: its HTML endpoint answers a scripted
// client with HTTP 202 and an anomaly page, and serves the no-JavaScript result list to a browser.
const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const htmlSearchAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// SearchProviders lists the backends automatic selection tries, in order: you.com is the default
// provider, both with its key and — through its keyless free profile — without any key, so it
// leads the list; the first backend with a configured key answers.
var SearchProviders = []string{BackendYou, BackendBrave, BackendOcten, BackendExa, BackendFirecrawl, BackendTinyFish, BackendKeenable}

// SearchBackends lists every backend web.search_provider accepts: SearchProviders plus the
// keyless DuckDuckGo crawl, which normally answers only as the fallback.
var SearchBackends = append(append([]string{}, SearchProviders...), BackendDuckDuckGo)

// CredentialEnv names the environment variable holding a backend's key, for diagnostics.
// You.com, Firecrawl and Keenable also offer keyless search; DuckDuckGo needs no key.
func CredentialEnv(provider string) string {
	switch provider {
	case BackendBrave:
		return "BRAVE_API_KEY"
	case BackendYou:
		return "YDC_API_KEY"
	case BackendOcten:
		return "OCTEN_API_KEY"
	case BackendExa:
		return "EXA_API_KEY"
	case BackendFirecrawl:
		return "FIRECRAWL_API_KEY"
	case BackendTinyFish:
		return "TINYFISH_API_KEY"
	case BackendKeenable:
		return "KEENABLE_API_KEY"
	}
	return ""
}

// backendLabel is the backend's name as it appears in a diagnostic.
func backendLabel(provider string) string {
	switch provider {
	case BackendYou:
		return "You.com"
	case BackendBrave:
		return "Brave Search"
	case BackendOcten:
		return "Octen"
	case BackendExa:
		return "Exa"
	case BackendFirecrawl:
		return "Firecrawl"
	case BackendTinyFish:
		return "TinyFish"
	case BackendKeenable:
		return "Keenable"
	case BackendDuckDuckGo:
		return "DuckDuckGo"
	}
	return provider
}

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

// Keys carries one credential per backend, taken from the matching web.<name>_api_key setting.
type Keys struct {
	Brave     string
	You       string
	Octen     string
	Exa       string
	Firecrawl string
	TinyFish  string
	Keenable  string
}

func (keys Keys) of(provider string) string {
	switch provider {
	case BackendBrave:
		return keys.Brave
	case BackendYou:
		return keys.You
	case BackendOcten:
		return keys.Octen
	case BackendExa:
		return keys.Exa
	case BackendFirecrawl:
		return keys.Firecrawl
	case BackendTinyFish:
		return keys.TinyFish
	case BackendKeenable:
		return keys.Keenable
	}
	return ""
}

// SearchOptions carries the per-call policy: the backend to use (empty selects one), the result
// count, the configured credentials, and the request limits.
type SearchOptions struct {
	Provider string
	Count    int
	Keys     Keys
	Limits   Limits
}

// Search queries the web through the selected backend: web.search_provider when it names one, the
// first backend with a key otherwise, and you.com's keyless profile when no key is configured. A
// backend that cannot answer falls back to the keyless DuckDuckGo crawl (see shouldFallBack).
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
	provider := options.provider()
	report, err := client.searchBackend(ctx, provider, query, count, options)
	if err == nil || !shouldFallBack(ctx, provider, err) {
		return report, err
	}
	fallback, fallbackError := client.searchBackend(ctx, BackendDuckDuckGo, query, count, options)
	if fallbackError != nil {
		return Report{}, fmt.Errorf("%s; the DuckDuckGo fallback failed too: %s", err, fallbackError)
	}
	return fallback, nil
}

// shouldFallBack reports whether a failed search is worth retrying through DuckDuckGo: the crawl
// keeps a search working when a service is rate-limited, broken, or out of credit, but it would
// bury a failure the user has to act on, and it cannot improve on an answer of "nothing found".
func shouldFallBack(ctx context.Context, provider string, err error) bool {
	if ctx.Err() != nil || provider == BackendDuckDuckGo {
		return false
	}
	var settled settledError
	return !errors.As(err, &settled)
}

func (client *Client) searchBackend(ctx context.Context, provider, query string, count int, options SearchOptions) (Report, error) {
	switch provider {
	case BackendYou:
		return client.searchYou(ctx, query, count, options)
	case BackendBrave:
		return client.searchBrave(ctx, query, count, options)
	case BackendOcten:
		return client.searchOcten(ctx, query, count, options)
	case BackendExa:
		return client.searchExa(ctx, query, count, options)
	case BackendFirecrawl:
		return client.searchFirecrawl(ctx, query, count, options)
	case BackendTinyFish:
		return client.searchTinyFish(ctx, query, count, options)
	case BackendKeenable:
		return client.searchKeenable(ctx, query, count, options)
	case BackendDuckDuckGo:
		return client.searchDuckDuckGo(ctx, query, count, options)
	default:
		// A name the settings reject is the user's to fix, so it never reaches the fallback.
		return Report{}, settledf("unknown web search provider %q (choose %s)",
			options.Provider, strings.Join(SearchBackends, ", "))
	}
}

// provider resolves the backend for one call.
func (options SearchOptions) provider() string {
	if name := strings.ToLower(strings.TrimSpace(options.Provider)); name != "" && name != "auto" {
		return name
	}
	for _, candidate := range SearchProviders {
		if strings.TrimSpace(options.Keys.of(candidate)) != "" {
			return candidate
		}
	}
	return BackendYou
}

func (client *Client) searchBrave(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	key, err := requiredKey(BackendBrave, options.Keys.Brave)
	if err != nil {
		return Report{}, err
	}
	endpoint := orDefault(client.BraveEndpoint, DefaultClient.BraveEndpoint)
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
		"X-Subscription-Token: " + key,
	}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload struct {
		Error struct {
			Detail string `json:"detail"`
		} `json:"error"`
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendBrave, status); err != nil {
		return Report{}, serviceMessage(err, payload.Error.Detail)
	}
	if parseError != nil {
		return Report{}, errors.New("the Brave Search API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Web.Results {
		results = addResult(results, count, entry.Title, entry.URL, entry.Description)
	}
	return finishSearch(BackendBrave, results)
}

// searchYou answers through the You.com Web Search API when a key is configured and through its
// keyless free MCP profile otherwise.
func (client *Client) searchYou(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	if key := strings.TrimSpace(options.Keys.You); key != "" {
		return client.searchYouAPI(ctx, query, count, key, options)
	}
	return client.searchYouFree(ctx, query, count, options)
}

func (client *Client) searchYouAPI(ctx context.Context, query string, count int, key string, options SearchOptions) (Report, error) {
	endpoint := orDefault(client.YouEndpoint, DefaultClient.YouEndpoint)
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"query": query,
		"count": count,
	}, []string{"X-API-Key: " + key}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload youResponse
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendYou, status); err != nil {
		return Report{}, err
	}
	if parseError != nil {
		return Report{}, errors.New("the You.com search API response is not valid JSON")
	}
	return finishSearch(BackendYou, payload.results(count))
}

// searchYouFree calls the free MCP profile: https://api.you.com/mcp?profile=free exposes you-search
// without a key. The endpoint answers a tools/call on its own — no initialize handshake — and may
// send notification frames before the reply.
func (client *Client) searchYouFree(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	endpoint := orDefault(client.YouMCPEndpoint, DefaultClient.YouMCPEndpoint)
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "you-search",
			"arguments": map[string]any{"query": query, "count": count},
		},
	}, []string{"Accept: application/json, text/event-stream"}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	if status != http.StatusOK {
		return Report{}, fmt.Errorf("the You.com free search returned HTTP %d", status)
	}
	message, err := parseRPCMessage(body)
	if err != nil {
		return Report{}, err
	}
	if message.Error != nil {
		return Report{}, fmt.Errorf("the You.com free search refused the call: %s", message.Error)
	}
	if message.Result == nil {
		return Report{}, errors.New("the You.com free search returned no reply")
	}
	payload, err := message.Result.searchPayload()
	if err != nil {
		return Report{}, err
	}
	return finishSearch(BackendYou, payload.results(count))
}

func (client *Client) searchOcten(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	key, err := requiredKey(BackendOcten, options.Keys.Octen)
	if err != nil {
		return Report{}, err
	}
	endpoint := orDefault(client.OctenEndpoint, DefaultClient.OctenEndpoint)
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"query": query,
		"count": count,
	}, []string{"Authorization: Bearer " + key}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Results []struct {
				Title     string `json:"title"`
				URL       string `json:"url"`
				Highlight string `json:"highlight"`
			} `json:"results"`
		} `json:"data"`
	}
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendOcten, status); err != nil {
		return Report{}, serviceMessage(err, payload.Msg)
	}
	// A refusal can also arrive with HTTP 200 and a non-zero business code.
	if parseError == nil && payload.Code != 0 {
		return Report{}, fmt.Errorf("the Octen API refused the query: %s (%d)", payload.Msg, payload.Code)
	}
	if parseError != nil {
		return Report{}, errors.New("the Octen API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Data.Results {
		results = addResult(results, count, entry.Title, entry.URL, entry.Highlight)
	}
	return finishSearch(BackendOcten, results)
}

func (client *Client) searchExa(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	key, err := requiredKey(BackendExa, options.Keys.Exa)
	if err != nil {
		return Report{}, err
	}
	endpoint := orDefault(client.ExaEndpoint, DefaultClient.ExaEndpoint)
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"query":      query,
		"numResults": count,
		"type":       "auto",
		// Highlights are the cheapest per-result text Exa returns; its full text is billed
		// separately and the model can fetch a page it wants to read.
		"contents": map[string]any{"highlights": true},
	}, []string{"x-api-key: " + key}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload struct {
		Error   string `json:"error"`
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Text       string   `json:"text"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendExa, status); err != nil {
		return Report{}, serviceMessage(err, payload.Error)
	}
	if parseError != nil {
		return Report{}, errors.New("the Exa API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Results {
		snippet := entry.Text
		if len(entry.Highlights) > 0 {
			snippet = entry.Highlights[0]
		}
		results = addResult(results, count, entry.Title, entry.URL, snippet)
	}
	return finishSearch(BackendExa, results)
}

func (client *Client) searchFirecrawl(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	// Firecrawl serves a keyless tier too, so the header is only added when a key exists.
	headers := []string{"Accept: application/json"}
	if key := strings.TrimSpace(options.Keys.Firecrawl); key != "" {
		headers = append(headers, "Authorization: Bearer "+key)
	}
	endpoint := orDefault(client.FirecrawlEndpoint, DefaultClient.FirecrawlEndpoint)
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"query":   query,
		"limit":   count,
		"sources": []string{"web"},
	}, headers, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Web []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"web"`
		} `json:"data"`
	}
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendFirecrawl, status); err != nil {
		return Report{}, serviceMessage(err, payload.Error)
	}
	if parseError != nil {
		return Report{}, errors.New("the Firecrawl API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Data.Web {
		results = addResult(results, count, entry.Title, entry.URL, entry.Description)
	}
	return finishSearch(BackendFirecrawl, results)
}

func (client *Client) searchTinyFish(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	key, err := requiredKey(BackendTinyFish, options.Keys.TinyFish)
	if err != nil {
		return Report{}, err
	}
	endpoint := orDefault(client.TinyFishEndpoint, DefaultClient.TinyFishEndpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Report{}, fmt.Errorf("invalid TinyFish endpoint %q", endpoint)
	}
	parameters := parsed.Query()
	parameters.Set("query", query)
	parsed.RawQuery = parameters.Encode()
	// TinyFish pages by ten and takes no count parameter, so the requested count is applied to the
	// first page.
	body, status, err := client.get(ctx, parsed.String(), []string{
		"Accept: application/json",
		"X-API-Key: " + key,
	}, options.Limits)
	if err != nil {
		return Report{}, err
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	parseError := json.Unmarshal(body, &payload)
	if err := statusFailure(BackendTinyFish, status); err != nil {
		return Report{}, serviceMessage(err, payload.Error.Message)
	}
	if parseError != nil {
		return Report{}, errors.New("the TinyFish API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Results {
		results = addResult(results, count, entry.Title, entry.URL, entry.Snippet)
	}
	return finishSearch(BackendTinyFish, results)
}

func (client *Client) searchKeenable(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	key := strings.TrimSpace(options.Keys.Keenable)
	endpoint := orDefault(client.KeenableEndpoint, DefaultClient.KeenableEndpoint)
	headers := []string{"Accept: application/json"}
	if key != "" {
		headers = append(headers, "X-API-Key: "+key)
	} else {
		endpoint = orDefault(client.KeenablePublicEndpoint, DefaultClient.KeenablePublicEndpoint)
		headers = append(headers, "X-Keenable-Title: gax")
	}
	body, status, err := client.postJSON(ctx, endpoint, map[string]any{
		"query":              query,
		"max_results":        count,
		"snippet_max_length": MaxSnippetChars,
	}, headers, options.Limits)
	if err != nil {
		return Report{}, err
	}
	// Keenable reports malformed keys with 400, not just 401/403.
	if key != "" && status == http.StatusBadRequest {
		return Report{}, settledf("the Keenable API rejected the request (HTTP 400) — check KEENABLE_API_KEY and the query")
	}
	if key == "" && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return Report{}, fmt.Errorf("the Keenable public search returned HTTP %d", status)
	}
	if err := statusFailure(BackendKeenable, status); err != nil {
		return Report{}, err
	}
	var payload struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Snippet     string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Report{}, errors.New("the Keenable API response is not valid JSON")
	}
	var results []Result
	for _, entry := range payload.Results {
		snippet := entry.Snippet
		if strings.TrimSpace(snippet) == "" {
			snippet = entry.Description
		}
		results = addResult(results, count, entry.Title, entry.URL, snippet)
	}
	return finishSearch(BackendKeenable, results)
}

// settledError marks a failure a fallback would only paper over: the user has to fix a credential,
// or the backend answered that there is nothing to find. Availability failures — a rate limit, a
// server error, an unusable response — are plain errors, and are retried through the fallback.
type settledError struct {
	message string
}

func (failure settledError) Error() string {
	return failure.message
}

func settledf(format string, args ...any) error {
	return settledError{message: fmt.Sprintf(format, args...)}
}

// requiredKey reads a backend's credential, naming the variable that supplies it when it is empty.
func requiredKey(provider, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", settledf("the %s backend needs an API key — set %s, or pick another web search provider",
			backendLabel(provider), CredentialEnv(provider))
	}
	return key, nil
}

// statusFailure maps a non-2xx status onto the action behind it; a status with no mapping of its
// own is still reported, so the model never reads a refused request as an empty search.
func statusFailure(provider string, status int) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	label := backendLabel(provider)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return settledf("the %s API rejected the key (HTTP %d) — check %s", label, status, CredentialEnv(provider))
	case http.StatusPaymentRequired:
		return settledf("the %s API has no credit left (HTTP %d) — check the account behind %s",
			label, status, CredentialEnv(provider))
	case http.StatusTooManyRequests:
		return fmt.Errorf("the %s API rate limit was reached (HTTP %d)", label, status)
	}
	return fmt.Errorf("the %s API returned HTTP %d", label, status)
}

// serviceMessage appends the service's own words to a status failure, when it wrote any.
func serviceMessage(statusError error, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return statusError
	}
	return fmt.Errorf("%s: %s", statusError, snippetText(message))
}

// finishSearch reports an empty listing with its reason instead of reaching the model as "no
// results" (see docs/superpowers/plans/2026-10-11-web-tools.md).
func finishSearch(provider string, results []Result) (Report, error) {
	if len(results) == 0 {
		return Report{}, settledf("the %s API returned no results for this query", backendLabel(provider))
	}
	return Report{Backend: provider, Results: results}, nil
}

func (client *Client) searchDuckDuckGo(ctx context.Context, query string, count int, options SearchOptions) (Report, error) {
	endpoint := orDefault(client.HTMLSearchEndpoint, DefaultClient.HTMLSearchEndpoint)
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
		return Report{}, errors.New("DuckDuckGo returned no usable results (its HTML endpoint throttles non-browser clients)")
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
		value := snippetText(captured.String())
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

// addResult appends one hit unless it carries neither a title nor a URL, and stops at the limit.
func addResult(results []Result, limit int, title, link, snippet string) []Result {
	if len(results) >= limit {
		return results
	}
	title = snippetText(title)
	link = strings.TrimSpace(link)
	if title == "" && link == "" {
		return results
	}
	return append(results, Result{Title: title, URL: link, Snippet: snippetText(snippet)})
}

// youResponse is the result shape the You.com Web Search API and its MCP tool share.
type youResponse struct {
	Results struct {
		Web []struct {
			Title       string   `json:"title"`
			URL         string   `json:"url"`
			Description string   `json:"description"`
			Snippets    []string `json:"snippets"`
			Contents    struct {
				Highlights []string `json:"highlights"`
			} `json:"contents"`
		} `json:"web"`
	} `json:"results"`
}

func (payload youResponse) results(limit int) []Result {
	var results []Result
	for _, entry := range payload.Results.Web {
		// Plain searches carry a description and snippets; a highlights extraction replaces them.
		snippet := entry.Description
		if snippet == "" && len(entry.Snippets) > 0 {
			snippet = entry.Snippets[0]
		}
		if snippet == "" && len(entry.Contents.Highlights) > 0 {
			snippet = entry.Contents.Highlights[0]
		}
		results = addResult(results, limit, entry.Title, entry.URL, snippet)
	}
	return results
}

// jsonRPCMessage is the envelope an MCP endpoint answers with.
type jsonRPCMessage struct {
	Result *jsonRPCResult `json:"result"`
	Error  *jsonRPCError  `json:"error"`
}

type jsonRPCResult struct {
	StructuredContent json.RawMessage `json:"structuredContent"`
	Content           []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// searchPayload reads the search response a tools/call result carries, preferring the structured
// content over the same JSON rendered as a text block.
func (result *jsonRPCResult) searchPayload() (youResponse, error) {
	if len(result.StructuredContent) > 0 {
		var payload youResponse
		if err := json.Unmarshal(result.StructuredContent, &payload); err != nil {
			return youResponse{}, errors.New("the You.com free search response is not valid JSON")
		}
		return payload, nil
	}
	for _, content := range result.Content {
		if content.Type != "text" {
			continue
		}
		var payload youResponse
		if err := json.Unmarshal([]byte(content.Text), &payload); err == nil {
			return payload, nil
		}
	}
	return youResponse{}, errors.New("the You.com free search returned no usable results")
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (failure *jsonRPCError) Error() string {
	return fmt.Sprintf("%s (%d)", failure.Message, failure.Code)
}

// parseRPCMessage decodes an MCP response. The endpoint answers with a JSON body or with a
// text/event-stream carrying one, and notifications may precede the reply.
func parseRPCMessage(body []byte) (*jsonRPCMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("the You.com free search returned an empty response")
	}
	if trimmed[0] == '{' {
		var message jsonRPCMessage
		if err := json.Unmarshal(trimmed, &message); err != nil {
			return nil, errors.New("the You.com free search response is not valid JSON")
		}
		return &message, nil
	}
	for _, line := range strings.Split(string(trimmed), "\n") {
		payload, found := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !found {
			continue
		}
		var message jsonRPCMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &message); err != nil {
			return nil, errors.New("the You.com free search response is not valid JSON")
		}
		if message.Result != nil || message.Error != nil {
			return &message, nil
		}
	}
	return nil, errors.New("the You.com free search returned no reply")
}

// orDefault returns the first non-empty value, so an endpoint a test set wins over the
// process-wide default.
func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// snippetText flattens a backend's title or snippet markup into one bounded line.
func snippetText(value string) string {
	text := inlineText(value)
	if len(text) <= MaxSnippetChars {
		return text
	}
	cut := text[:MaxSnippetChars]
	for cut != "" && !utf8.RuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	if space := strings.LastIndexByte(cut, ' '); space > 0 {
		cut = cut[:space]
	}
	return cut + "…"
}

// inlineText converts an HTML fragment to a single line: markup dropped, entities decoded, and
// whitespace collapsed.
func inlineText(value string) string {
	if !strings.ContainsAny(value, "<&") {
		return strings.Join(strings.Fields(value), " ")
	}
	return strings.Join(strings.Fields(Text([]byte(value))), " ")
}

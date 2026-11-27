package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultSearchResults = 5
	MaxSearchResults     = 10
	MaxSnippetChars      = 400
)

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

// SearchProviders lists backends tried in auto-selection order.
var SearchProviders = []string{
	BackendYou, BackendBrave, BackendOcten, BackendExa, BackendFirecrawl, BackendTinyFish, BackendKeenable,
}

// SearchBackends lists all accepted web.search_provider values.
var SearchBackends = append(append([]string{}, SearchProviders...), BackendDuckDuckGo)

// Keys holds per-backend API keys.
type Keys struct {
	You       string
	Brave     string
	Octen     string
	Exa       string
	Firecrawl string
	TinyFish  string
	Keenable  string
}

func (k Keys) of(provider string) string {
	switch provider {
	case BackendYou:
		return k.You
	case BackendBrave:
		return k.Brave
	case BackendOcten:
		return k.Octen
	case BackendExa:
		return k.Exa
	case BackendFirecrawl:
		return k.Firecrawl
	case BackendTinyFish:
		return k.TinyFish
	case BackendKeenable:
		return k.Keenable
	default:
		return ""
	}
}

// SearchOptions configures one Search call.
type SearchOptions struct {
	Provider string
	Count    int
	Keys     Keys
	Limits   Limits
}

// Result is one search hit.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Report is a search response with the backend that answered.
type Report struct {
	Backend string
	Results []Result
}

// settledError marks failures that should not fall back to DuckDuckGo
// (invalid configuration, missing required key, or empty results).
type settledError struct{ err error }

func (e settledError) Error() string { return e.err.Error() }
func (e settledError) Unwrap() error { return e.err }

// Search queries the selected backend and falls back to DuckDuckGo on transient/service failures.
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) (Report, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Report{}, errors.New("empty search query")
	}
	count := opts.Count
	if count <= 0 {
		count = DefaultSearchResults
	}
	if count > MaxSearchResults {
		count = MaxSearchResults
	}
	prov := opts.resolveProvider()
	rep, err := c.searchBackend(ctx, prov, query, count, opts)
	if err == nil {
		return rep, nil
	}
	var settled settledError
	if ctx.Err() != nil || prov == BackendDuckDuckGo || errors.As(err, &settled) {
		return Report{}, err
	}
	fb, fbErr := c.searchDuckDuckGo(ctx, query, count, opts)
	if fbErr != nil {
		return Report{}, fmt.Errorf("%s; DuckDuckGo fallback failed too: %s", err, fbErr)
	}
	return fb, nil
}

func (o SearchOptions) resolveProvider() string {
	if name := strings.ToLower(strings.TrimSpace(o.Provider)); name != "" && name != "auto" {
		return name
	}
	for _, cand := range SearchProviders {
		if strings.TrimSpace(o.Keys.of(cand)) != "" {
			return cand
		}
	}
	return BackendYou
}

type jsonSearchSpec struct {
	backend     string
	endpointKey string
	requireKey  bool
	authHeader  string // e.g. "X-API-Key" or "Bearer"
	countField  string // "count", "numResults", "limit", "max_results"
	extraBody   map[string]any
	extract     func(raw []byte, limit int) ([]Result, error)
}

var jsonSpecs = map[string]jsonSearchSpec{
	BackendOcten: {
		backend: BackendOcten, endpointKey: BackendOcten, requireKey: true,
		authHeader: "Bearer", countField: "limit", extract: extractFlatResults("results", "description"),
	},
	BackendExa: {
		backend: BackendExa, endpointKey: BackendExa, requireKey: true,
		authHeader: "x-api-key", countField: "numResults", extract: extractFlatResults("results", "text"),
	},
	BackendFirecrawl: {
		backend: BackendFirecrawl, endpointKey: BackendFirecrawl, requireKey: true,
		authHeader: "Bearer", countField: "limit", extract: extractFirecrawlResults,
	},
	BackendTinyFish: {
		backend: BackendTinyFish, endpointKey: BackendTinyFish, requireKey: true,
		authHeader: "X-API-Key", countField: "max_results", extract: extractFlatResults("results", "snippet"),
	},
	BackendKeenable: {
		backend: BackendKeenable, endpointKey: BackendKeenable, requireKey: false,
		authHeader: "Bearer", countField: "limit", extract: extractFlatResults("results", "snippet"),
	},
}

func (c *Client) searchBackend(ctx context.Context, prov, query string, count int, opts SearchOptions) (Report, error) {
	switch prov {
	case BackendYou:
		return c.searchYou(ctx, query, count, opts)
	case BackendBrave:
		return c.searchBrave(ctx, query, count, opts)
	case BackendDuckDuckGo:
		return c.searchDuckDuckGo(ctx, query, count, opts)
	default:
		if spec, ok := jsonSpecs[prov]; ok {
			return c.searchJSONSpec(ctx, spec, query, count, opts)
		}
		return Report{}, settledError{fmt.Errorf("unknown web search provider %q (choose %s)", opts.Provider, strings.Join(SearchBackends, ", "))}
	}
}

func (c *Client) searchJSONSpec(ctx context.Context, spec jsonSearchSpec, query string, count int, opts SearchOptions) (Report, error) {
	key := strings.TrimSpace(opts.Keys.of(spec.backend))
	if spec.requireKey && key == "" {
		return Report{}, settledError{fmt.Errorf("%s search requires an API key", spec.backend)}
	}
	epKey := spec.endpointKey
	if spec.backend == BackendKeenable && key == "" {
		epKey = BackendKeenable + "_p"
	}
	endpoint := c.Endpoints[epKey]
	payload := map[string]any{"query": query, spec.countField: count}
	for k, v := range spec.extraBody {
		payload[k] = v
	}
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "application/json")
	if key != "" {
		if spec.authHeader == "Bearer" {
			hdr.Set("Authorization", "Bearer "+key)
		} else {
			hdr.Set(spec.authHeader, key)
		}
	}
	raw, status, err := c.doJSON(ctx, http.MethodPost, endpoint, hdr, payload, opts.Limits)
	if err != nil {
		return Report{}, err
	}
	if status < 200 || status >= 300 {
		return Report{}, fmt.Errorf("%s HTTP %d", spec.backend, status)
	}
	results, err := spec.extract(raw, count)
	if err != nil {
		return Report{}, err
	}
	return finishSearch(spec.backend, results)
}

func (c *Client) searchYou(ctx context.Context, query string, count int, opts SearchOptions) (Report, error) {
	key := strings.TrimSpace(opts.Keys.You)
	if key != "" {
		hdr := http.Header{"Content-Type": {"application/json"}, "X-API-Key": {key}}
		raw, status, err := c.doJSON(ctx, http.MethodPost, c.Endpoints[BackendYou], hdr, map[string]any{"query": query, "count": count}, opts.Limits)
		if err != nil {
			return Report{}, err
		}
		if status < 200 || status >= 300 {
			return Report{}, fmt.Errorf("you HTTP %d", status)
		}
		var resp struct {
			Results struct {
				Web []struct {
					Title       string   `json:"title"`
					URL         string   `json:"url"`
					Description string   `json:"description"`
					Snippets    []string `json:"snippets"`
				} `json:"web"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return Report{}, err
		}
		var out []Result
		for _, w := range resp.Results.Web {
			snip := w.Description
			if snip == "" && len(w.Snippets) > 0 {
				snip = w.Snippets[0]
			}
			out = addResult(out, count, w.Title, w.URL, snip)
		}
		return finishSearch(BackendYou, out)
	}
	// Keyless You.com free MCP profile
	hdr := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
	mcpReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "you-search",
			"arguments": map[string]any{"query": query, "count": count},
		},
	}
	raw, status, err := c.doJSON(ctx, http.MethodPost, c.Endpoints[BackendYou+"_mcp"], hdr, mcpReq, opts.Limits)
	if err != nil {
		return Report{}, err
	}
	if status < 200 || status >= 300 {
		return Report{}, fmt.Errorf("you free MCP HTTP %d", status)
	}
	results, err := extractFlatResults("results", "description")(raw, count)
	if err != nil {
		return Report{}, err
	}
	return finishSearch(BackendYou, results)
}

func (c *Client) searchBrave(ctx context.Context, query string, count int, opts SearchOptions) (Report, error) {
	key := strings.TrimSpace(opts.Keys.Brave)
	if key == "" {
		return Report{}, settledError{errors.New("brave search requires BRAVE_API_KEY")}
	}
	u, err := url.Parse(c.Endpoints[BackendBrave])
	if err != nil {
		return Report{}, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()
	hdr := http.Header{"Accept": {"application/json"}, "X-Subscription-Token": {key}}
	raw, status, err := c.doJSON(ctx, http.MethodGet, u.String(), hdr, nil, opts.Limits)
	if err != nil {
		return Report{}, err
	}
	if status < 200 || status >= 300 {
		return Report{}, fmt.Errorf("brave HTTP %d", status)
	}
	var resp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Report{}, err
	}
	var out []Result
	for _, r := range resp.Web.Results {
		out = addResult(out, count, r.Title, r.URL, r.Description)
	}
	return finishSearch(BackendBrave, out)
}

var ddgLinkRe = regexp.MustCompile(`<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)

func (c *Client) searchDuckDuckGo(ctx context.Context, query string, count int, opts SearchOptions) (Report, error) {
	lim := opts.Limits.resolved()
	reqCtx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	form := url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.HTMLSearchEndpoint, strings.NewReader(form))
	if err != nil {
		return Report{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Report{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Report{}, fmt.Errorf("duckduckgo HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lim.MaxBytes))
	if err != nil {
		return Report{}, err
	}
	var out []Result
	for _, m := range ddgLinkRe.FindAllStringSubmatch(string(raw), count*2) {
		u := unwrapDDGURL(html.UnescapeString(m[1]))
		title := HTMLToText(m[2])
		out = addResult(out, count, title, u, "")
	}
	return finishSearch(BackendDuckDuckGo, out)
}

func unwrapDDGURL(raw string) string {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			return uddg
		}
	}
	return raw
}

func (c *Client) doJSON(ctx context.Context, method, target string, hdr http.Header, body any, limits Limits) ([]byte, int, error) {
	lim := limits.resolved()
	reqCtx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target, r)
	if err != nil {
		return nil, 0, err
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lim.MaxBytes))
	return raw, resp.StatusCode, err
}

func extractFlatResults(arrayKey, snippetKey string) func([]byte, int) ([]Result, error) {
	return func(raw []byte, limit int) ([]Result, error) {
		var root map[string]any
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
		arr, _ := root[arrayKey].([]any)
		var out []Result
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			title, _ := m["title"].(string)
			u, _ := m["url"].(string)
			snip, _ := m[snippetKey].(string)
			if snip == "" {
				snip, _ = m["description"].(string)
			}
			out = addResult(out, limit, title, u, snip)
		}
		return out, nil
	}
}

func extractFirecrawlResults(raw []byte, limit int) ([]Result, error) {
	var resp struct {
		Data struct {
			Web []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"web"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	var out []Result
	for _, r := range resp.Data.Web {
		out = addResult(out, limit, r.Title, r.URL, r.Description)
	}
	return out, nil
}

func addResult(dst []Result, limit int, title, u, snippet string) []Result {
	if len(dst) >= limit || strings.TrimSpace(u) == "" {
		return dst
	}
	snippet = strings.TrimSpace(snippet)
	if len(snippet) > MaxSnippetChars {
		snippet = snippet[:MaxSnippetChars-3] + "..."
	}
	return append(dst, Result{
		Title:   strings.TrimSpace(title),
		URL:     strings.TrimSpace(u),
		Snippet: snippet,
	})
}

func finishSearch(backend string, results []Result) (Report, error) {
	if len(results) == 0 {
		return Report{}, settledError{fmt.Errorf("%s returned no results", backend)}
	}
	return Report{Backend: backend, Results: results}, nil
}

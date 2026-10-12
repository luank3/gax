package tools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/web"
)

func webFetchDefinition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Name: "web_fetch",
		Description: "Fetch a URL over HTTP(S) and return the response body. An HTML page is converted to plain text " +
			"by default; pass `strip: false` for the raw body (markup, JSON, XML, CSV, or a page whose text conversion " +
			"lost structure you need). Redirects are followed, the download is capped, and non-text responses " +
			"(images, PDFs, archives) are reported by type and size instead of returned.",
		Params: []provider.ToolParam{
			{Name: "url", Type: "string", Required: true, Description: "Absolute http:// or https:// URL to fetch."},
			{Name: "strip", Type: "boolean", Description: "Convert an HTML response to plain text (default true)."},
		},
	}
}

func webSearchDefinition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Name: "web_search",
		Description: "Search the web and return ranked results with titles, URLs, and snippets. Searches through the " +
			"backend `web.search_provider` names — you, brave, octen, exa, firecrawl, tinyfish, or duckduckgo — " +
			"or through You.com (its free profile without a key) when another backend has no key. A backend that " +
			"cannot answer falls back to DuckDuckGo. Read a promising result with `web_fetch`.",
		Params: []provider.ToolParam{
			{Name: "query", Type: "string", Required: true, Description: "Search query."},
			{Name: "count", Type: "integer", Minimum: 1, Description: "Maximum results to return (default 5, maximum 10)."},
		},
	}
}

func (registry *Registry) runWebFetch(arguments string, _ int, _ DisplaySink) Result {
	return registry.runWebFetchContext(context.Background(), arguments)
}

func (registry *Registry) runWebFetchContext(ctx context.Context, arguments string) Result {
	if registry.noWeb() {
		return Result{Text: "the web tools are disabled (no_web)"}
	}
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	rawURL, ok := object.string("url")
	if !ok || strings.TrimSpace(rawURL) == "" {
		return Result{Text: "missing 'url' argument"}
	}
	strip, hasStrip, boolError := object.boolValue("strip")
	if boolError != "" {
		return Result{Text: boolError}
	}
	if !hasStrip {
		strip = true
	}
	document, err := registry.webClient().Fetch(ctx, rawURL, registry.webLimits())
	if err != nil {
		return Result{Text: fmt.Sprintf("error fetching %s: %s", strings.TrimSpace(rawURL), err)}
	}
	return Result{Text: capWebText(webDocumentText(document, rawURL, strip), registry.outputLimit())}
}

func (registry *Registry) runWebSearch(arguments string, _ int, _ DisplaySink) Result {
	return registry.runWebSearchContext(context.Background(), arguments)
}

func (registry *Registry) runWebSearchContext(ctx context.Context, arguments string) Result {
	if registry.noWeb() {
		return Result{Text: "the web tools are disabled (no_web)"}
	}
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	query, ok := object.string("query")
	if !ok || strings.TrimSpace(query) == "" {
		return Result{Text: "missing 'query' argument"}
	}
	count := 0
	if value, present, integerError := object.integer("count"); integerError != "" {
		return Result{Text: integerError}
	} else if present {
		count = int(value)
	}
	report, err := registry.webClient().Search(ctx, query, web.SearchOptions{
		Provider: registry.webSearchProvider(),
		Count:    count,
		Keys:     registry.webSearchKeys(),
		Limits:   registry.webLimits(),
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("error searching for %q: %s", strings.TrimSpace(query), err)}
	}
	return Result{Text: capWebText(webSearchText(report), registry.outputLimit())}
}

// webDocumentText renders a fetched response: the body, then the summary line the model needs to
// tell a complete page from a truncated one and a redirect from the URL it asked for.
func webDocumentText(document web.Document, requested string, strip bool) string {
	stripped := strip && document.HTML()
	summary := webDocumentSummary(document, requested, stripped)
	if !document.Textual() {
		return fmt.Sprintf("[%s · only text responses are returned]", summary)
	}
	body := string(document.Body)
	if stripped {
		body = web.Text(document.Body)
	}
	body = strings.TrimRight(sanitizeUTF8(body), " \t\n")
	if body == "" {
		body = "(empty response)"
	}
	return body + "\n\n[" + summary + "]"
}

func webDocumentSummary(document web.Document, requested string, stripped bool) string {
	status := strconv.Itoa(document.Status)
	if text := http.StatusText(document.Status); text != "" {
		status += " " + text
	}
	parts := []string{status}
	if document.ContentType != "" {
		parts = append(parts, document.ContentType)
	}
	if stripped {
		parts = append(parts, "stripped")
	}
	size := formatByteSize(int64(len(document.Body)))
	if document.Truncated {
		parts = append(parts, "first "+size+" of a larger response")
	} else {
		parts = append(parts, size)
	}
	if document.URL != "" && document.URL != strings.TrimSpace(requested) {
		parts = append(parts, "redirected to "+document.URL)
	}
	return strings.Join(parts, " · ")
}

func webSearchText(report web.Report) string {
	var builder strings.Builder
	if len(report.Results) == 0 {
		builder.WriteString("(no results)")
	}
	for index, result := range report.Results {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		title := result.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&builder, "%d. %s\n   %s", index+1, title, result.URL)
		if result.Snippet != "" {
			builder.WriteString("\n   " + result.Snippet)
		}
	}
	backend := report.Backend
	if backend == "" {
		backend = "unknown backend"
	}
	return builder.String() + fmt.Sprintf("\n\n[%d results · %s]", len(report.Results), backend)
}

// capWebText applies the tool output cap, keeping the trailing summary line of a short result and
// never cutting a rune in half.
func capWebText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for len(cut) > 0 && !utf8.RuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	if runeValue, size := utf8.DecodeLastRuneInString(cut); runeValue == utf8.RuneError && size == 1 {
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n\n[truncated at %d bytes; the response is larger — fetch a narrower URL or use bash + curl]", limit)
}

func (registry *Registry) webClient() *web.Client {
	if registry != nil && registry.options.WebClient != nil {
		return registry.options.WebClient
	}
	return web.DefaultClient
}

func (registry *Registry) webLimits() web.Limits {
	var limits web.Limits
	if registry == nil {
		return limits
	}
	limits = web.Limits{Timeout: registry.options.WebTimeout, MaxBytes: registry.options.WebMaxBytes}
	if registry.store != nil {
		if timeout := registry.store.Duration("web.timeout"); timeout > 0 {
			limits.Timeout = timeout
		}
		if size := registry.store.Size("web.max_bytes"); size > 0 {
			limits.MaxBytes = size
		}
	}
	return limits
}

// webSearchKeySetting names the credential setting of one backend.
func webSearchKeySetting(provider string) string {
	return "web." + provider + "_api_key"
}

// webSearchProvider reads web.search_provider: the backend web_search uses, or empty for automatic
// selection.
func (registry *Registry) webSearchProvider() string {
	if registry == nil || registry.store == nil {
		return ""
	}
	return stringValueOrEmpty(registry.store.StringNonempty("web.search_provider"))
}

func (registry *Registry) webSearchKeys() web.Keys {
	var keys web.Keys
	if registry == nil || registry.store == nil {
		return keys
	}
	key := func(provider string) string {
		return stringValueOrEmpty(registry.store.StringNonempty(webSearchKeySetting(provider)))
	}
	return web.Keys{
		Brave:     key(web.BackendBrave),
		You:       key(web.BackendYou),
		Octen:     key(web.BackendOcten),
		Exa:       key(web.BackendExa),
		Firecrawl: key(web.BackendFirecrawl),
		TinyFish:  key(web.BackendTinyFish),
	}
}

func (registry *Registry) noWeb() bool {
	if registry == nil {
		return false
	}
	if registry.store != nil {
		return registry.store.Bool("no_web")
	}
	return registry.options.NoWeb
}

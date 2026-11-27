package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/web"
)

type webFetchTool struct {
	reg    *Registry
	client *web.Client
}

func (t *webFetchTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "web_fetch",
		Description: "Fetch an http:// or https:// URL and convert HTML responses to plain text.",
		Params: []core.ToolParam{
			{Name: "url", Type: "string", Required: true, Description: "Absolute http:// or https:// URL to fetch."},
			{Name: "strip", Type: "boolean", Description: "Convert HTML to plain text (default true)."},
		},
	}
}

func (t *webFetchTool) Run(ctx context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		URL   string `json:"url"`
		Strip *bool  `json:"strip"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	strip := true
	if args.Strip != nil {
		strip = *args.Strip
	}
	lim := web.Limits{
		Timeout:  t.reg.store.Duration("web.timeout"),
		MaxBytes: t.reg.store.Bytes("web.max_bytes"),
	}
	doc, err := t.client.Fetch(ctx, args.URL, lim)
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err), IsError: true}
	}
	if !doc.Textual() {
		return Result{Text: fmt.Sprintf("fetched %s (%s, %d bytes; binary content omitted)", doc.URL, doc.ContentType, len(doc.Body))}
	}
	text := string(doc.Body)
	if strip && doc.HTML() {
		text = web.HTMLToText(text)
	}
	return Result{Text: capOutput(text, t.reg.outputCap(), "gax-fetch")}
}

type webSearchTool struct {
	reg    *Registry
	client *web.Client
}

func (t *webSearchTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "web_search",
		Description: "Search the web and return ranked titles, URLs, and snippets.",
		Params: []core.ToolParam{
			{Name: "query", Type: "string", Required: true, Description: "Search query."},
			{Name: "count", Type: "integer", Minimum: intPtr(1), Description: "Maximum results (default 5, max 10)."},
		},
	}
}

func (t *webSearchTool) Run(ctx context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	rep, err := t.client.Search(ctx, args.Query, web.SearchOptions{
		Provider: t.reg.store.String("web.search_provider"),
		Count:    args.Count,
		Keys: web.Keys{
			You:       t.reg.store.String("web.you_api_key"),
			Brave:     t.reg.store.String("web.brave_api_key"),
			Octen:     t.reg.store.String("web.octen_api_key"),
			Exa:       t.reg.store.String("web.exa_api_key"),
			Firecrawl: t.reg.store.String("web.firecrawl_api_key"),
			TinyFish:  t.reg.store.String("web.tinyfish_api_key"),
			Keenable:  t.reg.store.String("web.keenable_api_key"),
		},
		Limits: web.Limits{
			Timeout:  t.reg.store.Duration("web.timeout"),
			MaxBytes: t.reg.store.Bytes("web.max_bytes"),
		},
	})
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err), IsError: true}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Search results (%s):\n", rep.Backend)
	for i, r := range rep.Results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return Result{Text: strings.TrimRight(b.String(), "\n")}
}

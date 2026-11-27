// Package web implements web_fetch (HTML-to-text) and multi-provider web_search.
package web

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeout  = 30 * time.Second
	DefaultMaxBytes = 2 << 20
)

// Limits bounds wall-clock duration and retained response bytes.
type Limits struct {
	Timeout  time.Duration
	MaxBytes int64
}

func (l Limits) resolved() Limits {
	if l.Timeout <= 0 {
		l.Timeout = DefaultTimeout
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	return l
}

// Client executes web_fetch and web_search HTTP calls.
type Client struct {
	HTTP               *http.Client
	UserAgent          string
	Endpoints          map[string]string
	HTMLSearchEndpoint string
}

// NewClient constructs a Client with production endpoints.
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{},
		UserAgent: "gax/2.0 (+https://github.com/luank3/gax)",
		Endpoints: map[string]string{
			BackendYou:             "https://ydc-index.io/v1/search",
			BackendYou + "_mcp":    "https://api.you.com/mcp?profile=free",
			BackendBrave:           "https://api.search.brave.com/res/v1/web/search",
			BackendOcten:           "https://api.octen.ai/search",
			BackendExa:             "https://api.exa.ai/search",
			BackendFirecrawl:       "https://api.firecrawl.dev/v2/search",
			BackendTinyFish:        "https://api.search.tinyfish.ai",
			BackendKeenable:        "https://api.keenable.ai/v1/search",
			BackendKeenable + "_p": "https://api.keenable.ai/v1/search/public",
		},
		HTMLSearchEndpoint: "https://html.duckduckgo.com/html/",
	}
}

// Document is the result of Fetch.
type Document struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
	Truncated   bool
}

// HTML reports whether the response is HTML/XHTML.
func (d Document) HTML() bool {
	return d.ContentType == "text/html" || d.ContentType == "application/xhtml+xml"
}

// Textual reports whether the response is readable text.
func (d Document) Textual() bool {
	if strings.HasPrefix(d.ContentType, "text/") ||
		strings.HasSuffix(d.ContentType, "+json") ||
		strings.HasSuffix(d.ContentType, "+xml") {
		return true
	}
	switch d.ContentType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-ndjson", "application/yaml", "application/x-yaml":
		return true
	}
	return false
}

// Fetch retrieves rawURL within limits.
func (c *Client) Fetch(ctx context.Context, rawURL string, lim Limits) (Document, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Document{}, fmt.Errorf("invalid URL %q (must be http:// or https://)", rawURL)
	}
	lim = lim.resolved()
	reqCtx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Document{}, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.9,text/plain;q=0.8,*/*;q=0.5")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Document{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, lim.MaxBytes+1))
	if err != nil {
		return Document{}, err
	}
	truncated := int64(len(raw)) > lim.MaxBytes
	if truncated {
		raw = raw[:lim.MaxBytes]
	}

	ct := "application/octet-stream"
	if header := resp.Header.Get("Content-Type"); header != "" {
		if med, _, perr := mime.ParseMediaType(header); perr == nil {
			ct = strings.ToLower(med)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Document{URL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: ct, Body: raw, Truncated: truncated},
			fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, rawURL)
	}
	return Document{
		URL:         resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: ct,
		Body:        raw,
		Truncated:   truncated,
	}, nil
}

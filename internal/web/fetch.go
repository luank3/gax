// Package web implements the fetching, HTML-to-text conversion, and search backends behind gax's
// web tools. The ported tool set has no web tools, so this is a deliberate gax addition.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luank3/gax/internal/version"
)

const (
	DefaultTimeout  = 30 * time.Second
	DefaultMaxBytes = 2 << 20
)

// fetchAccept asks for pages first and falls back to anything, so servers that negotiate return
// HTML where they can.
const fetchAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.9,text/plain;q=0.8,*/*;q=0.5"

// Client carries the HTTP policy shared by the web tools: one connection pool, one identity, and
// the endpoints a test replaces with its own server.
type Client struct {
	HTTP               *http.Client
	UserAgent          string
	BraveEndpoint      string
	YouEndpoint        string
	YouMCPEndpoint     string
	OctenEndpoint      string
	ExaEndpoint        string
	FirecrawlEndpoint  string
	TinyFishEndpoint   string
	HTMLSearchEndpoint string
}

// DefaultClient is the process-wide client; it follows redirects and honors the proxy environment.
// The provider transport is deliberately not reused: it never follows redirects.
var DefaultClient = NewClient()

func NewClient() *Client {
	return &Client{
		HTTP:               &http.Client{},
		UserAgent:          "gax/" + version.Value + " (+https://github.com/luank3/gax)",
		BraveEndpoint:      "https://api.search.brave.com/res/v1/web/search",
		YouEndpoint:        "https://ydc-index.io/v1/search",
		YouMCPEndpoint:     "https://api.you.com/mcp?profile=free",
		OctenEndpoint:      "https://api.octen.ai/search",
		ExaEndpoint:        "https://api.exa.ai/search",
		FirecrawlEndpoint:  "https://api.firecrawl.dev/v2/search",
		TinyFishEndpoint:   "https://api.search.tinyfish.ai",
		HTMLSearchEndpoint: "https://html.duckduckgo.com/html/",
	}
}

// Limits bound one web request: the wall-clock budget and the response bytes kept.
type Limits struct {
	Timeout  time.Duration
	MaxBytes int64
}

func (limits Limits) resolved() Limits {
	if limits.Timeout <= 0 {
		limits.Timeout = DefaultTimeout
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxBytes
	}
	return limits
}

// Document is a fetched response. The body is UTF-8 for the text types this package recognizes,
// and truncated at the byte limit; a binary response keeps its bytes so the caller can report the
// type and size without dumping them into the conversation.
type Document struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
	Truncated   bool
}

// HTML reports whether the body is an HTML document worth converting to text.
func (document Document) HTML() bool {
	return document.ContentType == "text/html" || document.ContentType == "application/xhtml+xml"
}

// Textual reports whether the body is text the model can read.
func (document Document) Textual() bool {
	return IsTextType(document.ContentType)
}

// IsTextType reports whether a media type carries text. Types ending in +json or +xml count: they
// are structured text with the same JSON/XML media type shape.
func IsTextType(contentType string) bool {
	if contentType == "" {
		return false
	}
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	if strings.HasSuffix(contentType, "+json") || strings.HasSuffix(contentType, "+xml") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/javascript", "application/ecmascript",
		"application/x-ndjson", "application/x-www-form-urlencoded", "application/yaml", "application/x-yaml":
		return true
	}
	return false
}

// Fetch retrieves a URL. A non-2xx status is a successful fetch: the caller decides what to report.
func (client *Client) Fetch(ctx context.Context, rawURL string, limits Limits) (Document, error) {
	limits = limits.resolved()
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return Document{}, fmt.Errorf("invalid URL %q", rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Document{}, fmt.Errorf("unsupported URL scheme %q (use http:// or https://)", parsed.Scheme)
	}
	requestContext, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Document{}, err
	}
	request.Header.Set("User-Agent", client.UserAgent)
	request.Header.Set("Accept", fetchAccept)
	response, err := client.httpClient().Do(request)
	if err != nil {
		return Document{}, requestError(requestContext, err, limits.Timeout)
	}
	defer func() { _ = response.Body.Close() }()
	body, truncated, err := readLimited(response.Body, limits.MaxBytes)
	if err != nil {
		return Document{}, requestError(requestContext, err, limits.Timeout)
	}
	header := response.Header.Get("Content-Type")
	contentType, charset := mediaType(header)
	finalURL := parsed.String()
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return Document{
		URL: finalURL, Status: response.StatusCode, ContentType: contentType,
		Body: decodeCharset(body, charset), Truncated: truncated,
	}, nil
}

func (client *Client) httpClient() *http.Client {
	if client != nil && client.HTTP != nil {
		return client.HTTP
	}
	return DefaultClient.HTTP
}

func (client *Client) userAgent() string {
	if client != nil && client.UserAgent != "" {
		return client.UserAgent
	}
	return DefaultClient.UserAgent
}

// get performs a request with the client identity and the given headers, which may override it,
// and returns the body plus the status.
func (client *Client) get(ctx context.Context, endpoint string, headers []string, limits Limits) ([]byte, int, error) {
	return client.send(ctx, http.MethodGet, endpoint, nil, headers, limits)
}

// postJSON sends an encoded body as application/json, with the same identity and header rules as
// get.
func (client *Client) postJSON(ctx context.Context, endpoint string, payload any, headers []string, limits Limits) ([]byte, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to encode the request: %w", err)
	}
	return client.send(ctx, http.MethodPost, endpoint, encoded, append([]string{"Content-Type: application/json"}, headers...), limits)
}

func (client *Client) send(ctx context.Context, method, endpoint string, body []byte, headers []string, limits Limits) ([]byte, int, error) {
	limits = limits.resolved()
	requestContext, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(requestContext, method, endpoint, reader)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", client.userAgent())
	for _, header := range headers {
		name, value, found := strings.Cut(header, ":")
		if !found {
			return nil, 0, fmt.Errorf("invalid HTTP header %q", header)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimLeft(value, " \t")
		if strings.EqualFold(name, "User-Agent") {
			request.Header.Set(name, value)
			continue
		}
		request.Header.Add(name, value)
	}
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, 0, requestError(requestContext, err, limits.Timeout)
	}
	defer func() { _ = response.Body.Close() }()
	received, _, err := readLimited(response.Body, limits.MaxBytes)
	if err != nil {
		return nil, response.StatusCode, requestError(requestContext, err, limits.Timeout)
	}
	return received, response.StatusCode, nil
}

// requestError names the deadline and cancellation cases the model can act on, instead of the
// opaque context error.
func requestError(ctx context.Context, err error, timeout time.Duration) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return errors.New("interrupted")
	}
	return err
}

func readLimited(reader io.Reader, limit int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return body[:limit], true, nil
	}
	return body, false, nil
}

// mediaType splits a Content-Type header into its media type and charset parameter. A header that
// does not parse still yields its first segment, so "text/html; broken" stays text/html.
func mediaType(header string) (string, string) {
	value := strings.TrimSpace(header)
	if parsed, parameters, err := mime.ParseMediaType(value); err == nil {
		return strings.ToLower(parsed), strings.ToLower(parameters["charset"])
	}
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = value[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(value)), ""
}

// decodeCharset converts the legacy single-byte charsets that still appear in HTML to UTF-8.
// ISO-8859-1 is treated as Windows-1252, matching what browsers do with a declared latin-1 page.
func decodeCharset(body []byte, charset string) []byte {
	switch charset {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1", "cp1252", "windows-1252", "x-cp1252":
	default:
		return body
	}
	ascii := true
	for _, char := range body {
		if char >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return body
	}
	var builder strings.Builder
	builder.Grow(len(body))
	for _, char := range body {
		switch {
		case char < 0x80:
			builder.WriteByte(char)
		case char < 0xa0:
			builder.WriteRune(windows1252High[char-0x80])
		default:
			builder.WriteRune(rune(char))
		}
	}
	return []byte(builder.String())
}

// windows1252High maps 0x80..0x9F. Undefined positions keep their raw byte value as a rune, which
// sanitizes to a replacement character rather than silently inventing a glyph.
var windows1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8d, 'Ž', 0x8f,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9d, 'ž', 'Ÿ',
}

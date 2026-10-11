package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/web"
)

func TestFetchReturnsTextDocumentWithFinalURLAndHeaders(t *testing.T) {
	var requested *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		requested = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = io.WriteString(w, `<html><body><h1>Hi</h1><p>Body &amp; more</p></body></html>`)
	}))
	defer server.Close()

	document, err := web.NewClient().Fetch(context.Background(), server.URL+"/start", web.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if document.Status != http.StatusOK || document.ContentType != "text/html" || document.Truncated {
		t.Fatalf("document = %+v", document)
	}
	if document.URL != server.URL+"/final" {
		t.Fatalf("final URL = %q", document.URL)
	}
	if !document.HTML() || !document.Textual() {
		t.Fatalf("HTML/text classification = %t/%t", document.HTML(), document.Textual())
	}
	if got := web.Text(document.Body); got != "Hi\nBody & more\n" {
		t.Fatalf("stripped text = %q", got)
	}
	if got := requested.Header.Get("User-Agent"); !strings.HasPrefix(got, "gax/") {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := requested.Header.Get("Accept"); !strings.Contains(got, "text/html") {
		t.Fatalf("Accept = %q", got)
	}
}

func TestFetchReportsStatusAndTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "no such page")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("x", 100))
		}
	}))
	defer server.Close()

	missing, err := web.NewClient().Fetch(context.Background(), server.URL+"/missing", web.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != http.StatusNotFound || string(missing.Body) != "no such page" {
		t.Fatalf("missing document = %+v", missing)
	}
	truncated, err := web.NewClient().Fetch(context.Background(), server.URL+"/big", web.Limits{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.Truncated || len(truncated.Body) != 10 || !truncated.Textual() {
		t.Fatalf("truncated document = %+v", truncated)
	}
}

func TestFetchReportsBinaryAndRejectsUnsupportedTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	}))
	defer server.Close()

	document, err := web.NewClient().Fetch(context.Background(), server.URL, web.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if document.Textual() || document.HTML() {
		t.Fatalf("binary document classified as text: %+v", document)
	}
	if _, err := web.NewClient().Fetch(context.Background(), "ftp://example.com/x", web.Limits{}); err == nil ||
		!strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("unsupported scheme error = %v", err)
	}
	if _, err := web.NewClient().Fetch(context.Background(), "not a url", web.Limits{}); err == nil ||
		!strings.Contains(err.Error(), "invalid URL") {
		t.Fatalf("invalid URL error = %v", err)
	}
}

func TestFetchDecodesLegacyCharsets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		// "café" and a Windows-1252 right single quote.
		_, _ = w.Write([]byte{'<', 'p', '>', 'c', 'a', 'f', 0xe9, ' ', 0x92, 's', '<', '/', 'p', '>'})
	}))
	defer server.Close()

	document, err := web.NewClient().Fetch(context.Background(), server.URL, web.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := web.Text(document.Body); got != "café ’s\n" {
		t.Fatalf("decoded text = %q", got)
	}
}

func TestFetchTimesOutAndHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "late")
	}))
	defer server.Close()
	defer close(release)

	_, err := web.NewClient().Fetch(context.Background(), server.URL, web.Limits{Timeout: 20 * time.Millisecond})
	if err == nil || err.Error() != "timed out after 20ms" {
		t.Fatalf("timeout error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = web.NewClient().Fetch(ctx, server.URL, web.Limits{Timeout: time.Minute})
	if err == nil || err.Error() != "interrupted" {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestIsTextTypeClassifiesMediaTypes(t *testing.T) {
	tests := map[string]bool{
		"":                         false,
		"text/html":                true,
		"text/plain":               true,
		"application/json":         true,
		"application/ld+json":      true,
		"application/rss+xml":      true,
		"application/pdf":          false,
		"image/png":                false,
		"application/octet-stream": false,
	}
	for contentType, want := range tests {
		if got := web.IsTextType(contentType); got != want {
			t.Errorf("IsTextType(%q) = %t, want %t", contentType, got, want)
		}
	}
}

func TestMediaTypeFallsBackToTheFirstSegment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; broken")
		_, _ = fmt.Fprint(w, "<p>ok</p>")
	}))
	defer server.Close()
	document, err := web.NewClient().Fetch(context.Background(), server.URL, web.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if document.ContentType != "text/html" {
		t.Fatalf("content type = %q", document.ContentType)
	}
}

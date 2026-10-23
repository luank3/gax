package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/web"
)

const keenableResults = `{"query":"golang timeout","mode":"pro","results":[
	{"title":"<b>Go</b> timeouts","url":"https://blog.example.com/guide","description":"Short summary.","snippet":"A <b>highlight</b>.\nMore details."},
	{"title":"Stack Overflow","url":"https://stackoverflow.com/q/1","description":"Use a context deadline."},
	{"title":"Description fallback","url":"https://example.com/description","description":"A useful description.","snippet":" \t "},
	{"title":" ","url":" ","snippet":"Ignore this entry."}
]}`

func TestSearchSelectsKeenable(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{Provider: "keenable"})
	if err != nil || report.Backend != "keenable" || len(report.Results) != 3 || recorder.path() != "/keenable-public" {
		t.Fatalf("Search(keenable) = %+v, %v at %q; want public Keenable results", report, err, recorder.path())
	}
}

func TestKeenableEndpoints(t *testing.T) {
	client := web.NewClient()
	if client.KeenableEndpoint != "https://api.keenable.ai/v1/search" || client.KeenablePublicEndpoint != "https://api.keenable.ai/v1/search/public" {
		t.Errorf("NewClient() Keenable endpoints = %q, %q; want /v1/search and /v1/search/public", client.KeenableEndpoint, client.KeenablePublicEndpoint)
	}
}

func TestKeenableRequestsAndResults(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		count int
		limit int
		path  string
	}{
		{name: "keyed default count", key: " keen-secret ", limit: 5, path: "/keenable"},
		{name: "public default count", limit: 5, path: "/keenable-public"},
		{name: "public whitespace key", key: " \t ", limit: 5, path: "/keenable-public"},
		{name: "one result", key: "keen-secret", count: 1, limit: 1, path: "/keenable"},
		{name: "clamped count", key: "keen-secret", count: 99, limit: 10, path: "/keenable"},
		{name: "negative count", count: -1, limit: 5, path: "/keenable-public"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, recorder := newSearchTestServer(t)
			report, err := client.Search(context.Background(), "  golang timeout  ", web.SearchOptions{
				Provider: "keenable", Count: test.count, Keys: web.Keys{Keenable: test.key},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []web.Result{
				{Title: "Go timeouts", URL: "https://blog.example.com/guide", Snippet: "A highlight. More details."},
				{Title: "Stack Overflow", URL: "https://stackoverflow.com/q/1", Snippet: "Use a context deadline."},
				{Title: "Description fallback", URL: "https://example.com/description", Snippet: "A useful description."},
			}
			want = want[:min(test.limit, len(want))]
			if report.Backend != "keenable" || len(report.Results) != len(want) {
				t.Fatalf("Search(keenable, count=%d) = %+v; want %d Keenable results", test.count, report, len(want))
			}
			for index, result := range want {
				if report.Results[index] != result {
					t.Errorf("Search(keenable) result %d = %+v, want %+v", index, report.Results[index], result)
				}
			}
			request, _ := recorder.snapshot()
			if recorder.path() != test.path || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Keenable request = %s %s, Content-Type %q; want POST %s application/json", request.Method, recorder.path(), request.Header.Get("Content-Type"), test.path)
			}
			if got := recorder.header("X-API-Key"); got != strings.TrimSpace(test.key) {
				t.Errorf("Keenable X-API-Key = %q, want %q", got, strings.TrimSpace(test.key))
			}
			if test.path == "/keenable-public" && recorder.header("X-Keenable-Title") != "gax" {
				t.Errorf("public Keenable X-Keenable-Title = %q, want gax", recorder.header("X-Keenable-Title"))
			}
			body := recorder.jsonBody(t)
			if body["query"] != "golang timeout" || body["max_results"] != float64(test.limit) || body["snippet_max_length"] != float64(web.MaxSnippetChars) || len(body) != 3 {
				t.Errorf("Keenable request body = %#v; want query, max_results=%d, snippet_max_length=%d", body, test.limit, web.MaxSnippetChars)
			}
		})
	}
}

func TestKeenableFailuresAndFallback(t *testing.T) {
	for _, test := range []struct {
		name     string
		key      string
		status   int
		body     string
		want     string
		fallback bool
	}{
		{name: "malformed key", key: "keen-secret", status: 400, body: `{"error":"keen-secret is malformed"}`, want: "check KEENABLE_API_KEY"},
		{name: "rejected key", key: "keen-secret", status: 401, body: `{"error":"keen-secret is invalid"}`, want: "check KEENABLE_API_KEY"},
		{name: "revoked key", key: "keen-secret", status: 403, body: `{"error":"keen-secret was revoked"}`, want: "check KEENABLE_API_KEY"},
		{name: "no credit", key: "keen-secret", status: 402, body: `{"error":"keen-secret has no credits"}`, want: "no credit left"},
		{name: "keyed rate limit", key: "keen-secret", status: 429, body: `{}`, fallback: true},
		{name: "public rate limit", status: 429, body: `{}`, fallback: true},
		{name: "public refusal", status: 403, body: `{}`, fallback: true},
		{name: "server error", key: "keen-secret", status: 502, body: `{}`, fallback: true},
		{name: "invalid JSON", key: "keen-secret", status: 200, body: `not JSON`, fallback: true},
		{name: "invalid result shape", status: 200, body: `{"results":"unexpected"}`, fallback: true},
		{name: "empty results", key: "keen-secret", status: 200, body: `{"results":[]}`, want: "the Keenable API returned no results"},
		{name: "unusable results", status: 200, body: `{"results":[{"url":" "}]}`, want: "the Keenable API returned no results"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, recorder := newFailingSearchTestServer(t, test.status, test.body)
			report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{
				Provider: "keenable", Keys: web.Keys{Keenable: test.key},
			})
			if test.fallback {
				if err != nil || report.Backend != web.BackendDuckDuckGo || recorder.path() != "/crawl" {
					t.Fatalf("Search(keenable, HTTP %d) = %+v, %v at %q; want DuckDuckGo fallback", test.status, report, err, recorder.path())
				}
				if recorder.header("X-API-Key") != "" {
					t.Error("Keenable credential was forwarded to DuckDuckGo")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || recorder.path() == "/crawl" {
				t.Fatalf("Search(keenable, HTTP %d) = %+v, %v at %q; want %q without fallback", test.status, report, err, recorder.path(), test.want)
			}
			if strings.Contains(err.Error(), "keen-secret") {
				t.Errorf("Keenable error leaked credential: %v", err)
			}
		})
	}
}

func TestKeenableHonorsCancellationAndByteLimit(t *testing.T) {
	client, recorder := newSearchTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(ctx, "q", web.SearchOptions{Provider: "keenable"}); err == nil || recorder.path() != "" {
		t.Errorf("Search(keenable, canceled) error = %v at %q; want an error without an HTTP request", err, recorder.path())
	}
	_, err := client.Search(context.Background(), "q", web.SearchOptions{Provider: "keenable", Limits: web.Limits{MaxBytes: 8}})
	if err == nil || !strings.Contains(err.Error(), "the Keenable API response is not valid JSON") {
		t.Errorf("Search(keenable, MaxBytes=8) error = %v; want a truncated JSON diagnostic", err)
	}
}

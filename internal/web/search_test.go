package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/web"
)

const duckDuckGoFixture = `<html><body>
<div class="result results_links results_links_deep web-result">
  <a rel="nofollow" class="result__a" href="https://blog.example.com/guide">The complete <b>guide</b></a>
  <a class="result__snippet" href="https://blog.example.com/guide">In this post I&#x27;ll take apart the stages you might need a <b>timeout</b> for.</a>
</div>
<div class="result results_links results_links_deep web-result">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fstackoverflow.com%2Fq%2F1&amp;rut=abc">How to set a timeout?</a>
  <a class="result__snippet" href="https://stackoverflow.com/q/1">Use a context deadline instead.</a>
</div>
<div class="no-results">No more results.</div>
</body></html>`

func TestSearchParsesDuckDuckGoHTMLResults(t *testing.T) {
	var requested *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, duckDuckGoFixture)
	}))
	defer server.Close()

	client := &web.Client{HTTP: server.Client(), HTMLSearchEndpoint: server.URL + "/html/"}
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{Count: 5})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != web.BackendDuckDuckGo {
		t.Fatalf("backend = %q, want %q", report.Backend, web.BackendDuckDuckGo)
	}
	want := []web.Result{
		{
			Title:   "The complete guide",
			URL:     "https://blog.example.com/guide",
			Snippet: "In this post I'll take apart the stages you might need a timeout for.",
		},
		{
			Title:   "How to set a timeout?",
			URL:     "https://stackoverflow.com/q/1",
			Snippet: "Use a context deadline instead.",
		},
	}
	if len(report.Results) != len(want) {
		t.Fatalf("results = %+v, want %d entries", report.Results, len(want))
	}
	for index, expected := range want {
		if report.Results[index] != expected {
			t.Fatalf("result %d = %+v, want %+v", index, report.Results[index], expected)
		}
	}
	if got := requested.URL.Query().Get("q"); got != "golang timeout" {
		t.Fatalf("query = %q", got)
	}
	if got := requested.Header.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Fatalf("DuckDuckGo User-Agent = %q", got)
	}
	if got := requested.Header.Get("Accept"); !strings.Contains(got, "text/html") {
		t.Fatalf("DuckDuckGo Accept = %q", got)
	}
}

func TestSearchLimitsResultsAndReportsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/throttled":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "<html>anomaly</html>")
		case "/empty":
			_, _ = io.WriteString(w, "<html><body>nothing here</body></html>")
		default:
			_, _ = io.WriteString(w, duckDuckGoFixture)
		}
	}))
	defer server.Close()

	client := &web.Client{HTTP: server.Client(), HTMLSearchEndpoint: server.URL + "/results"}
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].URL != "https://blog.example.com/guide" {
		t.Fatalf("limited search = %+v", report.Results)
	}

	client = &web.Client{HTTP: server.Client(), HTMLSearchEndpoint: server.URL + "/throttled"}
	if _, err := client.Search(context.Background(), "q", web.SearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "DuckDuckGo returned HTTP 202") {
		t.Fatalf("throttled search error = %v", err)
	}
	client = &web.Client{HTTP: server.Client(), HTMLSearchEndpoint: server.URL + "/empty"}
	if _, err := client.Search(context.Background(), "q", web.SearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "BRAVE_API_KEY") {
		t.Fatalf("empty search error = %v", err)
	}
	if _, err := client.Search(context.Background(), "   ", web.SearchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "empty search query") {
		t.Fatalf("blank query error = %v", err)
	}
}

func TestSearchUsesTheBraveAPIWhenAKeyIsConfigured(t *testing.T) {
	var requested *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"web":{"results":[
			{"title":"<strong>Go</strong> timeouts","url":"https://blog.example.com/guide","description":"Take apart the <b>stages</b> of a timeout."},
			{"title":"Stack Overflow","url":"https://stackoverflow.com/q/1","description":"Use a context deadline."}
		]}}`)
	}))
	defer server.Close()

	client := &web.Client{HTTP: server.Client(), BraveEndpoint: server.URL + "/res/v1/web/search"}
	report, err := client.Search(context.Background(), "golang timeout", web.SearchOptions{APIKey: "brave-secret", Count: 99})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != web.BackendBrave || len(report.Results) != 2 {
		t.Fatalf("brave report = %+v", report)
	}
	if report.Results[0].Title != "Go timeouts" || report.Results[0].Snippet != "Take apart the stages of a timeout." {
		t.Fatalf("brave result markup was not stripped: %+v", report.Results[0])
	}
	if got := requested.URL.Query().Get("count"); got != "10" {
		t.Fatalf("brave count = %q, want the clamped maximum", got)
	}
	if got := requested.URL.Query().Get("q"); got != "golang timeout" {
		t.Fatalf("brave query = %q", got)
	}
	if got := requested.Header.Get("X-Subscription-Token"); got != "brave-secret" {
		t.Fatalf("brave subscription header = %q", got)
	}
	if got := requested.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("brave Accept = %q", got)
	}
}

func TestSearchReportsBraveKeyAndRateLimitFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "rejected key", status: http.StatusUnauthorized, want: "BRAVE_API_KEY"},
		{name: "forbidden key", status: http.StatusForbidden, want: "BRAVE_API_KEY"},
		{name: "rate limit", status: http.StatusTooManyRequests, want: "rate limit"},
		{name: "server error", status: http.StatusBadGateway, want: "HTTP 502"},
		{name: "invalid json", status: http.StatusOK, body: "not json", want: "not valid JSON"},
		{name: "empty listing", status: http.StatusOK, body: `{"web":{"results":[]}}`, want: "no results"},
		{
			name:   "listing without usable entries",
			status: http.StatusOK,
			body:   `{"web":{"results":[{"title":"","url":"","description":""},{"title":"   ","url":"  "}]}}`,
			want:   "no results",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := &web.Client{HTTP: server.Client(), BraveEndpoint: server.URL}
			_, err := client.Search(context.Background(), "q", web.SearchOptions{APIKey: "brave-secret"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("brave failure error = %v, want substring %q", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "brave-secret") {
				t.Fatalf("brave error leaked the key: %v", err)
			}
		})
	}
}

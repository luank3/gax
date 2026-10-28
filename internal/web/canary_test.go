package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/web"
)

func TestSearchCanaryFixtures(t *testing.T) {
	const emptyYou = `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":{"web":[]}}}}`
	const invalidYou = `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":{"web":[{"url":"not a url","title":"bad link"}]}}}}`
	tests := []struct {
		name       string
		provider   string
		youStatus  int
		youBody    string
		duckStatus int
		duckBody   string
		wantError  string
	}{
		{name: "you succeeds", provider: web.BackendYou, youStatus: 200, youBody: youFreeResults},
		{name: "duckduckgo succeeds", provider: web.BackendDuckDuckGo, duckStatus: 200, duckBody: crawlResults},
		{name: "fallback is not you", provider: web.BackendYou, youStatus: 503, duckStatus: 200, duckBody: crawlResults, wantError: "duckduckgo"},
		{name: "both fail", provider: web.BackendYou, youStatus: 503, duckStatus: 503, wantError: "503"},
		{name: "duckduckgo fails", provider: web.BackendDuckDuckGo, duckStatus: 503, wantError: "503"},
		{name: "empty you results", provider: web.BackendYou, youStatus: 200, youBody: emptyYou, wantError: "no results"},
		{name: "invalid result url", provider: web.BackendYou, youStatus: 200, youBody: invalidYou, wantError: "usable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
					t.Error("canary sent credentials")
				}
				if r.URL.Path == "/you" && test.youStatus != 0 {
					w.WriteHeader(test.youStatus)
					_, _ = w.Write([]byte(test.youBody))
					return
				}
				if r.URL.Path == "/duck" && test.duckStatus != 0 {
					w.WriteHeader(test.duckStatus)
					_, _ = w.Write([]byte(test.duckBody))
					return
				}
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			client := web.NewClient()
			client.YouMCPEndpoint = server.URL + "/you"
			client.HTMLSearchEndpoint = server.URL + "/duck"
			err := runSearchCanary(context.Background(), client, test.provider)
			if test.wantError == "" && err != nil {
				t.Fatalf("runSearchCanary(%q) = %v, want success", test.provider, err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("runSearchCanary(%q) = %v, want error containing %q", test.provider, err, test.wantError)
			}
		})
	}
}

func TestSearchCanaryHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("canceled canary sent a request to %s", r.URL.Path)
	}))
	defer server.Close()
	client := web.NewClient()
	client.YouMCPEndpoint = server.URL
	client.HTMLSearchEndpoint = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runSearchCanary(ctx, client, web.BackendYou); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("runSearchCanary(canceled) = %v, want interruption", err)
	}
}

func runSearchCanary(ctx context.Context, client *web.Client, provider string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	report, err := client.Search(ctx, "golang context timeout", web.SearchOptions{
		Provider: provider,
		Count:    3,
		Limits:   web.Limits{Timeout: 10 * time.Second},
	})
	if err != nil {
		return fmt.Errorf("%s search failed: %w", provider, err)
	}
	if report.Backend != provider {
		return fmt.Errorf("%s search returned backend %s instead", provider, report.Backend)
	}
	for _, result := range report.Results {
		parsed, err := url.Parse(result.URL)
		if err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http") {
			return nil
		}
	}
	return fmt.Errorf("%s search returned no usable result URLs", provider)
}

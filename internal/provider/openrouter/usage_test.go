package openrouter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/provider/openrouter"
	"github.com/luank3/gax/internal/transport"
)

func TestParseUsageFormatsKeyAndCredits(t *testing.T) {
	report, err := openrouter.ParseUsage(
		[]byte(`{"data":{"label":"sk-or-v1-secret","is_free_tier":true,"usage":0.00421,"limit":0.042,"limit_remaining":0.01}}`),
		[]byte(`{"data":{"total_credits":10,"total_usage":12}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	openrouter.WriteUsageReport(&output, report)
	want := "openrouter · free tier\n  spent   $0.0042\n  limit   $0.042 · $0.010 remaining\n  credits $0.00 of $10.00 remaining\n"
	if output.String() != want {
		t.Fatalf("usage report = %q, want %q", output.String(), want)
	}
}

func TestParseUsageKeepsSafeLabelsAndSkipsMissingValues(t *testing.T) {
	report, err := openrouter.ParseUsage([]byte(`{"data":{"label":"team\u001b(B\u001b]52;c;clipboard\u0007-key","usage":-2,"limit":"5"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	openrouter.WriteUsageReport(&output, report)
	if got, want := output.String(), "openrouter · team-key\n  spent   $0.00\n"; got != want {
		t.Fatalf("usage report = %q, want %q", got, want)
	}
}

func TestQueryUsageUsesAuthenticatedEndpointsAndIgnoresCreditsFailure(t *testing.T) {
	var keyCalls, creditCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer openrouter-secret" ||
			r.Header.Get("Accept") != "application/json" || r.Header.Get("X-Extra") != "configured" {
			t.Errorf("unexpected OpenRouter usage request: method=%s path=%q headers=%v", r.Method, r.URL.Path, r.Header)
		}
		switch r.URL.Path {
		case "/key":
			keyCalls++
			_, _ = io.WriteString(w, `{"data":{"usage":1.2,"limit":5}}`)
		case "/credits":
			creditCalls++
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected usage path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	report, err := openrouter.QueryUsage(context.Background(), server.URL, []string{
		"Authorization: Bearer openrouter-secret", "X-Extra: configured",
	}, true, &transport.Client{HTTP: server.Client()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keyCalls != 1 || creditCalls != 1 {
		t.Fatalf("endpoint calls: /key=%d /credits=%d", keyCalls, creditCalls)
	}
	var output strings.Builder
	openrouter.WriteUsageReport(&output, report)
	if !strings.Contains(output.String(), "  spent   $1.20\n") || strings.Contains(output.String(), "credits") {
		t.Fatalf("optional credits failure should leave key usage: %q", output.String())
	}
}

func TestQueryUsageRejectsMissingCredentialAndKeyResponseErrors(t *testing.T) {
	unused := httptest.NewServer(http.NotFoundHandler())
	defer unused.Close()
	if _, err := openrouter.QueryUsage(context.Background(), unused.URL, nil, false, &transport.Client{HTTP: unused.Client()}, nil); err == nil || err.Error() != "no OpenRouter API key configured" {
		t.Fatalf("missing-key diagnostic = %v", err)
	}

	for _, test := range []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: "OpenRouter rejected the configured API key (401)"},
		{name: "invalid JSON", status: http.StatusOK, body: "not-json", want: "usage response is not valid JSON:"},
		{name: "missing data", status: http.StatusOK, body: `{"data":[]}`, want: "unrecognized usage response shape (no data object)"},
		{name: "non-object JSON", status: http.StatusOK, body: `[]`, want: "unrecognized usage response shape (no data object)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := openrouter.QueryUsage(context.Background(), server.URL, nil, true, &transport.Client{HTTP: server.Client()}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "openrouter-secret") {
				t.Fatalf("query error = %v, want safely redacted %q", err, test.want)
			}
		})
	}
}

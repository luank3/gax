package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/transport"
)

func TestParseUsageFiltersAndCapsBalances(t *testing.T) {
	data := []byte(`{"is_available":false,"balance_infos":[
		{"currency":"USD","total_balance":"10.00","granted_balance":"2.50"},
		{"currency":"CNY","total_balance":"0","granted_balance":"0"},
		{"currency":"bad","total_balance":"12.00x"},
		{"currency":"EUR","total_balance":"3.25","granted_balance":"-1"},
		{"currency":"JPY","total_balance":"4"},
		{"currency":"GBP","total_balance":"5"}
	]}`)
	report, err := ParseUsage(data)
	if err != nil {
		t.Fatal(err)
	}
	if report.Available || len(report.Balances) != maxBalances {
		t.Fatalf("parsed balances = %+v", report)
	}
	if report.Balances[0] != (Balance{Currency: "USD", Total: "10.00", Granted: "2.50"}) ||
		report.Balances[1] != (Balance{Currency: "CNY", Total: "0"}) ||
		report.Balances[2] != (Balance{Currency: "EUR", Total: "3.25"}) {
		t.Fatalf("balance filtering = %+v", report.Balances)
	}
}

func TestParseUsageDefaultsAvailabilityAndRejectsNoBalances(t *testing.T) {
	report, err := ParseUsage([]byte(`{"balance_infos":[{"currency":"USD","total_balance":"1"}]}`))
	if err != nil || !report.Available {
		t.Fatalf("missing availability should default true: report=%+v err=%v", report, err)
	}
	if _, err := ParseUsage([]byte(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"NaN"}]}`)); err == nil {
		t.Fatal("expected a no-balances shape error")
	}
}

func TestWriteUsageReportFormatsAndSanitizesBalances(t *testing.T) {
	var output strings.Builder
	WriteUsageReport(&output, "deepseek\x1b[31m", UsageReport{
		Available: false,
		Balances:  []Balance{{Currency: "XYZ\nInjected", Total: "2.00", Granted: "1.00"}},
	})
	got := output.String()
	for _, want := range []string{"deepseek · balance too low for API calls", "balance 2.00 XYZ Injected · 1.00 XYZ Injected granted"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage output %q omits %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("usage report retained terminal control: %q", got)
	}
}

func TestQueryUsageFetchesBalanceWithProviderHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/balance" ||
			r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request mismatch: method=%s path=%q headers=%v", r.Method, r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"balance_infos":[{"currency":"USD","total_balance":"1"}]}`)
	}))
	defer server.Close()
	report, err := QueryUsage(context.Background(), server.URL+"/", []string{"Authorization: Bearer key"}, true,
		&transport.Client{HTTP: server.Client()}, nil)
	if err != nil || len(report.Balances) != 1 || !report.Available {
		t.Fatalf("usage query = %+v err=%v", report, err)
	}
}

func TestQueryUsageDoesNotExposeCredentialOnUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := QueryUsage(context.Background(), server.URL, []string{"Authorization: Bearer secret"}, true,
		&transport.Client{HTTP: server.Client()}, nil)
	if err == nil || err.Error() != "DeepSeek rejected the configured API key (401)" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unauthorized usage error = %v", err)
	}
}

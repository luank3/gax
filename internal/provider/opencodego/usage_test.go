package opencodego_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/provider/opencodego"
	"github.com/luank3/gax/internal/transport"
)

func TestParseUsagePreservesWindowOrderAndUTCResetTimes(t *testing.T) {
	report, err := opencodego.ParseUsage([]byte(`{"usage":{
		"rolling":{"status":"ok","percent":5,"resetsAt":"2026-08-21T21:14:51.969Z"},
		"weekly":{"status":"ok","percent":54,"resetsAt":"2026-08-24T00:00:00.969Z"},
		"monthly":{"status":"limited","percent":27,"resetsAt":"2026-09-19T13:32:19.969Z"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Windows) != 3 {
		t.Fatalf("parsed %d windows, want 3: %+v", len(report.Windows), report.Windows)
	}
	if report.Windows[0].Label != "rolling" || report.Windows[0].Percent != 5 || report.Windows[0].ResetAt.Unix() != 1787346891 || report.Windows[0].Note != nil {
		t.Fatalf("rolling window mismatch: %+v", report.Windows[0])
	}
	if report.Windows[1].Label != "weekly" || report.Windows[1].Percent != 54 || report.Windows[1].ResetAt.Unix() != 1787529600 {
		t.Fatalf("weekly window mismatch: %+v", report.Windows[1])
	}
	if report.Windows[2].Label != "monthly" || report.Windows[2].Note == nil || *report.Windows[2].Note != "limited" {
		t.Fatalf("monthly status should be retained: %+v", report.Windows[2])
	}
}

func TestParseUsageSkipsMalformedWindowsAndCapsAtEight(t *testing.T) {
	var body strings.Builder
	body.WriteString(`{"usage":{"missing_percent":{"resetsAt":"2026-08-24T00:00:00Z"},`)
	body.WriteString(`"offset":{"percent":1,"resetsAt":"2026-08-24T00:00:00+02:00"},`)
	body.WriteString(`"bad_day":{"percent":1,"resetsAt":"2026-02-31T00:00:00Z"},`)
	for i := 0; i < 9; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"window-%c":{"percent":%d,"resetsAt":"2026-08-24T00:00:00Z"}`, 'a'+i, i)
	}
	body.WriteString(`}}`)
	report, err := opencodego.ParseUsage([]byte(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Windows) != 8 || report.Windows[0].Label != "window-a" || report.Windows[7].Label != "window-h" {
		t.Fatalf("malformed windows should be skipped and valid windows capped in source order: %+v", report.Windows)
	}
}

func TestWriteUsageReportClampsAndSanitizesWindowRows(t *testing.T) {
	status := "lim\x1b[31mited\n now"
	var output strings.Builder
	opencodego.WriteUsageReport(&output, "opencode-go", opencodego.UsageReport{Windows: []opencodego.Window{{
		Label: "weekly\x1b[32m", Percent: 130,
		ResetAt: time.Date(2099, time.January, 2, 3, 4, 0, 0, time.UTC), Note: &status,
	}}}, 200)
	got := output.String()
	if !strings.HasPrefix(got, "opencode-go\n  weekly  "+strings.Repeat("█", 20)+" 100% used · resets ") || !strings.Contains(got, " · limited  now\n") {
		t.Fatalf("usage rows should clamp percentages and remove terminal controls: %q", got)
	}

	output.Reset()
	opencodego.WriteUsageReport(&output, "opencode-go", opencodego.UsageReport{Windows: []opencodego.Window{{
		Label: "weekly", Percent: 20, ResetAt: time.Now().Add(24 * time.Hour),
	}}}, 30)
	if !strings.Contains(output.String(), " used\n          resets ") {
		t.Fatalf("narrow rows should move reset details to an indented continuation: %q", output.String())
	}
}

func TestQueryUsageUsesBearerAuthAndUsageEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/usage" || r.Header.Get("Authorization") != "Bearer opencode-secret" ||
			r.Header.Get("Accept") != "application/json" || r.Header.Get("X-OpenCode") != "configured" {
			t.Errorf("unexpected OpenCode Go usage request: method=%s url=%q headers=%v", r.Method, r.URL, r.Header)
		}
		_, _ = io.WriteString(w, `{"usage":{"weekly":{"percent":32,"resetsAt":"2026-08-24T00:00:00Z"}}}`)
	}))
	defer server.Close()
	report, err := opencodego.QueryUsage(context.Background(), server.URL, []string{
		"Authorization: Bearer opencode-secret", "X-OpenCode: configured",
	}, true, &transport.Client{HTTP: server.Client()}, nil)
	if err != nil || len(report.Windows) != 1 || report.Windows[0].Label != "weekly" {
		t.Fatalf("usage query: report=%+v err=%v", report, err)
	}
}

func TestQueryUsageReportsMissingKeyAuthInvalidJSONAndEmptyShape(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := &transport.Client{HTTP: server.Client()}
	if _, err := opencodego.QueryUsage(context.Background(), server.URL, nil, false, client, nil); err == nil || err.Error() != "no OPENCODE_API_KEY configured" {
		t.Fatalf("missing-key diagnostic = %v", err)
	}

	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: "opencode rejected the configured API key (401)"},
		{name: "invalid JSON", status: http.StatusOK, body: "not-json", want: "usage response is not valid JSON:"},
		{name: "no windows", status: http.StatusOK, body: `{"usage":[]}`, want: "unrecognized usage response shape (no usage windows)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer response.Close()
			_, err := opencodego.QueryUsage(context.Background(), response.URL, nil, true, &transport.Client{HTTP: response.Client()}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "opencode-secret") {
				t.Fatalf("query error = %v, want safely redacted %q", err, test.want)
			}
		})
	}
}

package transport

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDefaultRetryPolicyClampsAndRoundsInputs(t *testing.T) {
	for _, test := range []struct {
		name        string
		retries     int
		baseDelay   time.Duration
		idleTimeout time.Duration
		want        RetryPolicy
	}{
		{
			name: "adds one attempt to the requested retries",
			want: RetryPolicy{MaxAttempts: 1, MaxDelay: maxRetryDelay},
		},
		{
			name:    "negative retries collapse to a single attempt",
			retries: -3,
			want:    RetryPolicy{MaxAttempts: 1, MaxDelay: maxRetryDelay},
		},
		{
			name:      "negative base delay collapses to none",
			retries:   2,
			baseDelay: -time.Second,
			want:      RetryPolicy{MaxAttempts: 3, MaxDelay: maxRetryDelay},
		},
		{
			name:        "idle timeout rounds up to a whole second",
			idleTimeout: 1500 * time.Millisecond,
			want:        RetryPolicy{MaxAttempts: 1, MaxDelay: maxRetryDelay, IdleTimeout: 2 * time.Second},
		},
		{
			name:        "a negative idle timeout is passed through unchanged",
			idleTimeout: -time.Second,
			want:        RetryPolicy{MaxAttempts: 1, MaxDelay: maxRetryDelay, IdleTimeout: -time.Second},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := DefaultRetryPolicy(test.retries, test.baseDelay, test.idleTimeout)
			if got != test.want {
				t.Fatalf("DefaultRetryPolicy(%d, %v, %v) = %+v, want %+v",
					test.retries, test.baseDelay, test.idleTimeout, got, test.want)
			}
		})
	}
}

func TestShouldRetryClassifiesStatusesAndTerminalQuotaErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "transport failure", status: 0, want: true},
		{name: "success", status: 200, want: false},
		{name: "empty success body", status: 204, want: false},
		{name: "request timeout", status: 408, want: true},
		{name: "bad request", status: 400, want: false},
		{name: "unauthorized", status: 401, want: false},
		{name: "rate limited", status: 429, want: true},
		{
			name:   "rate limit with a retryable body",
			status: 429,
			body:   `{"error":{"type":"rate_limit_exceeded"}}`,
			want:   true,
		},
		{
			name:   "quota exhausted by type",
			status: 429,
			body:   `{"error":{"type":"insufficient_quota"}}`,
			want:   false,
		},
		{
			name:   "quota exhausted by code, case-insensitive",
			status: 429,
			body:   `{"error":{"code":"Usage_Limit_Reached"}}`,
			want:   false,
		},
		{
			name:   "quota exhausted in a flat message",
			status: 429,
			body:   `{"message":"gousagelimiterror"}`,
			want:   true,
		},
		{name: "malformed body stays retryable", status: 429, body: "not json", want: true},
		{name: "internal error", status: 500, want: true},
		{name: "bad gateway", status: 502, want: true},
		{name: "service unavailable", status: 503, want: true},
		{name: "gateway timeout", status: 504, want: true},
		{name: "unassigned server error", status: 599, want: true},
		{name: "redirect is not retried", status: 302, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldRetry(test.status, test.body); got != test.want {
				t.Fatalf("ShouldRetry(%d, %q) = %t, want %t", test.status, test.body, got, test.want)
			}
		})
	}
}

func TestDelayDoublesWithJitterAndClampsToTheCeiling(t *testing.T) {
	// The loop doubles before it tests, so the pre-jitter delay at attempt n is MaxDelay/2 rounded
	// up to the next power of two: 12.8s at attempt 7, 25.6s at attempt 8, the 30s clamp after.
	const (
		almostSaturated = 12800 * time.Millisecond
		saturated       = 25600 * time.Millisecond
		capped          = maxRetryDelay
	)
	policy := DefaultRetryPolicy(6, 100*time.Millisecond, 0)
	for _, test := range []struct {
		attempt int
		base    time.Duration
	}{
		{attempt: -1, base: 100 * time.Millisecond},
		{attempt: 0, base: 100 * time.Millisecond},
		{attempt: 1, base: 200 * time.Millisecond},
		{attempt: 2, base: 400 * time.Millisecond},
		{attempt: 3, base: 800 * time.Millisecond},
		{attempt: 7, base: almostSaturated},
		{attempt: 8, base: saturated},
		{attempt: 20, base: capped},
		{attempt: 60, base: capped},
	} {
		got := Delay(policy, test.attempt)
		low := test.base * 75 / 100
		high := test.base * 125 / 100
		if high > maxRetryDelay {
			high = maxRetryDelay
		}
		if got < low || got > high {
			t.Fatalf("Delay(attempt %d) = %v, want within [%v, %v]", test.attempt, got, low, high)
		}
	}
}
func TestDelayIsZeroWithoutAConfiguredBackoff(t *testing.T) {
	for _, policy := range []RetryPolicy{
		{MaxAttempts: 3},
		{MaxAttempts: 3, BaseDelay: time.Second},
		{MaxAttempts: 3, MaxDelay: time.Minute},
		{MaxAttempts: 3, BaseDelay: -time.Second, MaxDelay: time.Minute},
	} {
		if got := Delay(policy, 4); got != 0 {
			t.Fatalf("Delay(%+v, 4) = %v, want 0", policy, got)
		}
	}
}

func TestDelayClampsTheJitterMultiplicationOverflow(t *testing.T) {
	// MaxDelay stays inside int64 so the jitter multiplication is what overflows: the final clamp
	// is the only thing that keeps the result finite.
	policy := RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   1 << 62,
		MaxDelay:    1<<62 + 1,
	}
	if got := Delay(policy, 0); got != policy.MaxDelay {
		t.Fatalf("Delay with an overflowing base = %v, want the ceiling %v", got, policy.MaxDelay)
	}
	if got := Delay(policy, 1); got != policy.MaxDelay {
		t.Fatalf("Delay with an overflowing doubling = %v, want the ceiling %v", got, policy.MaxDelay)
	}
}

func TestSleepWithTickReportsInterruptions(t *testing.T) {
	if SleepWithTick(nil, 0, nil) {
		t.Fatal("a zero delay without a tick should report no interruption")
	}
	calls := 0
	if SleepWithTick(context.Background(), 0, func() bool { calls++; return calls == 2 }) {
		t.Fatal("the first tick of a zero delay should not report an interruption")
	}
	if calls != 1 {
		t.Fatalf("zero delay called the tick %d times, want 1", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if !SleepWithTick(ctx, time.Minute, nil) {
		t.Fatal("a cancelled context should end the sleep early")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v, want a prompt return", elapsed)
	}

	if !SleepWithTick(context.Background(), 10*time.Millisecond, func() bool { return true }) {
		t.Fatal("a tick that reports an interruption should end the sleep and report it")
	}
}

func TestFormatAPIErrorPrefersTheJSONMessage(t *testing.T) {
	long := strings.Repeat("m", maxAPIErrorBytes+40)
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "empty body keeps the status only", status: 500, want: "HTTP 500"},
		{name: "no status and no body", want: "request failed"},
		{
			name:   "nested error message",
			status: 400,
			body:   `{"error":{"message":"bad model","type":"invalid_request_error"}}`,
			want:   "HTTP 400: bad model",
		},
		{
			name:   "flat error string",
			status: 401,
			body:   `{"error":"invalid api key"}`,
			want:   "HTTP 401: invalid api key",
		},
		{
			name:   "top-level message",
			status: 429,
			body:   `{"message":"slow down"}`,
			want:   "HTTP 429: slow down",
		},
		{
			name:   "html is stripped and whitespace flattened",
			status: 502,
			body:   "<html><body><h1>Bad   Gateway</h1><script>ignore()</script></body></html>",
			want:   "HTTP 502: Bad Gateway",
		},
		{
			name:   "sse error event wins over the fallback event",
			status: 200,
			body:   "event: message\ndata: {\"choices\":[]}\n\nevent: error\ndata: {\"error\":{\"message\":\"stream broke\"}}\n\n",
			want:   "HTTP 200: stream broke",
		},
		{
			name:   "long messages are cut at the byte cap",
			status: 400,
			body:   `{"message":"` + long + `"}`,
			want:   "HTTP 400: " + long[:maxAPIErrorBytes] + "...",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatAPIError(test.status, test.body); got != test.want {
				t.Fatalf("FormatAPIError(%d, %q) = %q, want %q", test.status, test.body, got, test.want)
			}
		})
	}
}

func TestFormatAPIErrorCutsOnARuneBoundary(t *testing.T) {
	// U+20AC is three bytes; a cap landing inside it must not emit a partial rune.
	body := `{"message":"` + strings.Repeat("a", maxAPIErrorBytes-1) + "€" + `"}`
	got := FormatAPIError(500, body)
	if !strings.HasSuffix(got, "...") || !strings.HasSuffix(strings.TrimSuffix(got, "..."), strings.Repeat("a", maxAPIErrorBytes-1)) {
		t.Fatalf("FormatAPIError cut inside a rune: %q", got)
	}
}

func TestFormatModelListErrorNamesTheCause(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		hasKey  bool
		wantSub string
	}{
		{name: "rejected key", status: 401, hasKey: true, wantSub: "rejected the API key (HTTP 401)"},
		{name: "missing key", status: 403, wantSub: "requires an API key (HTTP 403)"},
		{name: "empty listing", status: 200, wantSub: "empty or truncated /models response"},
		{name: "other status", status: 500, wantSub: "listing provider models failed (HTTP 500)"},
		{name: "unreachable", status: 0, wantSub: "could not reach provider at https://example.invalid/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := FormatModelListError("", "https://example.invalid/v1", test.hasKey, test.status)
			if !strings.Contains(got, test.wantSub) {
				t.Fatalf("FormatModelListError = %q, want it to contain %q", got, test.wantSub)
			}
		})
	}
	if got := FormatModelListError("", "", false, 0); !strings.Contains(got, "could not reach provider") {
		t.Fatalf("unknown-status error = %q, want the reachability form", got)
	}
}

func TestParseRetryAfterReadsSecondsAndDates(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "empty", value: "", want: 0},
		{name: "blank", value: "  \t", want: 0},
		{name: "seconds", value: "12", want: 12 * time.Second},
		{name: "seconds with padding", value: " 12\r\n", want: 12 * time.Second},
		{name: "zero seconds", value: "0", want: 0},
		{name: "negative is not a number", value: "-5", want: 0},
		{name: "seconds beyond the cap", value: "3600", want: maxRetryAfter},
		{name: "seconds at the cap", value: "120", want: maxRetryAfter},
		{name: "non-digit overflow", value: "99999999999999999999", want: maxRetryAfter},
		{name: "http date", value: "Mon, 12 Oct 2026 12:00:30 GMT", want: 30 * time.Second},
		{name: "http date beyond the cap", value: "Mon, 12 Oct 2026 13:00:00 GMT", want: maxRetryAfter},
		{name: "http date in the past", value: "Mon, 12 Oct 2026 11:59:00 GMT", want: 0},
		{name: "http date equals now", value: "Mon, 12 Oct 2026 12:00:00 GMT", want: 0},
		{name: "unparsable", value: "soon", want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := parseRetryAfter(test.value, now); got != test.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

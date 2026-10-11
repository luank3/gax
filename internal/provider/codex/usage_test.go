package codex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/transport"
)

func TestParseCLIAuthReadsAccountAndNamespacedEmail(t *testing.T) {
	idToken := testJWT(t, `{"https://api.openai.com/profile":{"email":"user@example.com"}}`)
	auth, err := codex.ParseCLIAuth([]byte(`{"tokens":{"access_token":"access-secret","account_id":"account-123","id_token":"` + idToken + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken != "access-secret" || auth.AccountID != "account-123" || auth.Email != "user@example.com" {
		t.Fatalf("parsed Codex auth = %+v", auth)
	}
}

func TestLoadCLIAuthReadsCodexAuthFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{"access_token":"access","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := codex.LoadCLIAuth(home)
	if err != nil || auth.AccessToken != "access" || auth.AccountID != "account" {
		t.Fatalf("load Codex CLI auth: auth=%+v err=%v", auth, err)
	}
}

func TestLoadCLIAuthPreservesHomePathComponents(t *testing.T) {
	home := t.TempDir() + "/missing/../home"
	_, err := codex.LoadCLIAuth(home)
	if err == nil || !strings.Contains(err.Error(), "("+home+"/.codex/auth.json)") {
		t.Fatalf("LoadCLIAuth() error = %v; want original HOME path components", err)
	}
}

func TestLoadCLIAuthUsesLiteralTildeWhenHomeIsMissing(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := codex.LoadCLIAuth("")
	if err == nil || !strings.Contains(err.Error(), "(~/.codex/auth.json)") {
		t.Fatalf("LoadCLIAuth() error = %v; want literal tilde path", err)
	}
}

func TestLoadAuthPrefersHaxCredentialStoreAndFallsBackWhenItsEntryIsPartial(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"cli-access","account_id":"cli-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	stateHome := t.TempDir()
	storePath := filepath.Join(stateHome, "hax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	idToken := testJWT(t, `{"email":"hax@example.com"}`)
	storeBody := `{"codex":{"access_token":"hax-access","refresh_token":"hax-refresh","id_token":"` + idToken + `","account_id":"hax-account"}}`
	if err := os.WriteFile(storePath, []byte(storeBody), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := codex.LoadAuth(home, stateHome)
	if err != nil || auth.Source != codex.SourceHax || auth.AccessToken != "hax-access" || auth.RefreshToken != "hax-refresh" ||
		auth.AccountID != "hax-account" || auth.Email != "hax@example.com" {
		t.Fatalf("Hax credentials should take precedence: auth=%+v err=%v", auth, err)
	}

	if err := os.WriteFile(storePath, []byte(`{"codex":{"access_token":"partial"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err = codex.LoadAuth(home, stateHome)
	if err != nil || auth.Source != codex.SourceCLI || auth.AccessToken != "cli-access" || auth.AccountID != "cli-account" {
		t.Fatalf("partial Hax entry should fall back to Codex CLI: auth=%+v err=%v", auth, err)
	}
}

func TestLoadAuthUsesConfiguredStateHomeWithoutHome(t *testing.T) {
	_, stateHome := writeHaxAuth(t, "hax-access", "hax-refresh")
	auth, err := codex.LoadAuth("", stateHome)
	if err != nil || auth.Source != codex.SourceHax || auth.AccessToken != "hax-access" {
		t.Fatalf("configured state store should load without HOME: auth=%+v err=%v", auth, err)
	}
}

func TestParseUsageKeepsPlanAndReportsRateLimitWindows(t *testing.T) {
	auth := codex.Auth{Email: "user@example.com"}
	report, err := codex.ParseUsage([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12.6,"reset_at":1800000000,"limit_window_seconds":604800},"secondary_window":{}}}`), auth)
	if err != nil {
		t.Fatal(err)
	}
	if report.Plan != "plus" || report.Email != auth.Email || !report.HasRateLimit || len(report.Windows) != 2 {
		t.Fatalf("Codex usage report mismatch: %+v", report)
	}
	if report.Windows[0].Label != "weekly" || !report.Windows[0].Recognized || report.Windows[0].Percent != 12.6 || report.Windows[0].ResetAt.Unix() != 1800000000 {
		t.Fatalf("primary usage window mismatch: %+v", report.Windows[0])
	}
	if report.Windows[1].Label != "secondary" || report.Windows[1].Recognized {
		t.Fatalf("malformed secondary window should be retained for the fallback row: %+v", report.Windows[1])
	}

	var output strings.Builder
	codex.WriteUsageReport(&output, report, 100)
	for _, want := range []string{"codex · user@example.com · plus", "weekly ", "  13% used · resets ", "  secondary (unrecognized window shape)"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("Codex usage output omits %q: %q", want, output.String())
		}
	}
}

func TestParseUsageReportsPlansWithoutRateLimits(t *testing.T) {
	report, err := codex.ParseUsage([]byte(`{"plan_type":"free","rate_limit":null}`), codex.Auth{})
	if err != nil || report.HasRateLimit || report.Plan != "free" {
		t.Fatalf("no-rate-limit response: report=%+v err=%v", report, err)
	}
	var output strings.Builder
	codex.WriteUsageReport(&output, report, 80)
	if !strings.Contains(output.String(), "no rate-limit windows reported for this plan") {
		t.Fatalf("missing no-rate-limit note: %q", output.String())
	}
}

func TestQueryUsageSendsCodexAccountHeadersToFixedEndpoint(t *testing.T) {
	client := &transport.Client{HTTP: &http.Client{Transport: codexRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" ||
			request.Header.Get("Authorization") != "Bearer access-secret" || request.Header.Get("chatgpt-account-id") != "account-123" ||
			request.Header.Get("Accept") != "application/json" || request.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("Codex usage request mismatch: method=%s url=%q headers=%v", request.Method, request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"plan_type":"plus","rate_limit":null}`)), Request: request,
		}, nil
	})}}
	report, err := codex.QueryUsage(context.Background(), &codex.Auth{AccessToken: "access-secret", AccountID: "account-123"}, []string{"originator: codex_cli_rs"}, client, nil)
	if err != nil || report.Plan != "plus" {
		t.Fatalf("Codex usage query: report=%+v err=%v", report, err)
	}
}

func TestQueryUsageProactivelyRefreshesAndPersistsRotatedHaxTokens(t *testing.T) {
	home, stateHome := writeHaxAuth(t, testJWTWithExpiry(t, time.Now().Add(-time.Minute).Unix()), "old-refresh")
	auth, err := codex.LoadAuth(home, stateHome)
	if err != nil {
		t.Fatal(err)
	}
	newAccess := testJWTWithExpiry(t, time.Now().Add(time.Hour).Unix())
	var posts, gets int
	client := &transport.Client{HTTP: &http.Client{Transport: codexRoundTrip(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			posts++
			if request.URL.String() != "https://auth.openai.com/oauth/token" || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("refresh request mismatch: url=%q headers=%v", request.URL, request.Header)
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" {
				t.Errorf("refresh body mismatch: body=%v err=%v", body, err)
			}
			return jsonResponse(request, http.StatusOK, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh"}`), nil
		case http.MethodGet:
			gets++
			if request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || request.Header.Get("Authorization") != "Bearer "+newAccess {
				t.Errorf("usage should use the rotated token: url=%q auth=%q", request.URL, request.Header.Get("Authorization"))
			}
			return jsonResponse(request, http.StatusOK, `{"plan_type":"plus","rate_limit":null}`), nil
		default:
			t.Errorf("unexpected Codex request method %q", request.Method)
			return jsonResponse(request, http.StatusMethodNotAllowed, ""), nil
		}
	})}}
	report, err := codex.QueryUsage(context.Background(), &auth, nil, client, nil)
	if err != nil || report.Plan != "plus" || posts != 1 || gets != 1 {
		t.Fatalf("proactive Codex refresh: report=%+v auth=%+v posts=%d gets=%d err=%v", report, auth, posts, gets, err)
	}
	if auth.AccessToken != newAccess || auth.RefreshToken != "new-refresh" {
		t.Fatalf("refreshed auth not adopted in memory: %+v", auth)
	}
	updated, err := codex.LoadAuth(home, stateHome)
	if err != nil || updated.AccessToken != newAccess || updated.RefreshToken != "new-refresh" {
		t.Fatalf("rotated tokens not persisted: auth=%+v err=%v", updated, err)
	}
	info, err := os.Stat(filepath.Join(stateHome, "hax", "auth.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential store permissions = %v, err=%v; want 0600", info.Mode().Perm(), err)
	}
}

func TestQueryUsageForceRefreshesAfterOneUnauthorizedResponse(t *testing.T) {
	home, stateHome := writeHaxAuth(t, testJWTWithExpiry(t, time.Now().Add(time.Hour).Unix()), "old-refresh")
	auth, err := codex.LoadAuth(home, stateHome)
	if err != nil {
		t.Fatal(err)
	}
	newAccess := testJWTWithExpiry(t, time.Now().Add(2*time.Hour).Unix())
	var posts, gets int
	client := &transport.Client{HTTP: &http.Client{Transport: codexRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			posts++
			return jsonResponse(request, http.StatusOK, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh"}`), nil
		}
		gets++
		if gets == 1 {
			return jsonResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer "+newAccess {
			t.Errorf("retry auth = %q, want rotated token", request.Header.Get("Authorization"))
		}
		return jsonResponse(request, http.StatusOK, `{"plan_type":"plus","rate_limit":null}`), nil
	})}}
	report, err := codex.QueryUsage(context.Background(), &auth, nil, client, nil)
	if err != nil || report.Plan != "plus" || posts != 1 || gets != 2 {
		t.Fatalf("401 Codex recovery: report=%+v posts=%d gets=%d err=%v", report, posts, gets, err)
	}
}

func TestQueryUsagePreservesRetryServerErrorsAfterRefresh(t *testing.T) {
	home, stateHome := writeHaxAuth(t, testJWTWithExpiry(t, time.Now().Add(time.Hour).Unix()), "old-refresh")
	auth, err := codex.LoadAuth(home, stateHome)
	if err != nil {
		t.Fatal(err)
	}
	newAccess := testJWTWithExpiry(t, time.Now().Add(2*time.Hour).Unix())
	gets := 0
	client := &transport.Client{HTTP: &http.Client{Transport: codexRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return jsonResponse(request, http.StatusOK, `{"access_token":"`+newAccess+`","refresh_token":"new-refresh"}`), nil
		}
		gets++
		if gets == 1 {
			return jsonResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
		}
		return jsonResponse(request, http.StatusInternalServerError, `{"error":"upstream failure"}`), nil
	})}}
	_, err = codex.QueryUsage(context.Background(), &auth, nil, client, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to fetch usage from") || strings.Contains(err.Error(), "login expired") {
		t.Fatalf("post-refresh server failure = %v, want a usage request error", err)
	}
}

func TestQueryUsageRedactsExpiredTokenAndReportsMalformedResponse(t *testing.T) {
	t.Setenv("HOME", "")
	for _, test := range []struct {
		name, body, want string
		status           int
		source           codex.Source
	}{
		{name: "expired token", status: http.StatusUnauthorized, want: "codex CLI token expired"},
		{name: "expired Hax token", status: http.StatusUnauthorized, want: "codex login expired — run /login again", source: codex.SourceHax},
		{name: "invalid JSON", status: http.StatusOK, body: "not-json", want: "usage response is not valid JSON:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &transport.Client{HTTP: &http.Client{Transport: codexRoundTrip(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			})}}
			_, err := codex.QueryUsage(context.Background(), &codex.Auth{AccessToken: "never-print-this-secret", AccountID: "account", Source: test.source}, nil, client, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "never-print-this-secret") {
				t.Fatalf("Codex usage error = %v, want safely redacted %q", err, test.want)
			}
		})
	}
}

func testJWT(t *testing.T, payload string) string {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return "header." + encoded + ".signature"
}

func testJWTWithExpiry(t *testing.T, expiry int64) string {
	t.Helper()
	return testJWT(t, `{"exp":`+strconv.FormatInt(expiry, 10)+`}`)
}

func writeHaxAuth(t *testing.T, accessToken, refreshToken string) (home, stateHome string) {
	t.Helper()
	home, stateHome = t.TempDir(), t.TempDir()
	storePath := filepath.Join(stateHome, "hax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"codex": map[string]string{
		"access_token": accessToken, "refresh_token": refreshToken, "account_id": "account-123",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, body, 0600); err != nil {
		t.Fatal(err)
	}
	return home, stateHome
}

func jsonResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

type codexRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip codexRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

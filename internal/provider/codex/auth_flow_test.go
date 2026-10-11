package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/wiretrace"
)

func TestExchangeAuthorizationCodeRedactsRawAndFormEncodedSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	logger := wiretrace.Default()
	if err := logger.Configure(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		_ = logger.Configure("")
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse exchange form: %v", err)
		}
		if request.Form.Get("code") != "secret/code" || request.Form.Get("code_verifier") != "verify secret" {
			t.Errorf("exchange form changed in transit: %v", request.Form)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"rejected"}`)
	}))
	defer server.Close()
	client := transport.NewClient()
	client.HTTP = server.Client()
	if _, err := exchangeAuthorizationCode(context.Background(), client, server.URL,
		"secret/code", "verify secret", "https://auth.example/callback"); err == nil {
		t.Fatal("failed token exchange unexpectedly succeeded")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, secret := range []string{"secret/code", "secret%2Fcode", "verify secret", "verify%20secret"} {
		if strings.Contains(text, secret) {
			t.Errorf("wire trace leaked OAuth secret %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "code=<redacted>") || !strings.Contains(text, "code_verifier=<redacted>") {
		t.Fatalf("wire trace did not redact OAuth form fields: %s", text)
	}
}

func TestParseDeviceUserCodeDefaultsAndClampsInterval(t *testing.T) {
	for _, test := range []struct {
		body string
		code string
		secs int
	}{
		{body: `{"device_auth_id":"da","user_code":"ABCD","interval":"7"}`, code: "ABCD", secs: 7},
		{body: `{"device_auth_id":"da","user_code":"PREFIX","interval":" 7seconds"}`, code: "PREFIX", secs: 7},
		{body: `{"device_auth_id":"da","usercode":"OLD","interval":600}`, code: "OLD", secs: 60},
		{body: `{"device_auth_id":"da","user_code":"C","interval":0}`, code: "C", secs: 5},
	} {
		device, err := parseDeviceUserCode([]byte(test.body))
		if err != nil || device.DeviceAuthID != "da" || device.UserCode != test.code || device.Interval != time.Duration(test.secs)*time.Second {
			t.Fatalf("parseDeviceUserCode(%s) = %+v, err=%v", test.body, device, err)
		}
	}
	if _, err := parseDeviceUserCode([]byte(`{"user_code":"missing id"}`)); err == nil {
		t.Fatal("incomplete device response succeeded")
	}
}

func TestLoginBrowserValidatesStateAndInstallsCredential(t *testing.T) {
	var exchanged atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
			form.Get("grant_type") != "authorization_code" || form.Get("code") != "browser-code" ||
			form.Get("code_verifier") == "" || !strings.HasPrefix(form.Get("redirect_uri"), "http://localhost:") {
			t.Errorf("browser token exchange = %q %q", r.Header.Get("Content-Type"), body)
		}
		exchanged.Store(true)
		idToken := testJWTWithClaims(t, `{"email":"browser@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"browser-account"}}`)
		accessToken := testJWTWithClaims(t, `{"exp":`+fmt.Sprint(time.Now().Add(time.Hour).Unix())+`}`)
		_, _ = fmt.Fprintf(w, `{"id_token":%q,"access_token":%q,"refresh_token":"browser-refresh"}`, idToken, accessToken)
	}))
	defer server.Close()
	stateHome := t.TempDir()
	var shownURL string
	result, err := LoginBrowser(LoginOptions{
		Context: context.Background(), Home: t.TempDir(), StateHome: stateHome,
		HTTPClient: transport.NewClient(), BrowserPorts: []int{0}, BrowserDeadline: 5 * time.Second,
		Endpoints: LoginEndpoints{Authorize: server.URL + "/authorize", Token: server.URL + "/oauth/token"},
		OpenBrowser: func(value string) {
			shownURL = value
			parsed, parseErr := url.Parse(value)
			if parseErr != nil {
				t.Errorf("parse authorization URL: %v", parseErr)
				return
			}
			query := parsed.Query()
			if query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" ||
				query.Get("originator") != "gax" || query.Get("state") == "" {
				t.Errorf("authorization URL query = %v", query)
			}
			go func() {
				redirect, parseErr := url.Parse(query.Get("redirect_uri"))
				if parseErr != nil {
					t.Errorf("parse callback URL: %v", parseErr)
					return
				}
				client := &http.Client{Timeout: time.Second}
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					callbackURL := *redirect
					callbackURL.RawQuery = url.Values{"code": {"browser-code"}, "state": {"wrong-state"}}.Encode()
					response, requestErr := client.Get(callbackURL.String())
					if requestErr == nil {
						status := response.StatusCode
						_ = response.Body.Close()
						if status == http.StatusBadRequest {
							callbackURL.RawQuery = url.Values{"code": {"browser-code"}, "state": {query.Get("state") + ".onboarding_entrypoint=test"}}.Encode()
							response, requestErr = client.Get(callbackURL.String())
							if requestErr == nil {
								status = response.StatusCode
								_ = response.Body.Close()
								if status == http.StatusOK {
									return
								}
							}
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Errorf("browser callback listener did not accept authorization")
			}()
		},
	})
	if err != nil || result.Email != "browser@example.com" || result.Cancelled || !exchanged.Load() ||
		!strings.Contains(shownURL, "/authorize?") {
		t.Fatalf("LoginBrowser() = %+v, err=%v authorize=%q exchanged=%t", result, err, shownURL, exchanged.Load())
	}
	auth, err := LoadAuth(t.TempDir(), stateHome)
	if err != nil || auth.Source != SourceGax || auth.AccountID != "browser-account" || auth.RefreshToken != "browser-refresh" {
		t.Fatalf("browser managed auth = %+v, err=%v", auth, err)
	}
}

func TestOAuthListenerRejectsEncodedCallbackPath(t *testing.T) {
	listener, port, err := openOAuthListener([]int{0})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan oauthCallbackResult, 1)
	go func() {
		callback, waitErr := listener.wait(ctx, "expected", 3*time.Second)
		result <- oauthCallbackResult{callback: callback, err: waitErr}
	}()

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/auth%%2Fcallback?code=evil&state=expected", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("encoded callback path status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}

	response, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=good&state=expected", port))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid callback status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if readErr != nil || !strings.Contains(string(body), "Login complete") {
		t.Fatalf("valid callback response body = %q, err=%v", body, readErr)
	}
	select {
	case got := <-result:
		if got.err != nil || got.callback.code != "good" || got.callback.denial != "" {
			t.Fatalf("listener result = %+v, want valid code callback", got)
		}
	case <-ctx.Done():
		t.Fatal("listener did not accept the valid callback")
	}
}

func TestClassifyDevicePollResponses(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   devicePollResult
	}{
		{status: 200, body: `{"authorization_code":"ac","code_verifier":"cv"}`, want: devicePollAuthorized},
		{status: 403, body: ``, want: devicePollPending},
		{status: 404, body: `{}`, want: devicePollPending},
		{status: 429, body: `{"error":{"code":"slow_down"}}`, want: devicePollSlowDown},
		{status: 400, body: `{"error":"access_denied"}`, want: devicePollFailed},
		{status: 200, body: `{"authorization_code":"ac"}`, want: devicePollFailed},
	} {
		if got := classifyDevicePoll(test.status, []byte(test.body)); got != test.want {
			t.Fatalf("classifyDevicePoll(%d, %q) = %v, want %v", test.status, test.body, got, test.want)
		}
	}
}

func TestAuthorizationCodeExchangeUsesRFC3986FormEscaping(t *testing.T) {
	body := authorizationCodeForm("a c/d", "verif~123", "https://auth.example/device/callback")
	if !strings.Contains(body, "grant_type=authorization_code&code=a%20c%2Fd&") ||
		!strings.Contains(body, "&code_verifier=verif~123") || !strings.Contains(body, "&client_id="+oauthClientID) {
		t.Fatalf("authorization form = %q", body)
	}
}

func TestPKCEPairMatchesUpstreamVerifierAndStateLengths(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) != 86 {
		t.Fatalf("PKCE verifier length = %d, want 86", len(verifier))
	}
	for _, character := range verifier {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", character) {
			t.Fatalf("PKCE verifier contains non-base64url character %q", character)
		}
	}
	digest := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(digest[:]); challenge != want {
		t.Fatalf("PKCE challenge = %q, want S256 digest %q", challenge, want)
	}
	secondVerifier, secondChallenge, err := pkcePair()
	if err != nil || verifier == secondVerifier || challenge == secondChallenge {
		t.Fatalf("PKCE pair was reused: first=%q/%q second=%q/%q err=%v",
			verifier, challenge, secondVerifier, secondChallenge, err)
	}
	state, err := randomOAuthValue()
	if err != nil || len(state) != 43 {
		t.Fatalf("OAuth state length = %d, err=%v; want 43", len(state), err)
	}
	secondState, err := randomOAuthValue()
	if err != nil || state == secondState {
		t.Fatalf("OAuth state was reused: %q and %q, err=%v", state, secondState, err)
	}
}

func TestJWTExpiryTruncatesFractionalNumericDates(t *testing.T) {
	for _, claims := range []string{
		`{"exp":1755500000.75}`,
		`{"exp":1.75550000075e9}`,
	} {
		token := testJWTWithClaims(t, claims)
		if got := tokenExpiry(token).Unix(); got != 1755500000 {
			t.Errorf("tokenExpiry(%s) = %d, want 1755500000", claims, got)
		}
	}
}

func TestLoginDevicePollsExchangesAndAtomicallyReplacesManagedCredential(t *testing.T) {
	var polls atomic.Int32
	var oldRefreshRevoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/usercode":
			if r.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(body), oauthClientID) {
				t.Errorf("user-code request = %q %s", r.Header.Get("Content-Type"), body)
			}
			_, _ = io.WriteString(w, `{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":1}`)
		case "/device-token":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":"deviceauth_authorization_pending"}`)
				return
			}
			_, _ = io.WriteString(w, `{"authorization_code":"a c/d","code_verifier":"verif~123"}`)
		case "/oauth/token":
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				!strings.Contains(string(body), "code=a%20c%2Fd") || !strings.Contains(string(body), "code_verifier=verif~123") {
				t.Errorf("token exchange body = %q", body)
			}
			idToken := testJWTWithClaims(t, `{"email":"new@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"account-2"}}`)
			accessToken := testJWTWithClaims(t, `{"exp":`+fmt.Sprint(time.Now().Add(time.Hour).Unix())+`}`)
			_, _ = fmt.Fprintf(w, `{"id_token":%q,"access_token":%q,"refresh_token":"new-refresh"}`, idToken, accessToken)
		case "/revoke":
			var request map[string]string
			_ = json.Unmarshal(body, &request)
			if request["token"] == "old-refresh" {
				oldRefreshRevoked.Store(true)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected auth path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	stateHome := t.TempDir()
	storePath := filepath.Join(stateHome, "gax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte(`{"codex":{"access_token":"old-access","refresh_token":"old-refresh","account_id":"account-1"},"other":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var shownCode string
	var busyLabels []string
	var busyEnds int
	result, err := LoginDevice(LoginOptions{
		Context: context.Background(), Home: t.TempDir(), StateHome: stateHome,
		HTTPClient: transport.NewClient(), Endpoints: LoginEndpoints{
			DeviceUserCode: server.URL + "/usercode", DeviceToken: server.URL + "/device-token",
			Token: server.URL + "/oauth/token", Revoke: server.URL + "/revoke", Verify: "https://auth.example/device",
		},
		Wait: func(context.Context, time.Duration) error { return nil },
		Busy: func(label string) (func() bool, func() bool) {
			busyLabels = append(busyLabels, label)
			return func() bool { return false }, func() bool { busyEnds++; return false }
		},
		ShowDeviceCode: func(code, _ string) { shownCode = code },
	})
	if err != nil || result.Email != "new@example.com" || result.Cancelled || shownCode != "ABCD-EFGH" ||
		polls.Load() != 2 || !oldRefreshRevoked.Load() {
		t.Fatalf("LoginDevice() = %+v, err=%v code=%q polls=%d revoked=%t", result, err, shownCode, polls.Load(), oldRefreshRevoked.Load())
	}
	if want := []string{"contacting auth.openai.com...", "waiting for browser approval...", "completing login...", "revoking previous login..."}; !reflect.DeepEqual(busyLabels, want) || busyEnds != len(want) {
		t.Fatalf("device login busy scopes = %v with %d ends, want %v", busyLabels, busyEnds, want)
	}
	managed, err := LoadAuth(t.TempDir(), stateHome)
	if err != nil || managed.Source != SourceGax || managed.AccountID != "account-2" || managed.RefreshToken != "new-refresh" {
		t.Fatalf("new managed auth = %+v, err=%v", managed, err)
	}
	updated, err := os.ReadFile(storePath)
	if err != nil || !strings.Contains(string(updated), `"keep": true`) {
		t.Fatalf("login lost unrelated auth data: %s, err=%v", updated, err)
	}
}

func TestInstallManagedLoginReplacesInvalidCredentialStore(t *testing.T) {
	for _, existing := range []string{"{broken", `[]`} {
		t.Run(existing, func(t *testing.T) {
			stateHome := t.TempDir()
			storePath := filepath.Join(stateHome, "gax", "auth.json")
			if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(storePath, []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}

			entry := map[string]json.RawMessage{
				"access_token":  json.RawMessage(`"new-access"`),
				"refresh_token": json.RawMessage(`"new-refresh"`),
			}
			if oldRefresh, err := installManagedLogin(t.TempDir(), stateHome, entry); err != nil || oldRefresh != "" {
				t.Fatalf("installManagedLogin() = old refresh %q, err=%v", oldRefresh, err)
			}
			body, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatalf("credential store is invalid JSON after login: %s (%v)", body, err)
			}
			if len(root) != 1 || jsonString(jsonObject(root["codex"])["refresh_token"]) != "new-refresh" {
				t.Fatalf("credential store after login = %s", body)
			}
		})
	}
}

func testJWTWithClaims(t *testing.T, claims string) string {
	t.Helper()
	return "aGVhZGVy." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
}

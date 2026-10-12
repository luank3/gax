package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/system/browser"
	systemfs "github.com/luank3/gax/internal/system/fs"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/wiretrace"
)

const (
	authorizeEndpoint      = "https://auth.openai.com/oauth/authorize"
	tokenEndpoint          = "https://auth.openai.com/oauth/token"
	deviceUserCodeEndpoint = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	deviceToken            = "https://auth.openai.com/api/accounts/deviceauth/token"
	deviceVerify           = "https://auth.openai.com/codex/device"
	deviceRedirect         = "https://auth.openai.com/deviceauth/callback"
	loginHTTPTimeout       = 30 * time.Second
	deviceDeadline         = 15 * time.Minute
	browserDeadline        = 10 * time.Minute
	browserPath            = "/auth/callback"
)

var browserPorts = []int{1455, 1457}

var ErrLoginCancelled = errors.New("login cancelled")

type LoginEndpoints struct {
	Authorize      string
	Token          string
	DeviceUserCode string
	DeviceToken    string
	Verify         string
	DeviceRedirect string
	Revoke         string
}

type LoginOptions struct {
	Context         context.Context
	Home            string
	StateHome       string
	HTTPClient      *transport.Client
	Endpoints       LoginEndpoints
	DeviceDeadline  time.Duration
	BrowserDeadline time.Duration
	BrowserPorts    []int
	Wait            func(context.Context, time.Duration) error
	Busy            func(label string) (tick func() bool, end func() bool)
	ShowDeviceCode  func(userCode, verificationURL string)
	ShowBrowserLink func(authorizeURL string)
	OpenBrowser     func(authorizeURL string)
}

func (options LoginOptions) beginBusy(label string) (func() bool, func() bool) {
	if options.Busy == nil {
		return nil, func() bool { return false }
	}
	tick, end := options.Busy(label)
	if end == nil {
		end = func() bool { return false }
	}
	return tick, end
}

type LoginResult struct {
	Email     string
	Cancelled bool
}

type deviceUserCode struct {
	DeviceAuthID string
	UserCode     string
	Interval     time.Duration
}

type devicePollResult uint8

const (
	devicePollAuthorized devicePollResult = iota
	devicePollPending
	devicePollSlowDown
	devicePollFailed
)

func (options LoginOptions) endpoints() LoginEndpoints {
	if options.Endpoints.Authorize == "" {
		options.Endpoints.Authorize = authorizeEndpoint
	}
	if options.Endpoints.Token == "" {
		options.Endpoints.Token = tokenEndpoint
	}
	if options.Endpoints.DeviceUserCode == "" {
		options.Endpoints.DeviceUserCode = deviceUserCodeEndpoint
	}
	if options.Endpoints.DeviceToken == "" {
		options.Endpoints.DeviceToken = deviceToken
	}
	if options.Endpoints.Verify == "" {
		options.Endpoints.Verify = deviceVerify
	}
	if options.Endpoints.DeviceRedirect == "" {
		options.Endpoints.DeviceRedirect = deviceRedirect
	}
	if options.Endpoints.Revoke == "" {
		options.Endpoints.Revoke = revokeEndpoint
	}
	return options.Endpoints
}

// LoginBrowser runs the loopback OAuth authorization-code flow and installs the resulting
// rotating Codex token in gax's credential store.
func LoginBrowser(options LoginOptions) (LoginResult, error) {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	client := options.HTTPClient
	if client == nil {
		client = transport.DefaultClient
	}
	endpoints := options.endpoints()
	ports := options.BrowserPorts
	if len(ports) == 0 {
		ports = browserPorts
	}
	listener, port, err := openOAuthListener(ports)
	if err != nil {
		return LoginResult{}, errors.New("cannot listen on localhost port 1455 or 1457 (already in use?) — close the conflicting program or use device login")
	}
	defer listener.close()

	verifier, challenge, err := pkcePair()
	if err != nil {
		return LoginResult{}, fmt.Errorf("could not prepare browser login: %w", err)
	}
	state, err := randomOAuthValue()
	if err != nil {
		return LoginResult{}, fmt.Errorf("could not prepare browser login: %w", err)
	}
	redirect := fmt.Sprintf("http://localhost:%d%s", port, browserPath)
	loginURL := authorizeURL(endpoints.Authorize, challenge, state, redirect)
	if options.OpenBrowser != nil {
		options.OpenBrowser(loginURL)
	} else {
		openLoginURL(loginURL)
	}
	if options.ShowBrowserLink != nil {
		options.ShowBrowserLink(loginURL)
	}

	deadline := options.BrowserDeadline
	if deadline <= 0 {
		deadline = browserDeadline
	}
	tick, endBusy := options.beginBusy("waiting for browser approval...")
	callback, err := listener.waitWithTick(ctx, state, deadline, tick)
	interrupted := endBusy()
	if interrupted {
		return LoginResult{Cancelled: true}, ErrLoginCancelled
	}
	if err != nil {
		return LoginResult{Cancelled: errors.Is(err, ErrLoginCancelled)}, err
	}
	if callback.denial != "" {
		return LoginResult{}, fmt.Errorf("login was rejected: %s", callback.denial)
	}
	if callback.code == "" {
		return LoginResult{}, errors.New("the localhost login listener failed")
	}
	tick, endBusy = options.beginBusy("completing login...")
	entry, err := exchangeAuthorizationCodeWithTick(ctx, client, endpoints.Token, callback.code, verifier, redirect, tick)
	interrupted = endBusy()
	if err != nil {
		if interrupted {
			return LoginResult{Cancelled: true}, ErrLoginCancelled
		}
		return LoginResult{Cancelled: errors.Is(err, ErrLoginCancelled)}, err
	}
	oldRefresh, err := installManagedLogin(options.Home, options.StateHome, entry)
	if err != nil {
		_ = revokeRefreshTokenWithBusy(ctx, client, endpoints.Revoke, jsonString(entry["refresh_token"]),
			"revoking unsaved login...", options)
		return LoginResult{}, err
	}
	if oldRefresh != "" && oldRefresh != jsonString(entry["refresh_token"]) {
		_ = revokeRefreshTokenWithBusy(ctx, client, endpoints.Revoke, oldRefresh, "revoking previous login...", options)
	}
	return LoginResult{Email: jwtEmail(jsonString(entry["id_token"]))}, nil
}

type oauthCallback struct {
	code   string
	denial string
}

type oauthCallbackResult struct {
	callback oauthCallback
	err      error
}

type oauthListener struct {
	port      int
	listeners []net.Listener
}

func openOAuthListener(ports []int) (*oauthListener, int, error) {
	for requireIPv6 := true; ; requireIPv6 = false {
		for _, candidate := range ports {
			listener4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate)))
			if err != nil {
				continue
			}
			port := listener4.Addr().(*net.TCPAddr).Port
			listener6, err6 := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)))
			if err6 != nil && requireIPv6 && errors.Is(err6, syscall.EADDRINUSE) {
				_ = listener4.Close()
				continue
			}
			listeners := []net.Listener{listener4}
			if err6 == nil {
				listeners = append(listeners, listener6)
			}
			return &oauthListener{port: port, listeners: listeners}, port, nil
		}
		if !requireIPv6 {
			break
		}
	}
	return nil, 0, errors.New("no OAuth callback port available")
}

func (listener *oauthListener) close() {
	if listener == nil {
		return
	}
	for _, item := range listener.listeners {
		_ = item.Close()
	}
}

func (listener *oauthListener) wait(ctx context.Context, expectedState string, timeout time.Duration) (oauthCallback, error) {
	return listener.waitWithTick(ctx, expectedState, timeout, nil)
}

func (listener *oauthListener) waitWithTick(ctx context.Context, expectedState string, timeout time.Duration,
	tick func() bool) (oauthCallback, error) {
	results := make(chan oauthCallbackResult, 1)
	var once sync.Once
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, MaxHeaderBytes: 8192}
	server.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writePage := func(status int, title, message string) {
			page := fmt.Sprintf("<!doctype html><html><head><meta charset=\"utf-8\"><title>gax — %s</title></head><body style=\"font-family:sans-serif;margin:4em auto;max-width:36em\"><h1>%s</h1><p>%s</p></body></html>", html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Content-Length", strconv.Itoa(len(page)))
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Connection", "close")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(page))
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if request.URL.EscapedPath() != browserPath {
			writePage(http.StatusNotFound, "Not found", "This is gax's login callback listener.")
			return
		}
		if !oauthStateMatches(expectedState, request.URL.Query().Get("state")) {
			writePage(http.StatusBadRequest, "Login mismatch", "This redirect does not belong to the login gax is waiting for — restart the login in gax.")
			return
		}
		result := oauthCallbackResult{}
		if errorCode := request.URL.Query().Get("error"); errorCode != "" {
			result.callback.denial = errorCode
			if description := request.URL.Query().Get("error_description"); description != "" {
				result.callback.denial += ": " + description
			}
			writePage(http.StatusOK, "Login failed", "Return to gax for details. This tab can be closed.")
		} else if code := request.URL.Query().Get("code"); code != "" {
			result.callback.code = code
			writePage(http.StatusOK, "Login complete", "You are signed in — return to gax. This tab can be closed.")
		} else {
			writePage(http.StatusBadRequest, "Bad request", "The redirect carried neither a code nor an error.")
			return
		}
		once.Do(func() { results <- result })
	})
	for _, item := range listener.listeners {
		go func(item net.Listener) {
			if err := server.Serve(item); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case results <- oauthCallbackResult{err: err}:
				default:
				}
			}
		}(item)
	}
	defer func() { _ = server.Close() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var tickChannel <-chan time.Time
	if tick != nil {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		tickChannel = ticker.C
	}
	for {
		select {
		case result := <-results:
			return result.callback, result.err
		case <-ctx.Done():
			return oauthCallback{}, ErrLoginCancelled
		case <-timer.C:
			return oauthCallback{}, errors.New("login timed out — no browser approval arrived")
		case <-tickChannel:
			if tick() {
				return oauthCallback{}, ErrLoginCancelled
			}
		}
	}
}

func oauthStateMatches(expected, actual string) bool {
	return strings.HasPrefix(actual, expected) && (len(actual) == len(expected) || actual[len(expected)] == '.')
}

func openLoginURL(value string) {
	browser.OpenURL(value)
}

// LoginDevice performs OpenAI's device-code flow and installs the resulting rotating Codex token
// in gax's credential store.
func LoginDevice(options LoginOptions) (LoginResult, error) {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	client := options.HTTPClient
	if client == nil {
		client = transport.DefaultClient
	}
	endpoints := options.endpoints()
	tick, endBusy := options.beginBusy("contacting auth.openai.com...")
	device, err := requestDeviceUserCode(ctx, client, endpoints.DeviceUserCode, tick)
	interrupted := endBusy()
	if interrupted {
		return LoginResult{Cancelled: true}, ErrLoginCancelled
	}
	if err != nil {
		return LoginResult{Cancelled: errors.Is(err, ErrLoginCancelled)}, err
	}
	if options.ShowDeviceCode != nil {
		options.ShowDeviceCode(device.UserCode, endpoints.Verify)
	}

	deadline := options.DeviceDeadline
	if deadline <= 0 {
		deadline = deviceDeadline
	}
	wait := options.Wait
	if wait == nil {
		wait = waitForContext
	}
	tick, endBusy = options.beginBusy("waiting for browser approval...")
	code, verifier, err := pollDeviceAuthorizationWithTick(ctx, client, endpoints.DeviceToken, device, deadline, wait, tick)
	interrupted = endBusy()
	if interrupted {
		return LoginResult{Cancelled: true}, ErrLoginCancelled
	}
	if err != nil {
		return LoginResult{Cancelled: errors.Is(err, ErrLoginCancelled)}, err
	}
	tick, endBusy = options.beginBusy("completing login...")
	entry, err := exchangeAuthorizationCodeWithTick(ctx, client, endpoints.Token, code, verifier, endpoints.DeviceRedirect, tick)
	interrupted = endBusy()
	if err != nil {
		if interrupted {
			return LoginResult{Cancelled: true}, ErrLoginCancelled
		}
		return LoginResult{Cancelled: errors.Is(err, ErrLoginCancelled)}, err
	}
	email := jwtEmail(jsonString(entry["id_token"]))
	oldRefresh, err := installManagedLogin(options.Home, options.StateHome, entry)
	if err != nil {
		_ = revokeRefreshTokenWithBusy(ctx, client, endpoints.Revoke, jsonString(entry["refresh_token"]),
			"revoking unsaved login...", options)
		return LoginResult{}, err
	}
	if oldRefresh != "" && oldRefresh != jsonString(entry["refresh_token"]) {
		_ = revokeRefreshTokenWithBusy(ctx, client, endpoints.Revoke, oldRefresh, "revoking previous login...", options)
	}
	return LoginResult{Email: email}, nil
}

func requestDeviceUserCode(ctx context.Context, client *transport.Client, endpoint string, tick func() bool) (deviceUserCode, error) {
	requestBody, _ := json.Marshal(map[string]string{"client_id": oauthClientID})
	body, status, err := client.Post(ctx, endpoint, nil, "application/json", requestBody, loginHTTPTimeout, maxBodySize, tick)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return deviceUserCode{}, ErrLoginCancelled
		}
		return deviceUserCode{}, errors.New("cannot reach auth.openai.com — check your network")
	}
	if status < 200 || status >= 300 {
		if status == 404 {
			return deviceUserCode{}, errors.New("device login is not enabled for this endpoint — log in with the codex CLI and gax will pick up its credentials")
		}
		return deviceUserCode{}, fmt.Errorf("device login request failed: %s", transport.FormatAPIError(status, string(body)))
	}
	device, err := parseDeviceUserCode(body)
	if err != nil {
		return deviceUserCode{}, errors.New("unrecognized device login response from auth.openai.com")
	}
	return device, nil
}

func parseDeviceUserCode(body []byte) (deviceUserCode, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return deviceUserCode{}, errors.New("invalid device authorization response")
	}
	deviceID := jsonString(root["device_auth_id"])
	userCode := jsonString(root["user_code"])
	if userCode == "" {
		userCode = jsonString(root["usercode"])
	}
	if deviceID == "" || userCode == "" {
		return deviceUserCode{}, errors.New("incomplete device authorization response")
	}
	interval := int64(0)
	if value := jsonString(root["interval"]); value != "" {
		interval = parseDecimalPrefix(value)
	} else if parsed := jsonInteger(root["interval"]); parsed != nil {
		interval = *parsed
	}
	if interval < 1 {
		interval = 5
	}
	if interval > 60 {
		interval = 60
	}
	return deviceUserCode{DeviceAuthID: deviceID, UserCode: userCode, Interval: time.Duration(interval) * time.Second}, nil
}

// parseDecimalPrefix matches strtol's base-10 handling for the interval string: leading
// ASCII whitespace and a sign are accepted, and parsing stops at the first non-digit.
func parseDecimalPrefix(value string) int64 {
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' ||
		value[start] == '\v' || value[start] == '\f' || value[start] == '\r') {
		start++
	}
	end := start
	if end < len(value) && (value[end] == '+' || value[end] == '-') {
		end++
	}
	digits := end
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == digits {
		return 0
	}
	parsed, _ := strconv.ParseInt(value[start:end], 10, 64)
	return parsed
}

func pollDeviceAuthorization(ctx context.Context, client *transport.Client, endpoint string,
	device deviceUserCode, maxWait time.Duration, wait func(context.Context, time.Duration) error) (string, string, error) {
	return pollDeviceAuthorizationWithTick(ctx, client, endpoint, device, maxWait, wait, nil)
}

func pollDeviceAuthorizationWithTick(ctx context.Context, client *transport.Client, endpoint string,
	device deviceUserCode, maxWait time.Duration, wait func(context.Context, time.Duration) error,
	tick func() bool) (string, string, error) {
	wiretrace.Default().RegisterSecret(device.DeviceAuthID)
	wiretrace.Default().RegisterSecret(device.UserCode)
	body, _ := json.Marshal(map[string]string{"device_auth_id": device.DeviceAuthID, "user_code": device.UserCode})
	deadline := time.Now().Add(maxWait)
	interval := device.Interval
	for {
		var waitErr error
		if tick == nil {
			waitErr = wait(ctx, interval)
		} else {
			waitErr = waitWithTick(ctx, interval, wait, tick)
		}
		if waitErr != nil {
			return "", "", ErrLoginCancelled
		}
		if !time.Now().Before(deadline) {
			return "", "", errors.New("login timed out — the code expired before it was approved")
		}
		response, status, err := client.Post(ctx, endpoint, nil, "application/json", body, loginHTTPTimeout, maxBodySize, tick)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", "", ErrLoginCancelled
			}
			return "", "", errors.New("cannot reach auth.openai.com — check your network")
		}
		switch classifyDevicePoll(status, response) {
		case devicePollAuthorized:
			var root map[string]json.RawMessage
			_ = json.Unmarshal(response, &root)
			return jsonString(root["authorization_code"]), jsonString(root["code_verifier"]), nil
		case devicePollPending:
			continue
		case devicePollSlowDown:
			interval += 5 * time.Second
		case devicePollFailed:
			return "", "", fmt.Errorf("login was rejected: %s", transport.FormatAPIError(status, string(response)))
		}
	}
}

func classifyDevicePoll(status int, body []byte) devicePollResult {
	var root map[string]json.RawMessage
	_ = json.Unmarshal(body, &root)
	if status >= 200 && status < 300 {
		if jsonString(root["authorization_code"]) != "" && jsonString(root["code_verifier"]) != "" {
			return devicePollAuthorized
		}
		return devicePollFailed
	}
	code := oauthErrorCode(root)
	if code == "slow_down" {
		return devicePollSlowDown
	}
	if status == http.StatusForbidden || status == http.StatusNotFound || strings.Contains(code, "authorization_pending") {
		return devicePollPending
	}
	return devicePollFailed
}

func oauthErrorCode(root map[string]json.RawMessage) string {
	errorObject := jsonObject(root["error"])
	if code := jsonString(errorObject["code"]); code != "" {
		return code
	}
	if code := jsonString(root["error"]); code != "" {
		return code
	}
	return jsonString(root["code"])
}

func exchangeAuthorizationCode(ctx context.Context, client *transport.Client, endpoint, code, verifier, redirect string) (map[string]json.RawMessage, error) {
	return exchangeAuthorizationCodeWithTick(ctx, client, endpoint, code, verifier, redirect, nil)
}

func exchangeAuthorizationCodeWithTick(ctx context.Context, client *transport.Client, endpoint, code, verifier, redirect string,
	tick func() bool) (map[string]json.RawMessage, error) {
	wiretrace.Default().RegisterSecret(code)
	wiretrace.Default().RegisterSecret(formEscape(code))
	wiretrace.Default().RegisterSecret(verifier)
	wiretrace.Default().RegisterSecret(formEscape(verifier))
	body := authorizationCodeForm(code, verifier, redirect)
	response, status, err := client.Post(ctx, endpoint, nil, "application/x-www-form-urlencoded", []byte(body), loginHTTPTimeout, maxBodySize, tick)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrLoginCancelled
		}
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("token exchange failed: %s", transport.FormatAPIError(status, string(response)))
	}
	entry, err := credentialFromExchange(response)
	if err != nil {
		return nil, errors.New("token exchange returned an unusable response")
	}
	return entry, nil
}

func authorizationCodeForm(code, verifier, redirect string) string {
	return "grant_type=authorization_code&code=" + formEscape(code) + "&redirect_uri=" + formEscape(redirect) +
		"&client_id=" + formEscape(oauthClientID) + "&code_verifier=" + formEscape(verifier)
}

func formEscape(value string) string {
	const hex = "0123456789ABCDEF"
	var output strings.Builder
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-._~", rune(character)) {
			output.WriteByte(character)
		} else {
			output.WriteByte('%')
			output.WriteByte(hex[character>>4])
			output.WriteByte(hex[character&15])
		}
	}
	return output.String()
}

func credentialFromExchange(body []byte) (map[string]json.RawMessage, error) {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil || response == nil {
		return nil, errors.New("invalid token response")
	}
	idToken := jsonString(response["id_token"])
	accessToken := jsonString(response["access_token"])
	refreshToken := jsonString(response["refresh_token"])
	if idToken == "" || accessToken == "" || refreshToken == "" {
		return nil, errors.New("incomplete token response")
	}
	accountID := jwtAccountID(idToken)
	if accountID == "" {
		accountID = jwtAccountID(accessToken)
	}
	if accountID == "" {
		return nil, errors.New("token response has no ChatGPT account id")
	}
	entry, _ := json.Marshal(map[string]string{
		"access_token": accessToken, "refresh_token": refreshToken,
		"id_token": idToken, "account_id": accountID,
	})
	var decoded map[string]json.RawMessage
	_ = json.Unmarshal(entry, &decoded)
	return decoded, nil
}

func jwtAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 3 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	claims := jsonObject(decoded)
	auth := jsonObject(claims["https://api.openai.com/auth"])
	return jsonString(auth["chatgpt_account_id"])
}

func installManagedLogin(home, stateHome string, entry map[string]json.RawMessage) (string, error) {
	wiretrace.Default().RegisterSecret(jsonString(entry["refresh_token"]))
	path, err := authStorePath(home, stateHome)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	unlock, err := lockAuthStore(path)
	if err != nil {
		return "", err
	}
	defer unlock()
	root := make(map[string]json.RawMessage)
	if body, readErr := systemfs.ReadFile(path); readErr == nil {
		if existing := jsonObject(body); existing != nil {
			root = existing
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	oldRefresh := jsonString(jsonObject(root["codex"])["refresh_token"])
	encoded, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	root["codex"] = encoded
	if err := writeAuthStore(path, root); err != nil {
		return "", err
	}
	return oldRefresh, nil
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitWithTick(ctx context.Context, duration time.Duration, wait func(context.Context, time.Duration) error,
	tick func() bool) error {
	if tick == nil {
		return wait(ctx, duration)
	}
	if tick() {
		return ErrLoginCancelled
	}
	waitContext, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- wait(waitContext, duration) }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ErrLoginCancelled
		case <-ticker.C:
			if tick() {
				return ErrLoginCancelled
			}
		}
	}
}

func revokeRefreshTokenWithBusy(ctx context.Context, client *transport.Client, endpoint, refreshToken, label string,
	options LoginOptions) bool {
	wiretrace.Default().RegisterSecret(refreshToken)
	tick, endBusy := options.beginBusy(label)
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = transport.DefaultClient
	}
	body, err := json.Marshal(map[string]string{
		"token": refreshToken, "token_type_hint": "refresh_token", "client_id": oauthClientID,
	})
	if err != nil {
		endBusy()
		return false
	}
	_, status, err := client.Post(ctx, endpoint, nil, "application/json", body, revokeTimeout, maxBodySize, tick)
	endBusy()
	return err == nil && status >= 200 && status < 300
}

func randomOAuthValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func pkcePair() (verifier, challenge string, err error) {
	var raw [64]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func authorizeURL(endpoint, challenge, state, redirect string) string {
	query := []string{
		"response_type=code", "client_id=" + formEscape(oauthClientID),
		"redirect_uri=" + formEscape(redirect), "scope=" + formEscape("openid profile email offline_access"),
		"code_challenge=" + formEscape(challenge), "code_challenge_method=S256",
		"state=" + formEscape(state), "id_token_add_organizations=true",
		"codex_cli_simplified_flow=true", "originator=gax",
	}
	return endpoint + "?" + strings.Join(query, "&")
}

func defaultLoginEndpoints() LoginEndpoints {
	return LoginEndpoints{
		Authorize: authorizeEndpoint, Token: tokenEndpoint, DeviceUserCode: deviceUserCodeEndpoint,
		DeviceToken: deviceToken, Verify: deviceVerify, DeviceRedirect: deviceRedirect,
		Revoke: revokeEndpoint,
	}
}

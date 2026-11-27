// Package codex manages ChatGPT/Codex OAuth credentials, token refresh,
// ~/.codex fallback, and account usage queries.
package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
)

const (
	defaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultAuthBaseURL = "https://auth.openai.com"
	defaultCodexAPI    = "https://chatgpt.com/backend-api/codex"
)

// Credentials holds stored OAuth tokens for a provider.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

// LoadCredentials returns gax-managed auth.json credentials for codex, or falls back
// read-only to ~/.codex/auth.json.
func LoadCredentials(stateDir, homeDir string) (Credentials, bool, error) {
	if stateDir != "" {
		p := filepath.Join(stateDir, "auth.json")
		if b, err := os.ReadFile(p); err == nil {
			var root map[string]Credentials
			if json.Unmarshal(b, &root) == nil {
				if c, ok := root["codex"]; ok && c.AccessToken != "" {
					if c.ExpiresAt > 0 && c.RefreshToken != "" && time.Now().Unix() >= c.ExpiresAt-60 {
						if refreshed, rerr := refreshTokens(c.RefreshToken); rerr == nil {
							if refreshed.AccountID == "" {
								refreshed.AccountID = c.AccountID
							}
							_ = SaveCredentials(stateDir, refreshed)
							return refreshed, true, nil
						}
					}
					if c.AccountID == "" {
						c.AccountID = extractAccountIDFromJWT(c.AccessToken)
					}
					return c, true, nil
				}
			}
		}
	}
	if homeDir != "" {
		p := filepath.Join(homeDir, ".codex", "auth.json")
		if b, err := os.ReadFile(p); err == nil {
			var raw struct {
				Tokens struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					AccountID    string `json:"account_id"`
				} `json:"tokens"`
			}
			if json.Unmarshal(b, &raw) == nil && raw.Tokens.AccessToken != "" {
				return Credentials{
					AccessToken:  raw.Tokens.AccessToken,
					RefreshToken: raw.Tokens.RefreshToken,
					AccountID:    raw.Tokens.AccountID,
				}, false, nil
			}
		}
	}
	return Credentials{}, false, fmt.Errorf("codex credentials not found (run /login)")
}

// SaveCredentials writes codex tokens to stateDir/auth.json under a file lock,
// preserving symlinks by writing through the resolved target.
func SaveCredentials(stateDir string, creds Credentials) error {
	if stateDir == "" {
		return fmt.Errorf("state directory unavailable")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	authPath := filepath.Join(stateDir, "auth.json")
	if target, err := filepath.EvalSymlinks(authPath); err == nil && target != "" {
		authPath = target
	}
	lock, err := platform.LockFile(authPath + ".lock")
	if err == nil {
		defer lock.Unlock()
	}

	root := make(map[string]Credentials)
	if b, err := os.ReadFile(authPath); err == nil {
		_ = json.Unmarshal(b, &root)
	}
	root["codex"] = creds
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(authPath, append(data, '\n'), 0o600)
}

// RemoveCredentials deletes gax-managed codex tokens from stateDir/auth.json.
func RemoveCredentials(stateDir string) error {
	if stateDir == "" {
		return nil
	}
	authPath := filepath.Join(stateDir, "auth.json")
	if target, err := filepath.EvalSymlinks(authPath); err == nil && target != "" {
		authPath = target
	}
	lock, err := platform.LockFile(authPath + ".lock")
	if err == nil {
		defer lock.Unlock()
	}
	b, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	root := make(map[string]Credentials)
	if err := json.Unmarshal(b, &root); err != nil {
		return err
	}
	delete(root, "codex")
	data, _ := json.MarshalIndent(root, "", "  ")
	return os.WriteFile(authPath, append(data, '\n'), 0o600)
}

// ReadCodexDefaults reads default model and reasoning effort from ~/.codex/config.toml.
func ReadCodexDefaults(homeDir string) (model, effort string) {
	if homeDir == "" {
		return "", ""
	}
	b, err := os.ReadFile(filepath.Join(homeDir, ".codex", "config.toml"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "model":
			model = v
		case "model_reasoning_effort":
			effort = v
		}
	}
	return model, effort
}

// BrowserLogin runs the localhost PKCE OAuth flow and saves credentials to stateDir/auth.json.
func BrowserLogin(ctx context.Context, stateDir string, out io.Writer) error {
	var verBytes [32]byte
	_, _ = rand.Read(verBytes[:])
	verifier := base64.RawURLEncoding.EncodeToString(verBytes[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var stateBytes [16]byte
	_, _ = rand.Read(stateBytes[:])
	state := base64.RawURLEncoding.EncodeToString(stateBytes[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://%s/auth/callback", ln.Addr().String())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("missing authorization code")
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "<html><body>Login complete. You may close this tab.</body></html>")
		codeCh <- code
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown(context.Background())

	authURL := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&id_token_add_organizations=true&codex_cli_simplified_flow=true&originator=codex_cli_rs&state=%s",
		defaultAuthBaseURL, url.QueryEscape(defaultClientID), url.QueryEscape(redirectURI),
		url.QueryEscape("openid profile email offline_access"), url.QueryEscape(challenge), url.QueryEscape(state))

	fmt.Fprintf(out, "Opening browser for Codex login...\nIf it does not open, visit:\n%s\n", authURL)
	_ = platform.OpenURL(ctx, authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("login timed out")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {defaultClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(defaultAuthBaseURL+"/oauth/token", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token exchange HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return err
	}
	accID := extractAccountIDFromJWT(tok.IDToken)
	if accID == "" {
		accID = extractAccountIDFromJWT(tok.AccessToken)
	}
	return SaveCredentials(stateDir, Credentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		AccountID:    accID,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
	})
}

// RefreshCredentials forces a refresh of gax-managed codex OAuth tokens in stateDir.
func RefreshCredentials(stateDir string) (Credentials, error) {
	if stateDir == "" {
		return Credentials{}, fmt.Errorf("state directory unavailable")
	}
	b, err := os.ReadFile(filepath.Join(stateDir, "auth.json"))
	if err != nil {
		return Credentials{}, err
	}
	var root map[string]Credentials
	if err := json.Unmarshal(b, &root); err != nil {
		return Credentials{}, err
	}
	c, ok := root["codex"]
	if !ok || c.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("no refresh token for codex")
	}
	refreshed, err := refreshTokens(c.RefreshToken)
	if err != nil {
		return Credentials{}, err
	}
	if refreshed.AccountID == "" {
		refreshed.AccountID = c.AccountID
	}
	_ = SaveCredentials(stateDir, refreshed)
	return refreshed, nil
}

func extractAccountIDFromJWT(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) == nil {
		return claims.Auth.ChatGPTAccountID
	}
	return ""
}

func refreshTokens(refreshToken string) (Credentials, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {defaultClientID},
		"refresh_token": {refreshToken},
	}
	resp, err := http.PostForm(defaultAuthBaseURL+"/oauth/token", form)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credentials{}, fmt.Errorf("refresh HTTP %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Credentials{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	accID := extractAccountIDFromJWT(tok.IDToken)
	if accID == "" {
		accID = extractAccountIDFromJWT(tok.AccessToken)
	}
	return Credentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		AccountID:    accID,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
	}, nil
}

// DeviceLogin runs the headless OAuth device-code flow for SSH sessions.
func DeviceLogin(ctx context.Context, stateDir string, out io.Writer) error {
	form := url.Values{
		"client_id": {defaultClientID},
		"scope":     {"openid profile email offline_access"},
	}
	resp, err := http.PostForm(defaultAuthBaseURL+"/oauth/device/code", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("device code HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		return err
	}
	fmt.Fprintf(out, "Open %s and enter code: %s\n", dc.VerificationURI, dc.UserCode)

	interval := max(2, dc.Interval)
	deadline := time.Now().Add(time.Duration(max(60, dc.ExpiresIn)) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		pollResp, err := http.PostForm(defaultAuthBaseURL+"/oauth/token", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {defaultClientID},
			"device_code": {dc.DeviceCode},
		})
		if err != nil {
			continue
		}
		pRaw, _ := io.ReadAll(io.LimitReader(pollResp.Body, 64*1024))
		_ = pollResp.Body.Close()
		if pollResp.StatusCode >= 200 && pollResp.StatusCode < 300 {
			var tok struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
				ExpiresIn    int64  `json:"expires_in"`
			}
			if err := json.Unmarshal(pRaw, &tok); err != nil {
				return err
			}
			accID := extractAccountIDFromJWT(tok.IDToken)
			if accID == "" {
				accID = extractAccountIDFromJWT(tok.AccessToken)
			}
			return SaveCredentials(stateDir, Credentials{
				AccessToken:  tok.AccessToken,
				RefreshToken: tok.RefreshToken,
				AccountID:    accID,
				ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
			})
		}
	}
	return fmt.Errorf("device login timed out")
}

// QueryUsage fetches ChatGPT subscription usage windows for Codex.
func QueryUsage(ctx context.Context, accessToken string, accountID ...string) (provider.UsageReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultCodexAPI+"/wham/usage", nil)
	if err != nil {
		return provider.UsageReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("originator", "codex_cli_rs")
	if len(accountID) > 0 && accountID[0] != "" {
		req.Header.Set("chatgpt-account-id", accountID[0])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return provider.UsageReport{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return provider.UsageReport{}, fmt.Errorf("codex usage HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var data struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			PrimaryWindow struct {
				UsedPercent float64 `json:"used_percent"`
			} `json:"primary_window"`
			SecondaryWindow struct {
				UsedPercent float64 `json:"used_percent"`
			} `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return provider.UsageReport{}, err
	}
	return provider.UsageReport{
		Summary: fmt.Sprintf("codex (%s): 5h=%.0f%% weekly=%.0f%%",
			data.PlanType, data.RateLimit.PrimaryWindow.UsedPercent, data.RateLimit.SecondaryWindow.UsedPercent),
	}, nil
}

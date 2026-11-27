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

	authURL := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
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
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return err
	}
	return SaveCredentials(stateDir, Credentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
	})
}

// QueryUsage fetches ChatGPT subscription usage windows for Codex.
func QueryUsage(ctx context.Context, accessToken string) (provider.UsageReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultCodexAPI+"/wham/usage", nil)
	if err != nil {
		return provider.UsageReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
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

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/wiretrace"
)

const (
	revokeEndpoint = "https://auth.openai.com/oauth/revoke"
	revokeTimeout  = 10 * time.Second
)

// HasManagedAuth reports whether gax has a complete Codex credential. Borrowed Codex CLI
// credentials deliberately do not count as a gax-managed login.
func HasManagedAuth(home, stateHome string) (bool, error) {
	_, exists, err := loadManagedAuth(home, stateHome)
	return exists, err
}

// LoginStatus describes the Codex credential visible to gax for interactive login pickers.
func LoginStatus(home, stateHome string) string {
	auth, err := LoadAuth(home, stateHome)
	if err != nil {
		return "not logged in"
	}
	if auth.Source == SourceGax {
		if auth.Email != "" {
			return "logged in as " + auth.Email + " — gax manages this token"
		}
		return "logged in — gax manages this token"
	}
	if auth.Email != "" {
		return "using codex CLI credentials of " + auth.Email + " (read-only) — log in to let gax manage its own token"
	}
	return "using codex CLI credentials (read-only) — log in to let gax manage its own token"
}

func loadManagedAuth(home, stateHome string) (Auth, bool, error) {
	path, err := authStorePath(home, stateHome)
	if err != nil {
		return Auth{}, false, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	body, err := systemfs.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Auth{}, false, nil
		}
		return Auth{}, false, err
	}
	root := jsonObject(body)
	if root == nil {
		return Auth{}, false, errors.New("gax credential store is not a JSON object")
	}
	auth, err := parseGaxAuth(root["codex"])
	if err != nil {
		return Auth{}, false, nil
	}
	auth.storePath = path
	auth.home = home
	auth.stateHome = stateHome
	return auth, true, nil
}

// LogoutManaged removes the gax-owned Codex entry under the same sidecar lock used by token
// refresh, then revokes its refresh token best-effort. Local removal succeeds even when offline.
func LogoutManaged(ctx context.Context, home, stateHome string, client *transport.Client,
	revokeURL string) (removed bool, warning string, err error) {
	path, err := authStorePath(home, stateHome)
	if err != nil {
		return false, "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	unlock, err := lockAuthStore(path)
	if err != nil {
		return false, "", err
	}
	var refreshToken string
	body, readErr := systemfs.ReadFile(path)
	if readErr != nil {
		unlock()
		if errors.Is(readErr, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", readErr
	}
	root := jsonObject(body)
	if root == nil {
		unlock()
		return false, "", errors.New("gax credential store is not a JSON object")
	}
	rawEntry, exists := root["codex"]
	if !exists {
		unlock()
		return false, "", nil
	}
	entry := jsonObject(rawEntry)
	refreshToken = jsonString(entry["refresh_token"])
	delete(root, "codex")
	if err := writeAuthStore(path, root); err != nil {
		unlock()
		return false, "", err
	}
	unlock()

	if refreshToken != "" {
		if revokeURL == "" {
			revokeURL = revokeEndpoint
		}
		if !revokeRefreshToken(ctx, client, revokeURL, refreshToken) {
			warning = "could not revoke the token server-side — it expires on its own"
		}
	}
	return true, warning, nil
}

func authStorePath(home, stateHome string) (string, error) {
	path, ok := systempath.XDGGaxPath(stateHome, home, ".local/state", "auth.json")
	if !ok {
		return "", errors.New("could not determine the state directory")
	}
	return path, nil
}

func revokeRefreshToken(ctx context.Context, client *transport.Client, endpoint, refreshToken string) bool {
	wiretrace.Default().RegisterSecret(refreshToken)
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
		return false
	}
	_, status, err := client.Post(ctx, endpoint, nil, "application/json", body, revokeTimeout, maxBodySize, nil)
	return err == nil && status >= 200 && status < 300
}

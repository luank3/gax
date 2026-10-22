package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/wiretrace"
)

const (
	refreshEndpoint = "https://auth.openai.com/oauth/token"
	oauthClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshTimeout  = 30 * time.Second
	refreshMargin   = 10 * time.Minute
)

type refreshResult uint8

const (
	refreshFresh refreshResult = iota
	refreshTransient
	refreshDead
)

// ensureFresh refreshes gax-managed credentials under the auth-store lock. A dispatched refresh
// ignores cancellation because the authorization server may consume the rotating refresh token.
func ensureFresh(ctx context.Context, auth *Auth, force bool, client *transport.Client,
	tick func() bool) (refreshResult, string) {
	if auth.Source != SourceGax {
		if force {
			return refreshDead, ""
		}
		return refreshFresh, ""
	}
	if !force && !tokenExpiring(auth.AccessToken, time.Now().Add(refreshMargin)) {
		return refreshFresh, ""
	}
	if auth.storePath == "" || auth.RefreshToken == "" {
		return refreshDead, ""
	}
	if client == nil {
		client = transport.DefaultClient
	}
	unlock, err := lockAuthStore(auth.storePath)
	if err != nil {
		return refreshTransient, ""
	}
	defer unlock()

	body, err := systemfs.ReadFile(auth.storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return refreshDead, ""
		}
		return refreshTransient, ""
	}
	root := jsonObject(json.RawMessage(body))
	entryRaw := root["codex"]
	stored, err := parseGaxAuth(entryRaw)
	if err != nil {
		return refreshDead, ""
	}
	if stored.AccountID != auth.AccountID {
		return refreshDead, ""
	}
	entryDiffers := stored.AccessToken != auth.AccessToken || stored.RefreshToken != auth.RefreshToken
	if entryDiffers && tokenAsFresh(stored.AccessToken, auth.AccessToken) &&
		(force || !tokenExpiring(stored.AccessToken, time.Now().Add(refreshMargin))) {
		adoptStoredAuth(auth, stored)
		return refreshFresh, ""
	}

	refreshToken := stored.RefreshToken
	if entryDiffers && !tokenAsFresh(stored.AccessToken, auth.AccessToken) {
		// The process memory is ahead of disk after an earlier persistence failure.
		refreshToken = auth.RefreshToken
	}
	wiretrace.Default().RegisterSecret(refreshToken)
	requestBody, _ := json.Marshal(map[string]string{
		"client_id": oauthClientID, "grant_type": "refresh_token", "refresh_token": refreshToken,
	})
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	rotationTick := func() bool {
		if tick != nil {
			tick()
		}
		return false
	}
	response, status, requestErr := client.Post(refreshCtx, refreshEndpoint, nil, "application/json",
		requestBody, refreshTimeout, maxBodySize, rotationTick)
	if requestErr != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
		if isTerminalRefreshRejection(status, response) {
			warning := ""
			if refreshToken == stored.RefreshToken {
				delete(root, "codex")
				if saveErr := writeAuthStore(auth.storePath, root); saveErr != nil {
					warning = "could not remove rejected Codex credentials from the gax auth store"
				}
			}
			return refreshDead, warning
		}
		return refreshTransient, ""
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(response, &values) != nil || values == nil {
		return refreshTransient, ""
	}
	if access := jsonString(values["access_token"]); access == "" {
		return refreshTransient, ""
	}
	merged := jsonObject(entryRaw)
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		if value := jsonString(values[key]); value != "" {
			encoded, _ := json.Marshal(value)
			merged[key] = encoded
		}
	}
	updatedEntry, _ := json.Marshal(merged)
	root["codex"] = updatedEntry
	updated, err := parseGaxAuth(updatedEntry)
	if err != nil {
		return refreshTransient, ""
	}
	wiretrace.Default().RegisterSecret(updated.RefreshToken)
	warning := ""
	if saveErr := writeAuthStore(auth.storePath, root); saveErr != nil {
		warning = "could not persist refreshed Codex credentials to the gax auth store"
	}
	adoptStoredAuth(auth, updated)
	return refreshFresh, warning
}

// PrepareRequestAuth refreshes an expiring gax-managed token before an outbound model request.
// Refresh failures remain best-effort until the request itself is rejected.
func PrepareRequestAuth(ctx context.Context, auth *Auth, client *transport.Client, tick func() bool) string {
	if auth == nil {
		return ""
	}
	_, warning := ensureFresh(ctx, auth, false, client, tick)
	return warning
}

// TokenExpiresWithin reports whether a JWT token expires before the supplied safety margin.
// Opaque tokens and JWTs without a valid expiry remain usable until the server rejects them.
func TokenExpiresWithin(token string, margin time.Duration) bool {
	return tokenExpiring(token, time.Now().Add(margin))
}

// RecoverUnauthorized refreshes or adopts the same account's canonical credential after a 401.
func RecoverUnauthorized(ctx context.Context, auth *Auth, client *transport.Client,
	tick func() bool) (retry bool, warning, message string) {
	if auth == nil || auth.AccessToken == "" {
		return false, "", "codex is not logged in — run /login"
	}
	if auth.Source != SourceGax {
		return false, "", "codex CLI token expired — rerun `codex`, or use /login"
	}
	result, warning := ensureFresh(ctx, auth, true, client, tick)
	if result == refreshFresh {
		return true, warning, "codex login expired — run /login again"
	}
	if result == refreshTransient {
		return false, warning, "could not refresh the codex login — retry, or run /login"
	}
	if fallback, err := LoadAuth(auth.home, auth.stateHome); err == nil {
		if fallback.AccountID != auth.AccountID {
			return false, warning, "the codex login belongs to a different account — run /login or /provider to switch"
		}
		if !sameAuth(*auth, fallback) {
			*auth = fallback
			return true, warning, "codex login expired — run /login again"
		}
	}
	return false, warning, "codex login expired — run /login again"
}

func adoptStoredAuth(destination *Auth, source Auth) {
	storePath, home, stateHome := destination.storePath, destination.home, destination.stateHome
	*destination = source
	destination.storePath, destination.home, destination.stateHome = storePath, home, stateHome
}

func tokenExpiring(token string, threshold time.Time) bool {
	expiry := tokenExpiry(token)
	return !expiry.IsZero() && !expiry.After(threshold)
}

func tokenAsFresh(candidate, current string) bool {
	candidateExpiry, currentExpiry := tokenExpiry(candidate), tokenExpiry(current)
	if candidateExpiry.IsZero() || currentExpiry.IsZero() {
		return true
	}
	return !candidateExpiry.Before(currentExpiry)
}

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	claims := jsonObject(payload)
	expiry := jsonNumber(claims["exp"])
	if expiry == nil {
		return time.Time{}
	}
	longLimit := math.Ldexp(1, strconv.IntSize-1)
	if *expiry <= -longLimit || *expiry >= longLimit {
		return time.Time{}
	}
	seconds := int64(*expiry)
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func isTerminalRefreshRejection(status int, body []byte) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return false
	}
	code := jsonString(root["code"])
	if code == "" {
		if raw := root["error"]; len(raw) > 0 {
			if json.Unmarshal(raw, &code) != nil {
				var object map[string]json.RawMessage
				if json.Unmarshal(raw, &object) == nil {
					code = jsonString(object["code"])
				}
			}
		}
	}
	switch code {
	case "invalid_grant", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		return true
	default:
		return false
	}
}

func writeAuthStore(path string, root map[string]json.RawMessage) error {
	directory := systempath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".auth.json-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return systemfs.SyncDirectory(directory)
}

func refreshErrorMessage() error {
	return errors.New("could not refresh the codex login — retry, or run /login")
}

package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthStorePathPreservesConfiguredStateHomeFormatting(t *testing.T) {
	path, err := authStorePath("/home/alice", "/tmp/state/")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/state//hax/auth.json"; path != want {
		t.Fatalf("authStorePath() = %q, want %q", path, want)
	}
}

func TestLogoutManagedRemovesCodexAndRevokesStoredRefreshToken(t *testing.T) {
	stateHome := t.TempDir()
	storePath := filepath.Join(stateHome, "hax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	store := `{"codex":{"access_token":"access","refresh_token":"refresh-secret","account_id":"acc"},"other":{"token":"keep"}}`
	if err := os.WriteFile(storePath, []byte(store), 0600); err != nil {
		t.Fatal(err)
	}
	var revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]string
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			json.Unmarshal(body, &request) != nil || request["token"] != "refresh-secret" ||
			request["token_type_hint"] != "refresh_token" || request["client_id"] != oauthClientID {
			t.Errorf("revocation request = %s %s %q %s", r.Method, r.URL, r.Header.Get("Content-Type"), body)
		}
		revoked = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	removed, warning, err := LogoutManaged(context.Background(), t.TempDir(), stateHome, nil, server.URL)
	if err != nil || !removed || warning != "" || !revoked {
		t.Fatalf("LogoutManaged() = removed %t, warning %q, err %v, revoked %t", removed, warning, err, revoked)
	}
	updated, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	var other map[string]string
	if json.Unmarshal(updated, &root) != nil || len(root) != 1 ||
		json.Unmarshal(root["other"], &other) != nil || other["token"] != "keep" {
		t.Fatalf("logout damaged other credentials: %s", updated)
	}
	info, err := os.Stat(storePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions = %v, err=%v", info, err)
	}
}

func TestLogoutManagedRemovesOfflineAndReturnsRevocationWarning(t *testing.T) {
	stateHome := t.TempDir()
	storePath := filepath.Join(stateHome, "hax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte(`{"codex":{"refresh_token":"refresh-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	removed, warning, err := LogoutManaged(context.Background(), t.TempDir(), stateHome, nil, server.URL)
	if err != nil || !removed || !strings.Contains(warning, "could not revoke the token server-side") {
		t.Fatalf("offline logout = removed %t, warning %q, err %v", removed, warning, err)
	}
	updated, err := os.ReadFile(storePath)
	if err != nil || strings.Contains(string(updated), "refresh-secret") {
		t.Fatalf("offline logout retained the secret: %s, err=%v", updated, err)
	}
}

func TestManagedAuthDoesNotCountBorrowedCodexCLIFile(t *testing.T) {
	home, stateHome := t.TempDir(), t.TempDir()
	cliPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(cliPath), 0700); err != nil {
		t.Fatal(err)
	}
	cliAuth := `{"tokens":{"access_token":"cli-access","account_id":"cli-account"}}`
	if err := os.WriteFile(cliPath, []byte(cliAuth), 0600); err != nil {
		t.Fatal(err)
	}
	managed, err := HasManagedAuth(home, stateHome)
	if err != nil || managed {
		t.Fatalf("HasManagedAuth() = %t, err=%v; borrowed CLI credentials must not count", managed, err)
	}
	loaded, err := LoadAuth(home, stateHome)
	if err != nil || loaded.Source != SourceCLI || loaded.AccessToken != "cli-access" {
		t.Fatalf("LoadAuth() = %+v, err=%v", loaded, err)
	}
}

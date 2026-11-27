package codex_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/luank3/gax/internal/provider/codex"
)

func TestCodexCredentialsAndDefaults(t *testing.T) {
	stateDir := t.TempDir()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".codex", "config.toml"), []byte("model = \"gpt-5.4\"\nmodel_reasoning_effort = \"high\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, e := codex.ReadCodexDefaults(homeDir)
	if m != "gpt-5.4" || e != "high" {
		t.Fatalf("unexpected defaults: model=%q effort=%q", m, e)
	}

	if err := codex.SaveCredentials(stateDir, codex.Credentials{AccessToken: "tok-123"}); err != nil {
		t.Fatal(err)
	}
	creds, managed, err := codex.LoadCredentials(stateDir, homeDir)
	if err != nil || !managed || creds.AccessToken != "tok-123" {
		t.Fatalf("unexpected creds: %+v managed=%v err=%v", creds, managed, err)
	}

	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pay := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt-1"}}`))
	jwtTok := hdr + "." + pay + ".sig"
	if err := codex.SaveCredentials(stateDir, codex.Credentials{AccessToken: jwtTok}); err != nil {
		t.Fatal(err)
	}
	creds, _, err = codex.LoadCredentials(stateDir, homeDir)
	if err != nil || creds.AccountID != "acct-jwt-1" {
		t.Fatalf("expected AccountID acct-jwt-1 from JWT, got %+v err=%v", creds, err)
	}
	if err := codex.RemoveCredentials(stateDir); err != nil {
		t.Fatal(err)
	}
}

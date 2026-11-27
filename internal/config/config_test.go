package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luank3/gax/internal/config"
)

func TestLayeredPrecedence(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "cfg")
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"theme":"light","compact":{"threshold":70}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte(`{"theme":"ansi"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := config.Load(cfgDir, stateDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, src := s.Resolve("theme"); got != "ansi" || src != config.SourceState {
		t.Fatalf("expected state ansi, got %q (%s)", got, src)
	}
	if got := s.Int("compact.threshold"); got != 70 {
		t.Fatalf("expected 70, got %d", got)
	}

	t.Setenv("GAX_THEME", "dark")
	if got, src := s.Resolve("theme"); got != "dark" || src != config.SourceEnv {
		t.Fatalf("expected env dark, got %q (%s)", got, src)
	}

	s.SetSession("theme", "off")
	if got, src := s.Resolve("theme"); got != "off" || src != config.SourceSession {
		t.Fatalf("expected session off, got %q (%s)", got, src)
	}

	s.SetOverride("theme", "light")
	if got, src := s.Resolve("theme"); got != "light" || src != config.SourceOverride {
		t.Fatalf("expected override light, got %q (%s)", got, src)
	}

	s.SetOverride("theme", config.DefaultSentinel)
	if got, src := s.Resolve("theme"); got != "off" || src != config.SourceSession {
		t.Fatalf("expected cleared override -> session off, got %q (%s)", got, src)
	}
}

func TestParsers(t *testing.T) {
	if b, ok := config.ParseBool("ON"); !ok || !b {
		t.Fatalf("ParseBool(ON) failed")
	}
	if d, err := config.ParseDuration("2m"); err != nil || d != 2*time.Minute {
		t.Fatalf("ParseDuration(2m): %v %v", d, err)
	}
	if b, err := config.ParseBytes("50k"); err != nil || b != 50*1024 {
		t.Fatalf("ParseBytes(50k): %d %v", b, err)
	}
	if tok, err := config.ParseTokens("272k"); err != nil || tok != 272000 {
		t.Fatalf("ParseTokens(272k): %d %v", tok, err)
	}
	if port := config.NewStore().Int("providers.llamacpp.port"); port != 9931 {
		t.Fatalf("expected llamacpp default port 9931, got %d", port)
	}
}

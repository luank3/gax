package app

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luank3/gax/internal/config"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

func TestConfigureTerminalThemeUsesPresetTintAndRunOverride(t *testing.T) {
	previous := terminaltheme.Current()
	defer terminaltheme.Configure(previous.Name(), previous.TintName())
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetOverride("theme", stringPtr("dark"))
	store.SetOverride("preset", stringPtr("review"))
	if err := store.LoadConfig([]byte(`{"tint":"teal","presets":{"review":{"provider":"mock","tint":"violet"}}}`)); err != nil {
		t.Fatal(err)
	}
	var warnings strings.Builder
	configureTerminalTheme(store, &warnings)
	if got := terminaltheme.Current(); got.Name() != "dark" || got.TintName() != "violet" {
		t.Fatalf("preset theme = %q/%q, want dark/violet", got.Name(), got.TintName())
	}
	store.SetOverride("tint", stringPtr("rose"))
	configureTerminalTheme(store, &warnings)
	if got := terminaltheme.Current(); got.Name() != "dark" || got.TintName() != "rose" {
		t.Fatalf("run tint override = %q/%q, want dark/rose", got.Name(), got.TintName())
	}
	if warnings.Len() != 0 {
		t.Fatalf("valid theme values warned: %q", warnings.String())
	}
}

func TestPresetTintPreviewUsesPresetBeforeBelowRunTint(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"tint":"sage"}`)); err != nil {
		t.Fatal(err)
	}
	store.SetOverride("tint", stringPtr("rose"))
	if got := presetTintPreview("violet", store); got != "\x1b[38;5;140m" {
		t.Fatalf("explicit preset tint preview = %q", got)
	}
	if got := presetTintPreview("", store); got != "\x1b[38;5;114m" {
		t.Fatalf("below-run tint preview = %q, want config sage", got)
	}
}

func TestConfigureTerminalThemeKeepsRedirectedWarningsPlain(t *testing.T) {
	previous := terminaltheme.Current()
	t.Cleanup(func() { terminaltheme.Configure(previous.Name(), previous.TintName()) })
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	// A repeated bad value is warned about once per process, so each run needs its own name.
	badTheme := fmt.Sprintf("parity-invalid-theme-%d", themeWarningSequence.Add(1))
	store.SetOverride("theme", stringPtr(badTheme))

	var output strings.Builder
	configureTerminalTheme(store, &output)
	if !strings.Contains(output.String(), "gax: unknown theme '"+badTheme+"'") {
		t.Fatalf("theme warning = %q", output.String())
	}
	if strings.ContainsRune(output.String(), '\x1b') {
		t.Fatalf("redirected theme warning contains terminal styling: %q", output.String())
	}
}

// themeWarningSequence keeps this file's bad values distinct across -count runs.
var themeWarningSequence atomic.Int64

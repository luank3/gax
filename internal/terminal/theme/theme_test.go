package theme

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestANSIThemeRoleSequences(t *testing.T) {
	palette, warnings := New("ansi", "teal", nil)
	if len(warnings) != 0 || palette.Name() != "ansi" {
		t.Fatalf("New(ansi) = %q, warnings %v", palette.Name(), warnings)
	}
	want := map[Role][2]string{
		Accent:     {"\x1b[95m", "\x1b[39m"},
		Chrome:     {"\x1b[36m", "\x1b[39m"},
		ChromeDim:  {"\x1b[2m\x1b[36m", "\x1b[39m\x1b[22m"},
		Stance:     {"\x1b[36m", "\x1b[39m"},
		CodeInline: {"\x1b[36m", "\x1b[39m"},
		CodeBlock:  {"\x1b[2m", "\x1b[22m"},
		Heading:    {"\x1b[1m", "\x1b[22m"},
		Link:       {"\x1b[4m", "\x1b[24m"},
		Add:        {"\x1b[32m", "\x1b[39m"},
		Remove:     {"\x1b[31m", "\x1b[39m"},
		OK:         {"\x1b[32m", "\x1b[39m"},
		Error:      {"\x1b[31m", "\x1b[39m"},
		Warn:       {"\x1b[33m", "\x1b[39m"},
	}
	for role, sequences := range want {
		if got := [2]string{palette.Open(role), palette.Close(role)}; got != sequences {
			t.Errorf("role %d = %q, want %q", role, got, sequences)
		}
	}
}

func TestDarkThemeTintsOnlyModelRolesAndPreserveCloseSequences(t *testing.T) {
	teal, _ := New("dark", "teal", nil)
	violet, warnings := New("DARK", "VIOLET", nil)
	if len(warnings) != 0 || violet.Name() != "dark" || violet.TintName() != "violet" {
		t.Fatalf("New(dark, violet) = %q/%q, warnings %v", violet.Name(), violet.TintName(), warnings)
	}
	for _, role := range []Role{Accent, Chrome, ChromeDim, Add, Remove, OK, Error, Warn} {
		if violet.Open(role) != teal.Open(role) {
			t.Errorf("fixed role %d changed from %q to %q", role, teal.Open(role), violet.Open(role))
		}
	}
	for _, role := range []Role{Stance, CodeInline, CodeBlock, Heading, Link} {
		if violet.Open(role) == teal.Open(role) {
			t.Errorf("tinted role %d stayed %q", role, violet.Open(role))
		}
		if violet.Close(role) != teal.Close(role) {
			t.Errorf("tint changed role %d close sequence", role)
		}
	}
	if violet.Open(Heading) != "\x1b[1m\x1b[38;5;140m" || violet.Open(Link) != "\x1b[4;38;5;97m" {
		t.Fatalf("violet heading/link sequences = %q / %q", violet.Open(Heading), violet.Open(Link))
	}
}

func TestOffThemeKeepsNonColorAttributes(t *testing.T) {
	palette, _ := New("off", "rose", nil)
	for _, role := range []Role{Accent, Chrome, Stance, CodeInline, Add, Remove, OK, Error, Warn} {
		if palette.Open(role) != "" || palette.Close(role) != "" {
			t.Errorf("off theme role %d = %q/%q", role, palette.Open(role), palette.Close(role))
		}
	}
	if palette.Open(Heading) != "\x1b[1m" || palette.Open(CodeBlock) != "\x1b[2m" ||
		palette.Open(ChromeDim) != "\x1b[2m" || palette.Open(Link) != "\x1b[4m" {
		t.Fatalf("off theme discarded structural emphasis")
	}
}

func TestTintPreviewUsesTheActiveThemePalette(t *testing.T) {
	dark, _ := New("dark", "teal", nil)
	if got := dark.TintOpen("violet"); got != "\x1b[38;5;140m" {
		t.Fatalf("dark violet preview = %q", got)
	}
	light, _ := New("light", "teal", nil)
	if got := light.TintOpen("sage"); got != "\x1b[38;5;71m" {
		t.Fatalf("light sage preview = %q", got)
	}
	ansi, _ := New("ansi", "teal", nil)
	off, _ := New("off", "teal", nil)
	if ansi.TintOpen("violet") != "" || off.TintOpen("sage") != "" || dark.TintOpen("unknown") != "" {
		t.Fatal("unsupported theme or tint produced a preview color")
	}
}

func TestAutoThemeAndInvalidValues(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	}
	light, warnings := New("auto", "sage", lookup(map[string]string{
		"TERM": "xterm-256color", "COLORFGBG": "0;15",
	}))
	if len(warnings) != 0 || light.Name() != "light" {
		t.Fatalf("auto light = %q, warnings %v", light.Name(), warnings)
	}
	off, _ := New("auto", "teal", lookup(map[string]string{"TERM": "dumb"}))
	if off.Name() != "off" {
		t.Fatalf("TERM=dumb selected %q", off.Name())
	}
	fallback, warnings := New("unknown", "invalid", lookup(map[string]string{"TERM": "xterm-256color"}))
	if fallback.Name() != "dark" || fallback.TintName() != "teal" || len(warnings) != 2 ||
		!strings.Contains(warnings[0], "unknown theme") || !strings.Contains(warnings[1], "unknown tint") {
		t.Fatalf("invalid values selected %q/%q with warnings %v", fallback.Name(), fallback.TintName(), warnings)
	}
}

// Configure warns once per offending value for the life of the process, so each run of this test
// needs values that no earlier run has used.
var warningDedupeSequence atomic.Int64

func uniqueBadValue(kind string) string {
	return fmt.Sprintf("missing-%s-%d", kind, warningDedupeSequence.Add(1))
}

func TestConfigureSuppressesRepeatedWarningsForTheSameValue(t *testing.T) {
	previous := Current()
	t.Cleanup(func() { Configure(previous.Name(), previous.TintName()) })

	name := uniqueBadValue("theme")
	if warnings := Configure(name, "teal"); len(warnings) != 1 || !strings.Contains(warnings[0], "unknown theme") {
		t.Fatalf("first Configure(%q) warnings = %v, want one unknown-theme warning", name, warnings)
	}
	if warnings := Configure(name, "teal"); len(warnings) != 0 {
		t.Fatalf("repeated Configure(%q) warnings = %v, want none", name, warnings)
	}
	if warnings := Configure(uniqueBadValue("theme"), "teal"); len(warnings) != 1 {
		t.Fatalf("a different bad theme must warn again: %v", warnings)
	}

	tint := uniqueBadValue("tint")
	if warnings := Configure("dark", tint); len(warnings) != 1 || !strings.Contains(warnings[0], "unknown tint") {
		t.Fatalf("first bad-tint warnings = %v, want one unknown-tint warning", warnings)
	}
	if warnings := Configure("dark", tint); len(warnings) != 0 {
		t.Fatalf("repeated bad tint warnings = %v, want none", warnings)
	}
	if warnings := Configure("dark", uniqueBadValue("tint")); len(warnings) != 1 {
		t.Fatalf("a different bad tint must warn again: %v", warnings)
	}
}

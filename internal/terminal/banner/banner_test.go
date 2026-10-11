package banner

import (
	"bytes"
	"strings"
	"testing"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

func TestIdentityLineLayoutsMatchTerminalWidth(t *testing.T) {
	tests := []struct {
		name     string
		identity Identity
		columns  int
		want     string
	}{
		{
			name:     "single row",
			identity: Identity{Provider: "mock", Model: "model-a", Effort: "high"}, columns: 100,
			want: "▌ gax › mock · model-a · high\n",
		},
		{
			name:     "provider breaks before model and effort",
			identity: Identity{Provider: "mock", Model: "abcdefghijklmnopqrstuv", Effort: "high"}, columns: 40,
			want: "▌ gax › mock\n▌   abcdefghijklmnopqrstuv · high\n",
		},
		{
			name:     "oversized model hard wraps",
			identity: Identity{Provider: "mock", Model: "deepseek-ai/DeepSeek-R1-Distill-Llama-70B"}, columns: 20,
			want: "▌ gax › mock\n▌   deepseek-ai/Deep\n▌   Seek-R1-Distill-\n▌   Llama-70B\n",
		},
		{
			name:     "no provider",
			identity: Identity{}, columns: 100,
			want: "▌ gax › no provider — use /provider\n",
		},
		{
			name:     "no model",
			identity: Identity{Provider: "mock"}, columns: 100,
			want: "▌ gax › mock · no model — use /model (or /provider)\n",
		},
		{
			name:     "preset stance",
			identity: Identity{Preset: "fast", Provider: "mock", Model: "model-a"}, columns: 100,
			want: "▌ gax [fast] › mock · model-a\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := IdentityLine(&output, test.identity, test.columns); err != nil {
				t.Fatal(err)
			}
			if got := stripSGR(output.String()); got != test.want {
				t.Fatalf("identity line = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBannerUsesThemeChromeAndStanceRoles(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "violet")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())
	var output bytes.Buffer
	if err := IdentityLine(&output, Identity{Preset: "review", Provider: "mock", Model: "m"}, 100); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		terminaltheme.Open(terminaltheme.Chrome) + "▌" + terminaltheme.Close(terminaltheme.Chrome),
		terminaltheme.Open(terminaltheme.Stance) + " [review]" + terminaltheme.Close(terminaltheme.Stance),
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("banner did not use theme sequence %q: %q", want, output.String())
		}
	}
}

func TestPrintAddsKeyTips(t *testing.T) {
	var output bytes.Buffer
	if err := Print(&output, Identity{Provider: "mock", Model: "model-a"}, 100); err != nil {
		t.Fatal(err)
	}
	if got, want := stripSGR(output.String()), "▌ gax › mock · model-a\n▌ ctrl-d quit · try /help\n"; got != want {
		t.Fatalf("banner = %q, want %q", got, want)
	}
}

func stripSGR(value string) string {
	var plain strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) {
				current := value[index]
				index++
				if current >= 0x40 && current <= 0x7e {
					break
				}
			}
			continue
		}
		plain.WriteByte(value[index])
		index++
	}
	return plain.String()
}

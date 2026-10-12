package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/slash"
	coretools "github.com/luank3/gax/internal/tools"
)

// runTestREPL drives the line-oriented REPL with stubs that never reach a provider.
func runTestREPL(ctx context.Context, stdin io.Reader, inputTTY bool, stdout, stderr io.Writer) int {
	return runREPL(ctx, stdin, inputTTY, false, stdout, stderr, config.New(config.DefaultSettings()),
		(*coretools.Registry)(nil), (*agent.Session)(nil),
		func(string) replOutput { return replOutput{} }, new(string), nil,
		func(slash.Parsed) bool { return false })
}

// The prompt marker is for a human at a terminal: redirected input must stay clean for pipes and
// test harnesses, which is what the non-TTY gate at repl.go protects.
func TestREPLPromptMarkerFollowsTheInputTTYFlag(t *testing.T) {
	for _, test := range []struct {
		name     string
		inputTTY bool
		want     int
	}{
		{name: "terminal input shows the marker before each read", inputTTY: true, want: 2},
		{name: "redirected input shows no marker", inputTTY: false, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := runTestREPL(context.Background(), strings.NewReader("hello\n"), test.inputTTY, &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("REPL exit: code=%d stderr=%q", code, stderr.String())
			}
			if got := strings.Count(stdout.String(), "> "); got != test.want {
				t.Fatalf("prompt markers = %d, want %d in %q", got, test.want, stdout.String())
			}
		})
	}
}

func TestREPLCancelledContextExitsWith130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := io.Pipe()
	defer writer.Close()

	var stdout, stderr strings.Builder
	if code := runTestREPL(ctx, reader, false, &stdout, &stderr); code != 130 {
		t.Fatalf("cancelled REPL exit = %d, want 130", code)
	}
}

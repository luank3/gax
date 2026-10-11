package notify

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

func TestSelectNotificationMethod(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		terminal   bool
		env        Environment
		want       Method
	}{
		{name: "non terminal", configured: "bel", want: Disabled},
		{name: "off", configured: "OFF", terminal: true, want: Disabled},
		{name: "explicit bell", configured: "BeL", terminal: true, env: Environment{Term: "dumb"}, want: Bell},
		{name: "explicit OSC 9", configured: "osc9", terminal: true, env: Environment{Term: "dumb"}, want: OSC9},
		{name: "dumb terminal", terminal: true, env: Environment{Term: "dumb"}, want: Disabled},
		{name: "tmux defaults to bell", terminal: true, env: Environment{Tmux: "1", TermProgram: "iTerm.app"}, want: Bell},
		{name: "iTerm supports OSC 9", terminal: true, env: Environment{TermProgram: "iTerm.app"}, want: OSC9},
		{name: "Ghostty environment supports OSC 9", terminal: true, env: Environment{GhosttyResourcesDir: "/ghostty"}, want: OSC9},
		{name: "WezTerm environment supports OSC 9", terminal: true, env: Environment{WezTermPane: "1"}, want: OSC9},
		{name: "unknown terminal defaults to bell", terminal: true, want: Bell},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Select(test.configured, test.terminal, test.env); got != test.want {
				t.Fatalf("Select(%q, %t, %+v) = %q, want %q", test.configured, test.terminal, test.env, got, test.want)
			}
		})
	}
}

func TestAttentionEmitsBellOrTmuxWrappedOSC9(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		env        Environment
		want       string
	}{
		{name: "bell", configured: "bel", want: "\a"},
		{name: "OSC 9", configured: "osc9", want: "\x1b]9;gax: ready\a"},
		{name: "OSC 9 through tmux", configured: "osc9", env: Environment{Tmux: "1"},
			want: "\x1bPtmux;\x1b\x1b]9;gax: ready\a\x1b\\"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := Attention(&output, true, test.configured, test.env); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("notification = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAttentionDoesNotWriteForDisabledMethod(t *testing.T) {
	var output bytes.Buffer
	if err := Attention(&output, false, "osc9", Environment{}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("non-terminal notification wrote %q", output.String())
	}
}

func TestAttentionReturnsWriteError(t *testing.T) {
	want := errors.New("write failed")
	err := Attention(errorWriter{err: want}, true, "bel", Environment{})
	if !errors.Is(err, want) {
		t.Fatalf("Attention error = %v, want %v", err, want)
	}
}

func TestIsTerminalWriterRejectsNonFileWriters(t *testing.T) {
	if IsTerminalWriter(io.Discard) || IsTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("non-file writer reported as terminal")
	}
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if IsTerminalWriter(file) {
		t.Fatal("character device without terminal ioctls reported as terminal")
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

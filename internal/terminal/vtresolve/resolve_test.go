package vtresolve

import (
	"strings"
	"testing"
)

func TestResolveTerminalRows(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain rows", in: "hello\nworld\n", want: "hello\nworld\n"},
		{name: "partial row terminates", in: "tail", want: "tail\n"},
		{name: "retro wrap erases partial word", in: "alpha beta\x1b[5D\x1b[K\nbeta\n", want: "alpha\nbeta\n"},
		{name: "erase keeps style runs", in: "ab\x1b[1mcd\x1b[2D\x1b[K\n", want: "ab\x1b[1m\x1b[0m\n"},
		{name: "carriage return overprints", in: "| body\r+\n", want: "+ body\n"},
		{name: "overprint keeps surrounding style", in: "\x1b[2m|\x1b[0m body\r\x1b[1m+\x1b[0m\n", want: "\x1b[2m\x1b[1m+\x1b[0m\x1b[0m body\n"},
		{name: "user echo row break", in: "| one\x1b[K\r\n| two\x1b[K\r\n", want: "| one\n| two\n"},
		{name: "newline keeps rest of row", in: "abcdef\rXY\n", want: "XYcdef\n"},
		{name: "multibyte glyph columns", in: "││\x1b[1D\x1b[K\n", want: "│\n"},
		{name: "cursor forward pads", in: "ab\x1b[3Cc\n", want: "ab   c\n"},
		{name: "unmodeled escape passes through", in: "a\x1b[2Ab\n", want: "a\x1b[2Ab\n"},
		{name: "double width glyph columns", in: "ab你\x1b[2D\x1b[K\n", want: "ab\n"},
		{name: "cursor back clamps", in: "abc\x1b[99DZ\n", want: "Zbc\n"},
		{name: "unterminated CSI is consumed", in: "ab\x1b[", want: "ab\x1b[\n"},
		{name: "bare escape is consumed", in: "ab\x1b", want: "ab\x1b\n"},
		{name: "OSC passes through whole", in: "a\x1b]8;;http://x\ab\n", want: "a\x1b]8;;http://x\ab\n"},
		{name: "multi parameter SGR passes through", in: "\x1b[38;5;173mtinted\x1b[39m\n", want: "\x1b[38;5;173mtinted\x1b[39m\n"},
		{name: "styled run reopens each row", in: "\x1b[2m\x1b[3mone\ntwo\nthree\x1b[0m\nplain\n",
			want: "\x1b[2m\x1b[3mone\x1b[0m\n\x1b[2;3mtwo\x1b[0m\n\x1b[2;3mthree\x1b[0m\nplain\n"},
		{name: "extended color reopens verbatim", in: "\x1b[38;5;173mone\ntwo\x1b[39m\n",
			want: "\x1b[38;5;173mone\x1b[0m\n\x1b[38;5;173mtwo\x1b[39m\n"},
		{name: "blank styled row stays bare", in: "\x1b[1mone\n\ntwo\x1b[0m\n",
			want: "\x1b[1mone\x1b[0m\n\n\x1b[1mtwo\x1b[0m\n"},
		{name: "selective SGR off codes stop carry", in: "\x1b[1m\x1b[3ma\x1b[22mb\nc\n",
			want: "\x1b[1m\x1b[3ma\x1b[22mb\x1b[0m\n\x1b[3mc\x1b[0m\n"},
		{name: "private final m does not track", in: "\x1b[1ma\x1b[>4;2mb\nc\x1b[0m\n",
			want: "\x1b[1ma\x1b[>4;2mb\x1b[0m\n\x1b[1mc\x1b[0m\n"},
		{name: "first CSI parameter controls erase", in: "abcdef\x1b[4D\x1b[1;99KZ\n", want: "  Zdef\n"},
		{name: "empty input", in: "", want: ""},
		{name: "erase whole row", in: "junk\x1b[2Kkept\n", want: "    kept\n"},
		{name: "erase to cursor keeps right side", in: "abcdef\x1b[4D\x1b[1KZ\n", want: "  Zdef\n"},
		{name: "erase to cursor at end pads", in: "abc\x1b[1KZ\n", want: "   Z\n"},
		{name: "combining mark is zero width", in: "xe\u0301\x1b[1D\x1b[K\n", want: "x\n"},
		{name: "erase keeps combining mark with base", in: "e\u0301x\x1b[1D\x1b[K\n", want: "e\u0301\n"},
		{name: "erase removes orphan combining mark", in: "\u0301a\r\x1b[K\n", want: "\n"},
		{name: "zero width writes retain order", in: "a\r\u0301\u0327\x1b[31m\n", want: "\u0301\u0327\x1b[31ma\x1b[0m\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := string(Resolve([]byte(test.in)))
			if got != test.want {
				t.Fatalf("Resolve() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveClampsAbsurdCursorForward(t *testing.T) {
	for _, sequence := range []string{"\x1b[2147483647C", "\x1b[99999999999999999999C"} {
		out := string(Resolve([]byte("a" + sequence + "b\n")))
		if len(out) >= 8192 || !strings.Contains(out, "b") {
			t.Fatalf("cursor forward %q was not bounded: output length=%d", sequence, len(out))
		}
	}
}

//go:build linux

package input

import "testing"

func TestEscapeClassifier(t *testing.T) {
	tests := []struct {
		name string
		feed string
		want int
	}{
		{name: "text is ignored", feed: "hello world\n", want: 0},
		{name: "arrow keys are ignored", feed: "\x1b[A\x1b[B\x1b[C\x1b[D", want: 0},
		{name: "function keys are ignored", feed: "\x1bOP\x1bOQ\x1bOR\x1bOS", want: 0},
		{name: "non-sequence byte confirms escape", feed: "\x1ba", want: 1},
		{name: "repeated escape confirms prior press", feed: "\x1b\x1b", want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var classifier escapeClassifier
			got := 0
			for _, value := range []byte(test.feed) {
				got += classifier.Feed(value)
			}
			if got != test.want {
				t.Fatalf("Feed(%q) confirmed %d escapes, want %d", test.feed, got, test.want)
			}
			if classifier.Timeout() {
				got++
			}
			if test.name == "repeated escape confirms prior press" && got != 2 {
				t.Fatalf("second pending escape timeout left total %d, want 2", got)
			}
		})
	}
}

func TestEscapeClassifierTimeoutConfirmsBareEscapeOnce(t *testing.T) {
	var classifier escapeClassifier
	if classifier.Feed(0x1b) != 0 || !classifier.Pending() {
		t.Fatal("bare Escape was not left pending for the sequence timeout")
	}
	if !classifier.Timeout() || classifier.Pending() {
		t.Fatal("sequence timeout did not confirm and clear the bare Escape")
	}
	if classifier.Timeout() {
		t.Fatal("sequence timeout confirmed the same Escape twice")
	}
}

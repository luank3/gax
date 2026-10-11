package picker

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	"time"
)

type pickerTestBytes struct {
	data []byte
	pos  int
}

func TestSelectedPickerItemsUseThemeAccentAndPositiveRole(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())
	line, _ := renderItem(Item{Label: "model", Current: true}, true, 50, false)
	if !strings.Contains(line, "\x1b[38;5;173m> \x1b[39m") || !strings.Contains(line, "\x1b[38;5;28m* current\x1b[39m") {
		t.Fatalf("picker did not use semantic accent/positive roles: %q", line)
	}
}

func (source *pickerTestBytes) ReadByteTimeout(time.Duration) (byte, bool, error) {
	if source.pos >= len(source.data) {
		return 0, false, io.EOF
	}
	value := source.data[source.pos]
	source.pos++
	return value, true, nil
}

func TestRunFiltersAndReturnsTheOriginalItemIndex(t *testing.T) {
	options := Options{Title: "resume", Items: []Item{{Label: "alpha"}, {Label: "Beta task"}}}
	var output bytes.Buffer
	index, err := Run(options, &pickerTestBytes{data: []byte("BETA\r")}, &output, func() (int, int) {
		return 60, 24
	}, true)
	if err != nil || index != 1 {
		t.Fatalf("picker result = %d, err=%v", index, err)
	}
	for _, want := range []string{"resume", "BETA", "Beta task", "1/2"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("picker frame omits %q: %q", want, output.String())
		}
	}
}

func TestRunSupportsArrowNavigationAndCancellation(t *testing.T) {
	options := Options{Items: []Item{{Label: "alpha"}, {Label: "beta"}}}
	var output bytes.Buffer
	index, err := Run(options, &pickerTestBytes{data: []byte("\x1b[B\r")}, &output, func() (int, int) {
		return 40, 10
	}, false)
	if err != nil || index != 1 {
		t.Fatalf("arrow selection = %d, err=%v", index, err)
	}
	index, err = Run(options, &pickerTestBytes{data: []byte{0x03}}, &output, func() (int, int) {
		return 40, 10
	}, false)
	if err != nil || index != -1 {
		t.Fatalf("cancel result = %d, err=%v", index, err)
	}
}

func TestRunPropagatesOutputErrors(t *testing.T) {
	options := Options{Items: []Item{{Label: "alpha"}}}
	wantErr := errors.New("writer failed")
	index, err := Run(options, &pickerTestBytes{}, errorWriter{err: wantErr}, func() (int, int) {
		return 40, 10
	}, false)
	if index != -1 || !errors.Is(err, wantErr) {
		t.Fatalf("writer error result = %d, %v", index, err)
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

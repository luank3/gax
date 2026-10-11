//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package input

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestReadTTYLineUsesThePlatformTerminalImplementation(t *testing.T) {
	_, _, err := ReadTTYLine(context.Background(), nil, io.Discard, NewCore(), nil, "> ")
	if err == nil || !strings.Contains(err.Error(), "input terminal is unavailable") || strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ReadTTYLine with no input = %v, want terminal input validation from the POSIX implementation", err)
	}
}

func TestInterruptWatchUsesThePlatformTerminalImplementation(t *testing.T) {
	if _, err := StartInterruptWatch(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "terminal input is unavailable") || strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("StartInterruptWatch with no input = %v, want terminal input validation from the POSIX implementation", err)
	}
}

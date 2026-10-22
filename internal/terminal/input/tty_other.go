//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package input

import (
	"context"
	"errors"
	"io"
	"os"
)

// TTYPair reports whether the platform-specific terminal editor can use both streams.
func TTYPair(_ io.Reader, _ io.Writer, _ bool) bool { return false }

// ReadTTYLine is unsupported on platforms without a raw-terminal implementation.
func ReadTTYLine(_ context.Context, _ *os.File, _ io.Writer, _ *Core, _ *HistoryFile, _ string, _ ...EditorOptions) (string, bool, error) {
	return "", false, errors.New("raw terminal input is unsupported on this platform")
}

// InterruptWatch is unavailable without a platform-specific raw terminal implementation.
type InterruptWatch struct{}

func StartInterruptWatch(_ *os.File, _, _ func()) (*InterruptWatch, error) {
	return nil, errors.New("terminal interrupt watching is unsupported on this platform")
}

func (*InterruptWatch) PauseRequested() bool { return false }
func (*InterruptWatch) AbortRequested() bool { return false }
func (*InterruptWatch) Stop()                {}
func (*InterruptWatch) StopResolvePending()  {}

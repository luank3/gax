package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalLive "github.com/luank3/gax/internal/terminal/live"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

// busyOperation scopes a blocking provider action, its optional terminal indicator, and Escape
// cancellation. Non-interactive callers keep the same context behavior without terminal output.
type busyOperation struct {
	ctx       context.Context
	cancel    context.CancelFunc
	watch     *terminalinput.InterruptWatch
	renderer  *terminalLive.Renderer
	stdout    io.Writer
	startErr  error
	ended     bool
	endCancel bool
	endErr    error
}

func startBusyOperation(ctx context.Context, stdin io.Reader, stdinTTY bool, stdout io.Writer, label string) *busyOperation {
	if ctx == nil {
		ctx = context.Background()
	}
	operationContext, cancel := context.WithCancel(ctx)
	operation := &busyOperation{ctx: operationContext, cancel: cancel, stdout: stdout}
	if !terminalinput.TTYPair(stdin, stdout, stdinTTY) {
		return operation
	}
	stdinFile, _ := stdin.(*os.File)
	operation.watch, _ = terminalinput.StartInterruptWatch(stdinFile, nil, cancel)
	width, _ := terminalwidth.WriterColumns(stdout)
	if width <= 0 {
		width = 80
	}
	operation.renderer = terminalLive.NewRenderer(stdout, width, true, false)
	operation.renderer.SetMarkdownEnabled(false)
	if err := operation.renderer.Begin(); err != nil {
		operation.startErr = err
		cancel()
		return operation
	}
	if err := operation.renderer.SetStatusLabel(label); err != nil {
		operation.startErr = err
		cancel()
	}
	return operation
}

func (operation *busyOperation) Context() context.Context {
	if operation == nil || operation.ctx == nil {
		return context.Background()
	}
	return operation.ctx
}

// Tick advances the spinner and lets provider transports observe context or Escape cancellation.
func (operation *busyOperation) Tick() bool {
	if operation == nil {
		return false
	}
	if operation.startErr != nil || operation.ctx.Err() != nil {
		return true
	}
	if operation.watch != nil && operation.watch.AbortRequested() {
		operation.cancel()
		return true
	}
	if operation.renderer != nil {
		if err := operation.renderer.Tick(time.Now()); err != nil {
			operation.startErr = err
			operation.cancel()
			return true
		}
	}
	return false
}

// End restores terminal input, clears the status row, and displays the interrupt marker when the
// user pressed Escape twice. It reports whether the operation was canceled.
func (operation *busyOperation) End() (cancelled bool, err error) {
	if operation == nil {
		return false, nil
	}
	if operation.ended {
		return operation.endCancel, operation.endErr
	}
	operation.ended = true
	if operation.watch != nil {
		operation.watch.StopResolvePending()
		cancelled = operation.watch.AbortRequested()
	}
	cancelled = cancelled || operation.ctx.Err() != nil
	if operation.renderer != nil {
		if closeErr := operation.renderer.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if cancelled && operation.watch != nil && operation.watch.AbortRequested() && err == nil {
			marker := "\x1b[2m[interrupted]\x1b[0m\n"
			if _, writeErr := io.WriteString(operation.stdout, marker); writeErr != nil {
				err = errors.Join(err, writeErr)
			}
		}
	}
	operation.cancel()
	if operation.startErr != nil {
		err = errors.Join(err, operation.startErr)
	}
	operation.endCancel = cancelled
	operation.endErr = err
	return cancelled, err
}

func endBusyOperation(operation *busyOperation, stderr io.Writer) bool {
	cancelled, err := operation.End()
	if err != nil {
		fmt.Fprintf(stderr, "gax: busy indicator: %v\n", err)
	}
	return cancelled
}

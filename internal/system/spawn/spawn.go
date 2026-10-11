// Package spawn provides bounded subprocess capture used by system probes and terminal helpers.
package spawn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

var (
	ErrInvalidArguments = errors.New("invalid subprocess capture arguments")
	ErrOutputTooLarge   = errors.New("subprocess output exceeded its byte limit")
	ErrEmptyOutput      = errors.New("subprocess produced no output")
	ErrTimeout          = errors.New("subprocess timed out")
)

var parentInterruptState struct {
	sync.Mutex
	depth   int
	ignore  func()
	restore func()
}

// SetParentInterruptHandlers connects subprocess handoffs to the application's signal manager.
// The callbacks change the parent's signal dispositions at the outermost suppression boundary.
// The returned cleanup function detaches those callbacks after signal listening stops.
func SetParentInterruptHandlers(ignore, restore func()) func() {
	parentInterruptState.Lock()
	parentInterruptState.ignore = ignore
	parentInterruptState.restore = restore
	if parentInterruptState.depth > 0 && ignore != nil {
		ignore()
	}
	parentInterruptState.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			parentInterruptState.Lock()
			if parentInterruptState.depth > 0 && parentInterruptState.restore != nil {
				parentInterruptState.restore()
			}
			parentInterruptState.ignore = nil
			parentInterruptState.restore = nil
			parentInterruptState.Unlock()
		})
	}
}

// SuppressParentInterrupts marks a subprocess handoff during which the parent must ignore
// terminal interrupts while the child receives them. The returned release function is safe to
// call more than once and scopes may be nested.
func SuppressParentInterrupts() func() {
	parentInterruptState.Lock()
	if parentInterruptState.depth == 0 && parentInterruptState.ignore != nil {
		parentInterruptState.ignore()
	}
	parentInterruptState.depth++
	parentInterruptState.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			parentInterruptState.Lock()
			parentInterruptState.depth--
			if parentInterruptState.depth == 0 && parentInterruptState.restore != nil {
				parentInterruptState.restore()
			}
			parentInterruptState.Unlock()
		})
	}
}

// ParentInterruptsSuppressed reports whether a caller is currently handing terminal control to
// a child process.
func ParentInterruptsSuppressed() bool {
	parentInterruptState.Lock()
	defer parentInterruptState.Unlock()
	return parentInterruptState.depth > 0
}

// CaptureStdout runs argv with stdin and stderr disconnected, and returns bounded nonempty stdout
// only when the command exits successfully before timeout. It also bounds waiting after stdout
// reaches EOF, since a helper can close stdout and continue running.
func CaptureStdout(argv []string, maxBytes int, timeout time.Duration) ([]byte, error) {
	return CaptureStdoutInDir(argv, "", maxBytes, timeout)
}

// CaptureStdoutInDir runs argv from dir and applies CaptureStdout's output and time bounds.
func CaptureStdoutInDir(argv []string, dir string, maxBytes int, timeout time.Duration) ([]byte, error) {
	if !validArguments(argv, maxBytes) || timeout <= 0 {
		return nil, ErrInvalidArguments
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return CaptureStdoutContextInDir(ctx, argv, dir, maxBytes)
}

// CaptureStdoutContext is CaptureStdout with a caller-owned cancellation and deadline.
func CaptureStdoutContext(ctx context.Context, argv []string, maxBytes int) ([]byte, error) {
	return CaptureStdoutContextInDir(ctx, argv, "", maxBytes)
}

// CaptureStdoutContextInDir is CaptureStdoutInDir with caller-owned cancellation and deadline.
func CaptureStdoutContextInDir(ctx context.Context, argv []string, dir string, maxBytes int) ([]byte, error) {
	if ctx == nil || !validArguments(argv, maxBytes) {
		return nil, ErrInvalidArguments
	}
	if err := ctx.Err(); err != nil {
		return nil, contextError(err)
	}
	releaseInterrupts := SuppressParentInterrupts()
	defer releaseInterrupts()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = dir
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}

	type readResult struct {
		output []byte
		err    error
	}
	readDone := make(chan readResult, 1)
	go func() {
		output, readErr := readCapped(stdout, maxBytes)
		readDone <- readResult{output: output, err: readErr}
	}()

	var output []byte
	select {
	case result := <-readDone:
		if result.err != nil {
			_ = stdout.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
			if err := ctx.Err(); err != nil {
				return nil, contextError(err)
			}
			return nil, result.err
		}
		output = result.output
		_ = stdout.Close()
	case <-ctx.Done():
		_ = stdout.Close()
		_ = command.Process.Kill()
		<-readDone
		_ = command.Wait()
		return nil, contextError(ctx.Err())
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			return nil, waitErr
		}
	case <-ctx.Done():
		select {
		case waitErr := <-waitDone:
			if waitErr != nil {
				return nil, waitErr
			}
		default:
			_ = command.Process.Kill()
			<-waitDone
			return nil, contextError(ctx.Err())
		}
	}
	if len(output) == 0 {
		return nil, ErrEmptyOutput
	}
	return output, nil
}

// StartDetached starts argv with all standard streams disconnected and returns without waiting
// for the command to exit. A background waiter reaps the child while the parent remains alive.
func StartDetached(argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return ErrInvalidArguments
	}
	command := exec.Command(argv[0], argv[1:]...)
	releaseInterrupts := SuppressParentInterrupts()
	defer releaseInterrupts()
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func validArguments(argv []string, maxBytes int) bool {
	return len(argv) > 0 && argv[0] != "" && maxBytes >= 0
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return err
}

func readCapped(reader io.Reader, maxBytes int) ([]byte, error) {
	var output bytes.Buffer
	chunk := make([]byte, 64*1024)
	for {
		count, err := reader.Read(chunk)
		if count > 0 {
			if count > maxBytes-output.Len() {
				return nil, ErrOutputTooLarge
			}
			_, _ = output.Write(chunk[:count])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return output.Bytes(), nil
			}
			return nil, err
		}
	}
}

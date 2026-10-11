package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCaptureStdoutReturnsSuccessfulNonemptyOutput(t *testing.T) {
	requirePOSIXShell(t)
	output, err := CaptureStdout([]string{"sh", "-c", "printf hello"}, 5, time.Second)
	if err != nil || string(output) != "hello" {
		t.Fatalf("CaptureStdout() = %q, %v; want hello", output, err)
	}
}

func TestParentInterruptSuppressionCanNestAndReleaseOnce(t *testing.T) {
	if ParentInterruptsSuppressed() {
		t.Fatal("parent interrupt suppression was already active")
	}
	firstRelease := SuppressParentInterrupts()
	secondRelease := SuppressParentInterrupts()
	if !ParentInterruptsSuppressed() {
		t.Fatal("parent interrupts were not suppressed")
	}
	firstRelease()
	if !ParentInterruptsSuppressed() {
		t.Fatal("releasing one nested scope cleared the other")
	}
	firstRelease()
	secondRelease()
	if ParentInterruptsSuppressed() {
		t.Fatal("parent interrupts remained suppressed after release")
	}
}

func TestCaptureStdoutRejectsEmptyAndNonzeroResults(t *testing.T) {
	requirePOSIXShell(t)
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "empty", argv: []string{"sh", "-c", "exit 0"}},
		{name: "nonzero", argv: []string{"sh", "-c", "printf x; exit 1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if output, err := CaptureStdout(test.argv, 32, time.Second); err == nil || output != nil {
				t.Fatalf("CaptureStdout() = %q, %v; want failure", output, err)
			}
		})
	}
}

func TestCaptureStdoutRejectsMissingCommandAndInvalidArguments(t *testing.T) {
	if output, err := CaptureStdout([]string{"gax-no-such-spawn-helper"}, 32, time.Second); err == nil || output != nil {
		t.Fatalf("missing command result = %q, %v; want failure", output, err)
	}
	for _, test := range []struct {
		name    string
		argv    []string
		max     int
		timeout time.Duration
	}{
		{name: "nil argv", argv: nil, max: 32, timeout: time.Second},
		{name: "empty argv", argv: []string{}, max: 32, timeout: time.Second},
		{name: "empty executable", argv: []string{""}, max: 32, timeout: time.Second},
		{name: "negative cap", argv: []string{"true"}, max: -1, timeout: time.Second},
		{name: "zero timeout", argv: []string{"true"}, max: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			if output, err := CaptureStdout(test.argv, test.max, test.timeout); !errors.Is(err, ErrInvalidArguments) || output != nil {
				t.Fatalf("CaptureStdout() = %q, %v; want invalid arguments", output, err)
			}
		})
	}
}

func TestCaptureStdoutRejectsOutputOverCap(t *testing.T) {
	requirePOSIXShell(t)
	output, err := CaptureStdout([]string{"sh", "-c", "printf 012345"}, 5, time.Second)
	if !errors.Is(err, ErrOutputTooLarge) || output != nil {
		t.Fatalf("overflow result = %q, %v; want output-too-large", output, err)
	}
}

func TestCaptureStdoutBoundsRunningAndPostEOFHelpers(t *testing.T) {
	requirePOSIXShell(t)
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "silent helper", argv: []string{"sleep", "30"}},
		{name: "EOF then hang", argv: []string{"sh", "-c", "printf hi; exec 1>&-; exec sleep 30"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			output, err := CaptureStdout(test.argv, 1024, 150*time.Millisecond)
			if !errors.Is(err, ErrTimeout) || output != nil {
				t.Fatalf("timeout result = %q, %v; want timeout", output, err)
			}
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("CaptureStdout() took %s after a 150ms deadline", elapsed)
			}
		})
	}
}

func TestCaptureStdoutContextHonorsCancellation(t *testing.T) {
	requirePOSIXShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	output, err := CaptureStdoutContext(ctx, []string{"sleep", "30"}, 1024)
	if !errors.Is(err, context.Canceled) || output != nil {
		t.Fatalf("cancelled capture = %q, %v; want context cancellation", output, err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("CaptureStdoutContext() took %s after cancellation", elapsed)
	}
}

func TestStartDetachedReturnsBeforeTheCommandExits(t *testing.T) {
	requirePOSIXShell(t)
	marker := filepath.Join(t.TempDir(), "finished")
	t.Setenv("GAX_SPAWN_MARKER", marker)
	started := time.Now()
	if err := StartDetached([]string{"sh", "-c", "sleep 1; : > \"$GAX_SPAWN_MARKER\""}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("StartDetached() waited for command exit: %s", elapsed)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached command did not finish")
}

func requirePOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("upstream process helpers use POSIX commands")
	}
}

//go:build linux

package keepawake

import (
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/system/spawn"
)

func startPlatform() func() {
	inhibit := findExecutable([]string{"/usr/bin/systemd-inhibit", "/bin/systemd-inhibit"})
	sleep := findExecutable([]string{"/usr/bin/sleep", "/bin/sleep"})
	if inhibit == "" || sleep == "" {
		return nil
	}
	args := []string{}
	if supportsNoAskPassword(inhibit) {
		args = append(args, "--no-ask-password")
	}
	args = append(args, "--what=idle", "--mode=block", "--who=gax", "--why=gax is running a turn", sleep, "2147483647")
	command := exec.Command(inhibit, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	return startCommand(command)
}

var noAskPasswordOnce sync.Once
var noAskPasswordSupported bool

func supportsNoAskPassword(path string) bool {
	noAskPasswordOnce.Do(func() {
		_, err := spawn.CaptureStdout([]string{path, "--no-ask-password", "--version"}, 4096, time.Second)
		noAskPasswordSupported = err == nil
	})
	return noAskPasswordSupported
}

func startCommand(command *exec.Cmd) func() {
	if command.Start() != nil {
		return nil
	}
	finished := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(finished)
	}()
	return func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
			_ = command.Process.Kill()
			<-finished
		}
	}
}

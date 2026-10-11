//go:build darwin

package keepawake

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func startPlatform() func() {
	caffeinate := findExecutable([]string{"/usr/bin/caffeinate"})
	if caffeinate == "" {
		return nil
	}
	command := exec.Command(caffeinate, "-i", "-w", strconv.Itoa(syscall.Getpid()))
	return startCommand(command)
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

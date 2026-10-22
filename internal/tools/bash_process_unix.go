//go:build !windows

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

type commandProcess struct{}

func startCommandProcess(cmd *exec.Cmd) (*commandProcess, error) {
	cmd.SysProcAttr = bashProcessAttributes()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &commandProcess{}, nil
}
func (*commandProcess) close() {}
func (*commandProcess) kill(cmd *exec.Cmd, signal syscall.Signal) bool {
	if err := syscall.Kill(-cmd.Process.Pid, signal); err == nil {
		return true
	} else if !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Kill()
	}
	return false
}

func commandSignal(_ *commandTask, err *exec.ExitError) syscall.Signal {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal()
	}
	return 0
}

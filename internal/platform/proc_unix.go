//go:build !windows

package platform

import (
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type unixProcessGroup struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	pgid int
	done bool
}

func startCommand(cmd *exec.Cmd) (ProcessGroup, error) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	configurePlatformSysProcAttr(attr)
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pgid := cmd.Process.Pid
	return &unixProcessGroup{cmd: cmd, pgid: pgid}, nil
}

func (g *unixProcessGroup) Wait() error {
	err := g.cmd.Wait()
	g.mu.Lock()
	g.done = true
	g.mu.Unlock()
	return err
}

func (g *unixProcessGroup) SignalGraceful() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.pgid <= 0 {
		return nil
	}
	err := unix.Kill(-g.pgid, unix.SIGTERM)
	if err == unix.ESRCH {
		return nil
	}
	return err
}

func (g *unixProcessGroup) KillTree() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.pgid <= 0 {
		return nil
	}
	err := unix.Kill(-g.pgid, unix.SIGKILL)
	if err == unix.ESRCH {
		return nil
	}
	return err
}

func (g *unixProcessGroup) Close() error {
	return nil
}

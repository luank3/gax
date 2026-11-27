//go:build linux

package platform

import (
	"os/exec"
	"sync"
)

type cmdKeepAwake struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func inhibitSleep(reason string) KeepAwake {
	p, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil
	}
	if reason == "" {
		reason = "gax turn in progress"
	}
	cmd := exec.Command(p, "--what=idle", "--who=gax", "--why="+reason, "sleep", "3600")
	if err := cmd.Start(); err != nil {
		return nil
	}
	return &cmdKeepAwake{cmd: cmd}
}

func (k *cmdKeepAwake) Stop() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cmd != nil && k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
		_ = k.cmd.Wait()
		k.cmd = nil
	}
}

//go:build darwin

package platform

import (
	"os/exec"
	"sync"
)

type cmdKeepAwake struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func inhibitSleep(_ string) KeepAwake {
	p, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil
	}
	cmd := exec.Command(p, "-i")
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

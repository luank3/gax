//go:build !linux && !windows

package tools

import "syscall"

func bashProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

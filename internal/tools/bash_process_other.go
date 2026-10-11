//go:build !linux

package tools

import "syscall"

func bashProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

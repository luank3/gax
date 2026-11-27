//go:build linux

package platform

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func configurePlatformSysProcAttr(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = unix.SIGKILL
}

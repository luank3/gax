//go:build !windows && !linux

package platform

import "syscall"

func configurePlatformSysProcAttr(_ *syscall.SysProcAttr) {}

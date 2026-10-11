//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package osinfo

import "syscall"

func kernelIdentity() (system, release string, ok bool) {
	system, systemErr := syscall.Sysctl("kern.ostype")
	release, releaseErr := syscall.Sysctl("kern.osrelease")
	if systemErr != nil || releaseErr != nil {
		return "", "", false
	}
	return system, release, true
}

//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package osinfo

import (
	"os/exec"
	"strings"
)

func kernelIdentity() (system, release string, ok bool) {
	systemOutput, err := exec.Command("uname", "-s").Output()
	if err != nil {
		return "", "", false
	}
	releaseOutput, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "", "", false
	}
	return strings.TrimSpace(string(systemOutput)), strings.TrimSpace(string(releaseOutput)), true
}

//go:build !windows

package platform

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func resolveShell(override string) (ShellSpec, error) {
	if strings.TrimSpace(override) != "" {
		p, err := exec.LookPath(override)
		if err != nil {
			return ShellSpec{}, fmt.Errorf("shell %q not found: %w", override, err)
		}
		return buildPOSIXShell(p), nil
	}
	for _, candidate := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(candidate); err == nil {
			return buildPOSIXShell(p), nil
		}
	}
	return ShellSpec{}, fmt.Errorf("no bash or sh found on PATH")
}

func buildPOSIXShell(path string) ShellSpec {
	name := strings.ToLower(filepath.Base(path))
	if name != "bash" {
		name = "sh"
	}
	return ShellSpec{
		Path:    path,
		Name:    name,
		Command: []string{path, "-c"},
	}
}

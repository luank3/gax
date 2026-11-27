//go:build windows

package platform

import (
	"fmt"
	"os"
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
		return buildWindowsShell(p), nil
	}
	if p := findGitBash(); p != "" {
		return buildWindowsShell(p), nil
	}
	for _, candidate := range []string{"bash.exe", "pwsh.exe", "powershell.exe", "cmd.exe"} {
		if p, err := exec.LookPath(candidate); err == nil {
			return buildWindowsShell(p), nil
		}
	}
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		if _, err := os.Stat(comspec); err == nil {
			return buildWindowsShell(comspec), nil
		}
	}
	return ShellSpec{}, fmt.Errorf("no supported shell found (Git Bash, pwsh, powershell, or cmd)")
}

func findGitBash() string {
	if gitPath, err := exec.LookPath("git.exe"); err == nil {
		dir := filepath.Dir(gitPath)
		for _, rel := range []string{`..\bin\bash.exe`, `..\usr\bin\bash.exe`} {
			cand := filepath.Clean(filepath.Join(dir, rel))
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	for _, envVar := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		root := os.Getenv(envVar)
		if root == "" {
			continue
		}
		cand := filepath.Join(root, "Git", "bin", "bash.exe")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

func buildWindowsShell(path string) ShellSpec {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "pwsh"):
		return ShellSpec{Path: path, Name: "pwsh", Command: []string{path, "-NoProfile", "-NonInteractive", "-Command"}}
	case strings.Contains(base, "powershell"):
		return ShellSpec{Path: path, Name: "powershell", Command: []string{path, "-NoProfile", "-NonInteractive", "-Command"}}
	case strings.Contains(base, "cmd"):
		return ShellSpec{Path: path, Name: "cmd", Command: []string{path, "/d", "/s", "/c"}}
	default:
		return ShellSpec{Path: path, Name: "bash", Command: []string{path, "-c"}}
	}
}

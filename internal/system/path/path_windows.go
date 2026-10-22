//go:build windows

package path

import (
	"os"
	"path/filepath"
	"strings"
)

// Home uses the Windows profile when no POSIX-style HOME was configured.
func Home() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

func nativePath(value string) bool {
	return strings.Contains(value, `\`) || filepath.VolumeName(value) != ""
}
func slashes(value string) string { return strings.ReplaceAll(value, `\`, "/") }

func Join(base, suffix string) string {
	if !nativePath(base) {
		return posixJoin(base, suffix)
	}
	return filepath.FromSlash(posixJoin(slashes(base), slashes(suffix)))
}

func ExpandHome(value, home string) string {
	if home != "" && strings.HasPrefix(value, `~\`) {
		return Join(home, value[2:])
	}
	return posixExpandHome(value, home)
}

func CollapseHome(value, home string) string {
	if !nativePath(value) && !nativePath(home) {
		return posixCollapseHome(value, home)
	}
	if home == "" {
		return value
	}
	normalized, prefix := slashes(value), strings.TrimRight(slashes(home), "/")
	if strings.EqualFold(normalized, prefix) {
		return "~"
	}
	if len(normalized) > len(prefix) && strings.EqualFold(normalized[:len(prefix)], prefix) && normalized[len(prefix)] == '/' {
		return "~" + filepath.FromSlash(normalized[len(prefix):])
	}
	return value
}

func Relativize(value, cwd string) (string, bool) {
	if !nativePath(value) && !nativePath(cwd) {
		return posixRelativize(value, cwd)
	}
	if !filepath.IsAbs(value) || !filepath.IsAbs(cwd) || hasParentComponent(slashes(value)) {
		return "", false
	}
	normalized, prefix := slashes(value), strings.TrimRight(slashes(cwd), "/")
	if len(normalized) <= len(prefix) || !strings.EqualFold(normalized[:len(prefix)], prefix) || normalized[len(prefix)] != '/' {
		return "", false
	}
	return filepath.FromSlash(normalized[len(prefix)+1:]), true
}

func ClimbToParent(value string) (string, bool) {
	if !nativePath(value) {
		return posixClimbToParent(value)
	}
	volume := filepath.VolumeName(value)
	if strings.TrimRight(slashes(value), "/") == slashes(volume) {
		return value, false
	}
	parent := Dir(value)
	return parent, parent != value
}

func Dir(value string) string {
	if !nativePath(value) {
		return posixDir(value)
	}
	volume := filepath.VolumeName(value)
	normalized := slashes(value)
	if strings.TrimRight(normalized, "/") == slashes(volume) {
		return filepath.FromSlash(slashes(volume) + "/")
	}
	parent := posixDir(normalized)
	if parent == slashes(volume) {
		parent += "/"
	}
	return filepath.FromSlash(parent)
}

func XDGGaxPath(base, home, homeRelative, relative string) (string, bool) {
	if base != "" && !nativePath(base) {
		return posixXDGGaxPath(base, home, homeRelative, relative)
	}
	if base != "" {
		return Join(Join(base, "gax"), relative), true
	}
	if home == "" {
		home = Home()
	}
	if home == "" {
		return "", false
	}
	return Join(Join(Join(home, homeRelative), "gax"), relative), true
}

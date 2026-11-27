package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// ProjectBucket returns a readable slug + short hash for cwd so sessions are
// scoped per directory and symlink-canonicalized.
func ProjectBucket(cwd string) string {
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil && resolved != "" {
		cwd = resolved
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	base := filepath.Base(cwd)
	var slug strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			slug.WriteRune(r)
		} else {
			slug.WriteByte('-')
		}
	}
	name := strings.Trim(slug.String(), "-")
	if name == "" {
		name = "root"
	}
	sum := sha256.Sum256([]byte(cwd))
	return name + "." + hex.EncodeToString(sum[:4])
}

func resolveHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func resolveDirs() Dirs {
	home := resolveHome()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" && home != "" {
		cfg = filepath.Join(home, ".config")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" && home != "" {
		state = filepath.Join(home, ".local", "state")
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" && home != "" {
		cache = filepath.Join(home, ".cache")
	}
	return Dirs{
		Config: filepath.Join(cfg, "gax"),
		State:  filepath.Join(state, "gax"),
		Cache:  filepath.Join(cache, "gax"),
		Home:   home,
	}
}

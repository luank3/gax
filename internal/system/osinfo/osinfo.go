// Package osinfo reads the host identity used in the model-facing Environment section.
package osinfo

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	systemfs "github.com/luank3/gax/internal/system/fs"
)

const releaseFileCap = 64 * 1024

// ReleaseName returns PRETTY_NAME, falling back to NAME and VERSION. Missing or unreadable
// files and files over 64 KiB have no release name.
func ReleaseName(path string) string {
	data, truncated, err := systemfs.ReadFileCapped(path, releaseFileCap)
	if err != nil || truncated {
		return ""
	}

	values := make(map[string]string, 3)
	for _, line := range strings.Split(string(data), "\n") {
		key, raw, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "PRETTY_NAME", "NAME", "VERSION":
			values[key] = decodeReleaseValue(raw)
		}
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	if values["NAME"] == "" {
		return ""
	}
	if values["VERSION"] == "" {
		return values["NAME"]
	}
	return values["NAME"] + " " + values["VERSION"]
}

// ReleaseNameWithFallback reads fallback only when the primary path is missing. An existing but
// unreadable or unrecognized primary file is authoritative and produces an empty name.
func ReleaseNameWithFallback(primaryPath, fallbackPath string) string {
	if _, err := os.Stat(primaryPath); err == nil {
		return ReleaseName(primaryPath)
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return ""
	}
	return ReleaseName(fallbackPath)
}

func decodeReleaseValue(value string) string {
	value = strings.TrimRight(value, "\r \t")
	value = strings.TrimLeft(value, " \t")
	quote := byte(0)
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		quote = value[0]
		value = value[1 : len(value)-1]
	}
	var decoded strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && quote != '\'' && index+1 < len(value) {
			index++
		}
		decoded.WriteByte(value[index])
	}
	return decoded.String()
}

// Description returns the distribution and kernel identity shown in the Environment section.
func Description() string {
	system, release, ok := kernelIdentity()
	if !ok {
		return "unknown"
	}
	if system == "Linux" {
		if distribution := ReleaseNameWithFallback("/etc/os-release", "/usr/lib/os-release"); distribution != "" {
			return fmt.Sprintf("%s (Linux %s)", distribution, release)
		}
	}
	if system == "Darwin" {
		return fmt.Sprintf("macOS (Darwin %s)", release)
	}
	return fmt.Sprintf("%s %s", system, release)
}

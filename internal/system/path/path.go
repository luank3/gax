// Package path implements POSIX path rules without filepath.Clean normalization.
package path

import (
	"fmt"
	"strings"
)

// Join appends suffix to base, trimming boundary slashes while preserving inner components such as
// "missing/../target" exactly as the upstream path_join helper does.
func Join(base, suffix string) string {
	baseLength := len(base)
	for baseLength > 1 && base[baseLength-1] == '/' {
		baseLength--
	}
	for len(suffix) > 0 && suffix[0] == '/' {
		suffix = suffix[1:]
	}
	baseIsRoot := baseLength == 1 && base[0] == '/'
	if baseIsRoot {
		return base[:baseLength] + suffix
	}
	return base[:baseLength] + "/" + suffix
}

// ExpandHome expands only a bare tilde or the ~/ prefix. Named-user forms remain literal.
func ExpandHome(value, home string) string {
	if value == "" || value[0] != '~' || len(value) > 1 && value[1] != '/' {
		return value
	}
	if home == "" {
		return value
	}
	if value == "~" {
		return home
	}
	return Join(home, value[2:])
}

// CollapseHome replaces an exact HOME path or descendant with a tilde path.
func CollapseHome(value, home string) string {
	if home == "" {
		return value
	}
	homeLength := len(home)
	for homeLength > 1 && home[homeLength-1] == '/' {
		homeLength--
	}
	if homeLength == 1 && home[0] == '/' {
		if value == "" || value[0] != '/' {
			return value
		}
		if value == "/" {
			return "~"
		}
		return "~" + value
	}
	if len(value) < homeLength || value[:homeLength] != home[:homeLength] {
		return value
	}
	if len(value) == homeLength {
		return "~"
	}
	if value[homeLength] != '/' {
		return value
	}
	return "~" + value[homeLength:]
}

// Relativize returns path relative to cwd only when path is an absolute descendant without any
// parent components. The cwd itself and unrelated paths are rejected.
func Relativize(value, cwd string) (string, bool) {
	if value == "" || cwd == "" || value[0] != '/' || cwd[0] != '/' || hasParentComponent(value) {
		return "", false
	}
	cwdLength := len(cwd)
	for cwdLength > 1 && cwd[cwdLength-1] == '/' {
		cwdLength--
	}
	var relative string
	if cwdLength == 1 && cwd[0] == '/' {
		relative = value[1:]
	} else {
		if len(value) <= cwdLength || value[:cwdLength] != cwd[:cwdLength] || value[cwdLength] != '/' {
			return "", false
		}
		relative = value[cwdLength+1:]
	}
	for len(relative) > 0 && relative[0] == '/' {
		relative = relative[1:]
	}
	if relative == "" {
		return "", false
	}
	return relative, true
}

func hasParentComponent(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

// ClimbToParent returns the directory's parent and whether it moved. The root remains unchanged.
func ClimbToParent(directory string) (string, bool) {
	slash := strings.LastIndexByte(directory, '/')
	if slash < 0 {
		return directory, false
	}
	if slash == 0 {
		if len(directory) == 1 {
			return directory, false
		}
		return "/", true
	}
	return directory[:slash], true
}

// Dir returns a POSIX-style parent path without normalizing dot components.
func Dir(value string) string {
	if value == "" {
		return "."
	}
	end := len(value)
	for end > 1 && value[end-1] == '/' {
		end--
	}
	value = value[:end]
	slash := strings.LastIndexByte(value, '/')
	if slash < 0 {
		return "."
	}
	if slash == 0 {
		return "/"
	}
	parentEnd := slash
	for parentEnd > 1 && value[parentEnd-1] == '/' {
		parentEnd--
	}
	return value[:parentEnd]
}

// XDGGaxPath mirrors the upstream XDG path construction and reports false when neither the XDG
// base nor HOME can resolve a location.
func XDGGaxPath(xdgBase, home, homeRelative, relative string) (string, bool) {
	if xdgBase != "" {
		return fmt.Sprintf("%s/gax/%s", xdgBase, relative), true
	}
	if home == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s/gax/%s", home, homeRelative, relative), true
}

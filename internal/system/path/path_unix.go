//go:build !windows

package path

import "os"

// Home returns the configured home directory.
func Home() string                                { return os.Getenv("HOME") }
func Join(base, suffix string) string             { return posixJoin(base, suffix) }
func ExpandHome(value, home string) string        { return posixExpandHome(value, home) }
func CollapseHome(value, home string) string      { return posixCollapseHome(value, home) }
func Relativize(value, cwd string) (string, bool) { return posixRelativize(value, cwd) }
func ClimbToParent(value string) (string, bool)   { return posixClimbToParent(value) }
func Dir(value string) string                     { return posixDir(value) }
func XDGGaxPath(base, home, homeRelative, relative string) (string, bool) {
	return posixXDGGaxPath(base, home, homeRelative, relative)
}

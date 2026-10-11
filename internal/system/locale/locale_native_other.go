//go:build !cgo || (!linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly)

package locale

func nativeProcessLocale([]string) (State, bool) { return State{}, false }

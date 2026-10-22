//go:build windows

package locale

// Go's console IO is Unicode; Git Bash children use its available C.UTF-8 locale.
func nativeProcessLocale(environment []string) (State, bool) {
	all, _ := environmentValue(environment, "LC_ALL")
	return State{UTF8: true, ChildrenUTF8: all == "", ChildCType: "C.UTF-8"}, true
}

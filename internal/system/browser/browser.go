// Package browser launches a URL handler without tying it to the terminal session.
package browser

import (
	"runtime"

	"github.com/luank3/gax/internal/system/spawn"
)

// OpenURL asks the platform's URL handler to open value. The handler runs independently so a
// browser that stays open cannot keep gax attached to it.
func OpenURL(value string) {
	name, args := openerCommand(runtime.GOOS, value)
	_ = spawn.StartDetached(append([]string{name}, args...))
}

func openerCommand(goos, value string) (string, []string) {
	if goos == "windows" {
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", value}
	}
	if goos == "darwin" {
		return "open", []string{value}
	}
	return "xdg-open", []string{value}
}

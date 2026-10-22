//go:build windows

package app

import (
	"io"
	"os"

	"github.com/luank3/gax/internal/system/console"
)

func prepareTerminalOutput(writers ...io.Writer) func() {
	var modes []*console.Mode
	for _, writer := range writers {
		file, ok := writer.(*os.File)
		if !ok || !console.IsTerminal(file) {
			continue
		}
		if mode, err := console.Output(file); err == nil {
			modes = append(modes, mode)
		}
	}
	return func() {
		for index := len(modes) - 1; index >= 0; index-- {
			modes[index].Restore()
		}
	}
}

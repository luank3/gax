//go:build windows

package picker

import (
	"io"
	"os"

	"github.com/luank3/gax/internal/system/console"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
)

// RunTTY opens the picker on native consoles, restoring both modes before returning.
func RunTTY(options Options, stdin *os.File, stdout io.Writer) (int, bool, error) {
	if !terminalinput.TTYPair(stdin, stdout, true) {
		return -1, false, nil
	}
	if len(options.Items) == 0 {
		index, err := Run(options, nil, stdout, nil, terminalocale.HaveUTF8())
		return index, false, err
	}
	out := stdout.(*os.File)
	mode, err := console.Raw(stdin, out, false)
	if err != nil {
		return -1, true, err
	}
	defer mode.Restore()
	index, err := Run(options, &console.Source{File: stdin}, stdout, func() (int, int) {
		columns, rows, ok := console.Size(out)
		if !ok {
			return 80, 24
		}
		return columns, rows
	}, terminalocale.HaveUTF8())
	return index, true, err
}

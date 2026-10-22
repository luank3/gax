//go:build windows

package width

import (
	"io"
	"os"

	"github.com/luank3/gax/internal/system/console"
)

// WriterColumns reports the physical width of an attached Windows console.
func WriterColumns(writer io.Writer) (int, bool) {
	file, ok := writer.(*os.File)
	if !ok {
		return 0, false
	}
	columns, _, ok := console.Size(file)
	if ok && columns <= 0 {
		columns = fallbackWidth
	}
	return columns, ok
}

// TerminalColumns returns stdout's width, or the standard fallback for redirected output.
func TerminalColumns() int {
	if columns, ok := WriterColumns(os.Stdout); ok {
		return columns
	}
	return fallbackWidth
}

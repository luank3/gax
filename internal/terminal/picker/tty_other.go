//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package picker

import (
	"io"
	"os"
)

func RunTTY(_ Options, _ *os.File, _ io.Writer) (int, bool, error) { return -1, false, nil }

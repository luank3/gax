//go:build !windows

package app

import "io"

func prepareTerminalOutput(...io.Writer) func() { return func() {} }

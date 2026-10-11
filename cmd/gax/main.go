package main

import (
	"os"

	"github.com/luank3/gax/internal/app"
)

func main() {
	info, err := os.Stdin.Stat()
	stdinTTY := err == nil && info.Mode()&os.ModeCharDevice != 0
	os.Exit(app.Run(os.Args[1:], os.Stdin, stdinTTY, os.Stdout, os.Stderr))
}

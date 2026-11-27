// Command gax is the CLI entrypoint for the gax coding agent.
package main

import (
	"fmt"
	"os"

	"github.com/luank3/gax/internal/core"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	for _, arg := range args {
		switch arg {
		case "-v", "--version":
			fmt.Printf("gax %s\n", version)
			return 0
		case "-h", "--help":
			fmt.Println("gax - minimalist terminal-native coding agent")
			fmt.Printf("outcome types: %s\n", core.OutcomeComplete)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "gax: interactive mode not yet wired (Slice 1 scaffold)")
	return 1
}

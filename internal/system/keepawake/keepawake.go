// Package keepawake provides a best-effort system sleep inhibitor for active agent turns.
package keepawake

import (
	"os"
)

// Run brackets an agent turn with an optional inhibitor. A missing helper never blocks the turn.
func Run(enabled bool, acquire func() func(), turn func()) {
	if enabled && acquire != nil {
		if release := acquire(); release != nil {
			defer release()
		}
	}
	if turn != nil {
		turn()
	}
}

// Start starts the platform sleep-inhibition helper, when one is available.
func Start() func() { return startPlatform() }

func findExecutable(candidates []string) string {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate
		}
	}
	return ""
}

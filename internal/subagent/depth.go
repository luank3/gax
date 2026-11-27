// Package subagent enforces the recursion depth limit for nested gax -p runs.
package subagent

import (
	"fmt"
	"strconv"
	"strings"
)

const MaxDepth = 3

// CheckDepth rejects a nested gax invocation at MaxDepth (failing closed on malformed values).
func CheckDepth(value string) error {
	if value == "" {
		return nil
	}
	depth, ok := parseDepth(value)
	if !ok || depth < 0 {
		depth = MaxDepth
	}
	if depth < MaxDepth {
		return nil
	}
	return fmt.Errorf("subagent depth limit (%d) reached — run the task directly instead of spawning another gax", MaxDepth)
}

// NextChildDepth returns the depth stamped onto child shell processes.
func NextChildDepth(value string) int {
	depth := 0
	if value != "" {
		parsed, ok := parseDepth(value)
		if !ok || parsed < 0 {
			depth = MaxDepth
		} else {
			depth = parsed
		}
	}
	if depth < MaxDepth {
		depth++
	}
	return depth
}

func parseDepth(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}

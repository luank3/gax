// Package subagent contains the recursion boundary shared by nested gax runs and shell children.
package subagent

import (
	"fmt"
	"strconv"
	"strings"
)

const MaxDepth = 3

// CheckDepth rejects a nested gax invocation at the limit. A malformed or negative inherited
// depth fails closed at MaxDepth, matching the command-line guard in gax.
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

// NextChildDepth stamps a shell child with its ancestry depth. Malformed values fail closed at
// MaxDepth so that a bad inherited marker cannot permit unbounded nested gax processes.
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
	value = strings.TrimLeft(value, " \t\n\r\v\f")
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}

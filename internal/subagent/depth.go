// Package subagent enforces the recursion depth limit for nested gax -p runs.
package subagent

import (
	"fmt"
	"sort"
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

// ChildEnv returns a copy of base environment with GAX_SUBAGENT_DEPTH incremented
// and non-empty overrides applied (removing stale duplicates).
func ChildEnv(base []string, currentDepth string, overrides map[string]string) []string {
	skip := make(map[string]bool, len(overrides)+1)
	skip["GAX_SUBAGENT_DEPTH"] = true
	for k, v := range overrides {
		if v != "" {
			skip[k] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides)+1)
	for _, entry := range base {
		k, _, ok := strings.Cut(entry, "=")
		if ok && skip[k] {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, fmt.Sprintf("GAX_SUBAGENT_DEPTH=%d", NextChildDepth(currentDepth)))
	keys := make([]string, 0, len(overrides))
	for k, v := range overrides {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
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

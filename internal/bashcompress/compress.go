package bashcompress

import (
	"strings"
)

// Caps shared by the filters, mirroring rtk's src/core/truncate.rs.
const (
	capWarnings  = 10 // warnings and test failures
	capList      = 20 // flat lists: one line per item
	capInventory = 50 // inventories: exhaustive lookups
)

const (
	// detailLineWidth is the width one failure detail line is truncated to: wider than a search
	// result's, because a stack frame's file:line and the compared values both have to fit.
	detailLineWidth = 200
	// failureDetailBudget bounds the detail lines one failure contributes. Passing tests collapse
	// to a count; a failure keeps its locations, its outcome, and the line after each location.
	failureDetailBudget = 6
)

// Result is a compact form of a command's output.
type Result struct {
	Text string
	// Omitted is true when the compact form leaves out lines the raw output holds — capped lists,
	// truncated hunks, collapsed passing tests, trimmed comments. The caller keeps the full output
	// recoverable in that case; a pure reformatting leaves it false.
	Omitted bool
}

// filter compacts one command family's output. It reports false when the output does not have the
// shape the filter understands, in which case the caller keeps the raw bytes.
type filter func(invocation Invocation, output string, exitCode int) (Result, bool)

// filters maps a program name to its filter.
var filters = map[string]filter{
	"ls":             filterLs,
	"tree":           filterTree,
	"cat":            filterCat,
	"head":           filterCat,
	"tail":           filterCat,
	"bat":            filterCat,
	"grep":           filterSearch,
	"egrep":          filterSearch,
	"fgrep":          filterSearch,
	"rg":             filterSearch,
	"ripgrep":        filterSearch,
	"ast-grep":       filterAstGrep,
	"sg":             filterAstGrep,
	"git":            filterGit,
	"cargo":          filterCargo,
	"go":             filterGo,
	"pytest":         filterPytest,
	"python":         filterPytest,
	"python3":        filterPytest,
	"py.test":        filterPytest,
	"npm":            filterNodeTest,
	"pnpm":           filterNodeTest,
	"yarn":           filterNodeTest,
	"bun":            filterNodeTest,
	"npx":            filterNodeTest,
	"jest":           filterNodeTest,
	"vitest":         filterNodeTest,
	"mocha":          filterNodeTest,
	"ruff":           filterRuff,
	"sqlfluff":       filterSqlfluff,
	"docker":         filterDocker,
	"docker-compose": filterDocker,
}

// Compress returns the compact form of a command's captured output. It reports false when the
// command is not a single simple command, no filter matches, the filter does not recognize the
// output, or the compact form would not be smaller than the raw bytes.
func Compress(command, output string, exitCode int) (Result, bool) {
	if output == "" {
		return Result{}, false
	}
	invocation, ok := Parse(command)
	if !ok {
		return Result{}, false
	}
	compact, ok := filters[invocation.Program]
	if !ok {
		return Result{}, false
	}
	result, ok := compact(invocation, output, exitCode)
	if !ok || result.Text == "" {
		return Result{}, false
	}
	// Never worse than raw: a filter that would grow the output is not used (rtk's never_worse).
	if len(result.Text) >= len(output) {
		return Result{}, false
	}
	return result, true
}

// truncateLine caps a line's length the way rtk's truncate_line does, marking the cut with "...".
func truncateLine(line string, width int) string {
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// compactPath shortens a path to its last two components when it is long, so a grouped listing
// stays readable without losing the file name.
func compactPath(path string) string {
	if len(path) <= 50 {
		return path
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return truncateLine(path, 50)
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}

// collapseBlankRuns replaces runs of two or more blank lines with one, as rtk's minimal filter
// does after stripping comments.
func collapseBlankRuns(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// trimTrailingBlankLines removes blank lines from the end of a compact form.
func trimTrailingBlankLines(text string) string {
	return strings.TrimRight(text, "\n")
}

// locationLine reports whether a line carries a `file.ext:line` reference, in any of the shapes
// test runners and compilers emit: `x_test.go:12:`, `panicked at src/utils.rs:18:`,
// `--> src/main.rs:5:9`, `File "/tmp/test_y.py", line 12`, `at /app/x.test.ts:12:5`.
func locationLine(line string) bool {
	for index := 0; index+1 < len(line); index++ {
		if line[index] != '.' || !isExtensionStart(line[index+1]) {
			continue
		}
		end := index + 1
		for end < len(line) && isWordByte(line[end]) {
			end++
		}
		for end < len(line) && (line[end] == ':' || line[end] == ',') {
			if line[end] == ',' {
				// Python's `", line 12"` form.
				rest := strings.TrimSpace(line[end+1:])
				if !strings.HasPrefix(rest, "line ") {
					break
				}
				rest = strings.TrimPrefix(rest, "line ")
				digits := 0
				for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
					digits++
				}
				if digits > 0 {
					return true
				}
				break
			}
			end++
			digits := 0
			for end+digits < len(line) && line[end+digits] >= '0' && line[end+digits] <= '9' {
				digits++
			}
			if digits > 0 {
				return true
			}
			break
		}
	}
	return false
}

// isExtensionStart reports whether a byte can start a file extension.
func isExtensionStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

// isWordByte reports whether a byte can appear in a file extension or name.
func isWordByte(character byte) bool {
	return isExtensionStart(character) || character >= '0' && character <= '9' || character == '_' || character == '-'
}

// failureDetailLine reports whether a line carries a failure's answer: an assertion outcome, a
// panic, an exception, or the values a test compared.
func failureDetailLine(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{"panic:", "error", "expected", "got", "want", "actual",
		"assert", "mismatch", "unexpected", "failed", "exception", "traceback"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// selectFailureLines keeps the lines of one failure block that must survive compression: file:line
// references, the line after each, and the lines that state what went wrong. It returns the kept
// lines and how many relevant lines the budget held back, so the caller can say what it dropped.
func selectFailureLines(lines []string, skip func(string) bool, budget, width int) ([]string, int) {
	kept := make([]string, 0, budget)
	dropped := 0
	keepNext := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || skip != nil && skip(trimmed) {
			keepNext = false
			continue
		}
		location := locationLine(trimmed)
		if !location && !keepNext && !failureDetailLine(trimmed) {
			continue
		}
		keepNext = location
		if len(kept) >= budget {
			dropped++
			continue
		}
		kept = append(kept, truncateLine(trimmed, width))
	}
	return kept, dropped
}

// splitLines splits output into lines without a trailing empty element.
func splitLines(output string) []string {
	lines := strings.Split(output, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

package bashcompress

import (
	"fmt"
	"strings"
)

const (
	capList      = 20
	capInventory = 50
	capWarnings  = 10
)

type filterFunc func(inv Invocation, output string, exitCode int) (Result, bool)

var filters = map[string]filterFunc{
	"ls":             filterList(capInventory),
	"tree":           filterList(capInventory),
	"find":           filterList(capInventory),
	"fd":             filterList(capInventory),
	"du":             filterList(capInventory),
	"df":             filterList(capInventory),
	"ps":             filterList(capInventory),
	"cat":            filterCat,
	"head":           filterCat,
	"tail":           filterCat,
	"bat":            filterCat,
	"grep":           filterSearch,
	"egrep":          filterSearch,
	"fgrep":          filterSearch,
	"rg":             filterSearch,
	"ripgrep":        filterSearch,
	"ast-grep":       filterSearch,
	"sg":             filterSearch,
	"git":            filterGit,
	"cargo":          filterTestRunner("FAIL", "FAILED", "error["),
	"go":             filterGo,
	"pytest":         filterTestRunner("FAILED", "ERROR", "E "),
	"python":         filterTestRunner("FAILED", "ERROR", "Traceback"),
	"python3":        filterTestRunner("FAILED", "ERROR", "Traceback"),
	"npm":            filterTestRunner("FAIL", "Error:", "ERR!"),
	"pnpm":           filterTestRunner("FAIL", "Error:", "ERR!"),
	"yarn":           filterTestRunner("FAIL", "Error:", "ERR!"),
	"bun":            filterTestRunner("FAIL", "error:", "(fail)"),
	"npx":            filterTestRunner("FAIL", "Error:"),
	"jest":           filterTestRunner("FAIL ", "● "),
	"vitest":         filterTestRunner("FAIL ", "❯ "),
	"mocha":          filterTestRunner("failing", "Error:"),
	"make":           filterBuild,
	"cmake":          filterBuild,
	"ninja":          filterBuild,
	"pip":            filterPip,
	"pip3":           filterPip,
	"uv":             filterPip,
	"ruff":           filterLinter,
	"sqlfluff":       filterLinter,
	"tsc":            filterLinter,
	"eslint":         filterLinter,
	"golangci-lint":  filterLinter,
	"mypy":           filterLinter,
	"pyright":        filterLinter,
	"biome":          filterLinter,
	"curl":           filterCurl,
	"wget":           filterCurl,
	"kubectl":        filterKubectl,
	"docker":         filterDocker,
	"docker-compose": filterDocker,
}

// Compress returns a compacted version of output when a filter matches and
// produces strictly fewer bytes than output (never-worse invariant).
func Compress(command, output string, exitCode int) (Result, bool) {
	if output == "" {
		return Result{}, false
	}
	inv, ok := Parse(command)
	if !ok {
		return Result{}, false
	}
	fn, ok := filters[inv.Program]
	if !ok {
		return Result{}, false
	}
	res, ok := fn(inv, output, exitCode)
	if !ok || res.Text == "" || len(res.Text) >= len(output) {
		return Result{}, false
	}
	return res, true
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func filterList(capLines int) filterFunc {
	return func(_ Invocation, output string, exitCode int) (Result, bool) {
		if exitCode != 0 {
			return Result{}, false
		}
		lines := splitNonEmpty(output)
		if len(lines) <= capLines {
			return Result{}, false
		}
		var b strings.Builder
		for _, l := range lines[:capLines] {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "... (+%d more entries)", len(lines)-capLines)
		return Result{Text: b.String(), Omitted: true}, true
	}
}

func filterCat(_ Invocation, output string, exitCode int) (Result, bool) {
	if exitCode != 0 {
		return Result{}, false
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) <= 60 {
		return Result{}, false
	}
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "... (%d lines omitted)\n", len(lines)-50)
	for i := len(lines) - 20; i < len(lines); i++ {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return Result{Text: strings.TrimRight(b.String(), "\n"), Omitted: true}, true
}

func filterSearch(_ Invocation, output string, exitCode int) (Result, bool) {
	if exitCode > 1 {
		return Result{}, false
	}
	lines := splitNonEmpty(output)
	if len(lines) <= capList {
		return Result{}, false
	}
	var b strings.Builder
	for _, l := range lines[:capList] {
		if len(l) > 160 {
			l = l[:157] + "..."
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "... (%d more matches)", len(lines)-capList)
	return Result{Text: b.String(), Omitted: true}, true
}

func filterGit(inv Invocation, output string, exitCode int) (Result, bool) {
	if len(inv.Args) == 0 || exitCode != 0 {
		return Result{}, false
	}
	sub := inv.Args[0]
	lines := splitNonEmpty(output)
	switch sub {
	case "status", "log", "add", "commit", "push", "branch", "tag", "stash":
		if len(lines) <= capList {
			return Result{}, false
		}
		var b strings.Builder
		for _, l := range lines[:capList] {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "... (%d more lines)", len(lines)-capList)
		return Result{Text: b.String(), Omitted: true}, true
	case "diff", "show":
		if len(lines) <= 80 {
			return Result{}, false
		}
		var b strings.Builder
		kept := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "diff --git") || strings.HasPrefix(l, "@@") ||
				strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
				if kept < 60 {
					b.WriteString(l)
					b.WriteByte('\n')
					kept++
				}
			}
		}
		fmt.Fprintf(&b, "... (diff compacted; %d total non-empty lines)", len(lines))
		return Result{Text: b.String(), Omitted: kept < len(lines)}, true
	}
	return Result{}, false
}

func filterGo(inv Invocation, output string, exitCode int) (Result, bool) {
	if len(inv.Args) == 0 {
		return Result{}, false
	}
	switch inv.Args[0] {
	case "test":
		return filterTestRunner("--- FAIL:", "FAIL\t", "panic:")(inv, output, exitCode)
	case "build", "vet":
		return filterLinter(inv, output, exitCode)
	}
	return Result{}, false
}

func filterTestRunner(failMarkers ...string) filterFunc {
	return func(_ Invocation, output string, exitCode int) (Result, bool) {
		lines := splitNonEmpty(output)
		if len(lines) <= capList {
			return Result{}, false
		}
		var kept []string
		for _, l := range lines {
			for _, m := range failMarkers {
				if strings.Contains(l, m) {
					kept = append(kept, l)
					break
				}
			}
		}
		if exitCode == 0 && len(kept) == 0 {
			return Result{
				Text:    fmt.Sprintf("all passed (%d lines; last: %s)", len(lines), lines[len(lines)-1]),
				Omitted: true,
			}, true
		}
		if len(kept) > 0 {
			if len(kept) > capWarnings*3 {
				kept = kept[:capWarnings*3]
			}
			return Result{
				Text:    strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d lines omitted)", len(lines)-len(kept)),
				Omitted: true,
			}, true
		}
		return Result{}, false
	}
}

func filterLinter(_ Invocation, output string, _ int) (Result, bool) {
	lines := splitNonEmpty(output)
	if len(lines) <= capWarnings {
		return Result{}, false
	}
	return Result{
		Text:    strings.Join(lines[:capWarnings], "\n") + fmt.Sprintf("\n... (%d more diagnostics)", len(lines)-capWarnings),
		Omitted: true,
	}, true
}

func filterDocker(inv Invocation, output string, exitCode int) (Result, bool) {
	if len(inv.Args) == 0 {
		return Result{}, false
	}
	switch inv.Args[0] {
	case "ps", "images":
		return filterList(capList)(inv, output, exitCode)
	case "build":
		return filterBuild(inv, output, exitCode)
	case "logs":
		return filterCat(inv, output, exitCode)
	}
	return Result{}, false
}

func filterBuild(_ Invocation, output string, exitCode int) (Result, bool) {
	lines := splitNonEmpty(output)
	if len(lines) <= capList {
		return Result{}, false
	}
	var kept []string
	for _, l := range lines {
		low := strings.ToLower(l)
		if strings.Contains(low, "error") || strings.Contains(low, "warning:") ||
			strings.Contains(l, "***") || strings.Contains(l, "FAILED") {
			kept = append(kept, l)
		}
	}
	if exitCode == 0 && len(kept) == 0 {
		return Result{
			Text:    fmt.Sprintf("build succeeded (%d lines; last: %s)", len(lines), lines[len(lines)-1]),
			Omitted: true,
		}, true
	}
	if len(kept) > 0 {
		if len(kept) > capWarnings*2 {
			kept = kept[:capWarnings*2]
		}
		return Result{
			Text:    strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d build lines omitted)", len(lines)-len(kept)),
			Omitted: true,
		}, true
	}
	return Result{}, false
}

func filterPip(_ Invocation, output string, exitCode int) (Result, bool) {
	if exitCode != 0 {
		return Result{}, false
	}
	lines := splitNonEmpty(output)
	if len(lines) <= capList {
		return Result{}, false
	}
	var kept []string
	for _, l := range lines {
		if strings.HasPrefix(l, "Collecting ") || strings.HasPrefix(l, "  Downloading ") ||
			strings.HasPrefix(l, "  Using cached ") || strings.HasPrefix(l, "Requirement already satisfied:") {
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) == 0 {
		return Result{Text: fmt.Sprintf("pip completed (%d lines omitted)", len(lines)), Omitted: true}, true
	}
	if len(kept) > capList {
		kept = kept[len(kept)-capList:]
	}
	return Result{
		Text:    strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d download/cache lines omitted)", len(lines)-len(kept)),
		Omitted: true,
	}, true
}

func filterCurl(_ Invocation, output string, _ int) (Result, bool) {
	lines := strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n")
	var kept []string
	stripped := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "% Total") || strings.HasPrefix(t, "Dload  Upload") ||
			(strings.Contains(t, "%") && (strings.Contains(t, "--:--:--") || strings.Contains(t, "###"))) {
			stripped++
			continue
		}
		if t != "" {
			kept = append(kept, l)
		}
	}
	if stripped == 0 {
		return Result{}, false
	}
	return Result{Text: strings.Join(kept, "\n"), Omitted: true}, true
}

func filterKubectl(inv Invocation, output string, exitCode int) (Result, bool) {
	if exitCode != 0 || len(inv.Args) == 0 {
		return Result{}, false
	}
	switch inv.Args[0] {
	case "get":
		return filterList(capInventory)(inv, output, exitCode)
	case "logs", "describe":
		return filterCat(inv, output, exitCode)
	}
	return Result{}, false
}

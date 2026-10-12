package bashcompress

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// cargo test
// ---------------------------------------------------------------------------

// cargoSummary is one parsed `test result:` line.
type cargoSummary struct {
	passed      int
	failed      int
	ignored     int
	filteredOut int
	duration    string
}

// filterCargo compacts `cargo test` output: passing tests collapse to a count and the failure
// blocks stay (rtk's filter_cargo_test). Only the `test` subcommand is filtered.
func filterCargo(invocation Invocation, output string, _ int) (Result, bool) {
	if invocation.subcommand() != "test" {
		return Result{}, false
	}
	failures := make([]string, 0, 8)
	summaries := make([]cargoSummary, 0, 8)
	summaryLines := make([]string, 0, 8)
	current := make([]string, 0, 8)
	inFailures := false
	inFailureNames := false
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Compiling") || strings.HasPrefix(trimmed, "Downloading") ||
			strings.HasPrefix(trimmed, "Downloaded") || strings.HasPrefix(trimmed, "Finished") {
			continue
		}
		if strings.HasPrefix(line, "running ") || strings.HasPrefix(line, "test ") && strings.HasSuffix(line, "... ok") {
			continue
		}
		if trimmed == "failures:" {
			// The second `failures:` header opens the list of failing test names, which the
			// failure blocks above already carry.
			if inFailures {
				inFailureNames = true
			} else {
				inFailures = true
			}
			continue
		}
		if inFailureNames {
			if strings.HasPrefix(line, "test result:") {
				inFailureNames = false
				summaryLines = append(summaryLines, line)
				if summary, ok := parseCargoSummary(line); ok {
					summaries = append(summaries, summary)
				}
			}
			continue
		}
		if inFailures {
			switch {
			case strings.HasPrefix(line, "test result:"):
				inFailures = false
				summaryLines = append(summaryLines, line)
				if summary, ok := parseCargoSummary(line); ok {
					summaries = append(summaries, summary)
				}
				continue
			case strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "---- "):
				current = append(current, line)
				continue
			case trimmed == "":
				if len(current) > 0 {
					failures = append(failures, strings.Join(current, "\n"))
					current = current[:0]
				}
				continue
			default:
				current = append(current, line)
				continue
			}
		}
		if strings.HasPrefix(line, "test result:") {
			summaryLines = append(summaryLines, line)
			if summary, ok := parseCargoSummary(line); ok {
				summaries = append(summaries, summary)
			}
		}
	}
	if len(current) > 0 {
		failures = append(failures, strings.Join(current, "\n"))
	}
	if len(failures) == 0 && len(summaries) == 0 {
		return Result{}, false
	}
	if len(failures) == 0 {
		text := cargoCompactSummary(summaries)
		if text == "" {
			return Result{}, false
		}
		return Result{Text: text, Omitted: true}, true
	}
	var body strings.Builder
	fmt.Fprintf(&body, "FAILURES (%d):\n", len(failures))
	shown := failures
	if len(shown) > capWarnings {
		shown = shown[:capWarnings]
	}
	for index, failure := range shown {
		lines := strings.Split(failure, "\n")
		fmt.Fprintf(&body, "%d. %s\n", index+1, cargoFailureName(lines))
		kept, dropped := cargoFailureDetail(lines)
		for _, line := range kept {
			fmt.Fprintf(&body, "     %s\n", line)
		}
		if dropped > 0 {
			fmt.Fprintf(&body, "     … (%d more lines)\n", dropped)
		}
	}
	if len(failures) > capWarnings {
		fmt.Fprintf(&body, "\n… +%d more failures\n", len(failures)-capWarnings)
	}
	for _, line := range summaryLines {
		body.WriteString(line + "\n")
	}
	return Result{Text: trimTrailingBlankLines(body.String()), Omitted: true}, true
}

// cargoFailureName names a failure block from its `---- test stdout ----` header, falling back to
// its first line.
func cargoFailureName(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "---- ") {
			name := strings.TrimPrefix(trimmed, "---- ")
			return strings.TrimSuffix(strings.TrimSuffix(name, " stdout ----"), " stderr ----")
		}
	}
	if len(lines) > 0 {
		return truncateLine(strings.TrimSpace(lines[0]), 120)
	}
	return "failure"
}

// cargoFailureDetail keeps a failure block's head: libtest prints the panic location, the failed
// assertion, and the compared values first, and anything after that is a backtrace or a note. The
// block is kept verbatim rather than filtered, because a `left:`/`right:` line has no marker of
// its own, and the count says how much of it the budget held back.
func cargoFailureDetail(lines []string) ([]string, int) {
	kept := make([]string, 0, failureDetailBudget)
	dropped := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "---- ") {
			continue
		}
		if len(kept) >= failureDetailBudget {
			dropped++
			continue
		}
		kept = append(kept, truncateLine(trimmed, detailLineWidth))
	}
	return kept, dropped
}

// parseCargoSummary parses `test result: ok. 15 passed; 0 failed; 0 ignored; 0 measured; 0 filtered
// out; finished in 0.01s`.
func parseCargoSummary(line string) (cargoSummary, bool) {
	if !strings.HasPrefix(line, "test result: ok.") {
		return cargoSummary{}, false
	}
	summary := cargoSummary{}
	for _, field := range strings.Split(strings.TrimPrefix(line, "test result: ok."), ";") {
		words := strings.Fields(strings.TrimSpace(field))
		if len(words) < 2 {
			continue
		}
		value := atoi(words[0])
		switch {
		case strings.HasPrefix(words[1], "passed"):
			summary.passed = value
		case strings.HasPrefix(words[1], "failed"):
			summary.failed = value
		case strings.HasPrefix(words[1], "ignored"):
			summary.ignored = value
		case strings.HasPrefix(words[1], "filtered"):
			summary.filteredOut = value
		case strings.HasPrefix(words[0], "finished"):
			summary.duration = words[len(words)-1]
		}
	}
	return summary, true
}

// cargoCompactSummary renders rtk's `cargo test: N passed (...) (S suites)` line.
func cargoCompactSummary(summaries []cargoSummary) string {
	passed, ignored, filtered := 0, 0, 0
	seconds := 0.0
	hasDuration := true
	for _, summary := range summaries {
		passed += summary.passed
		ignored += summary.ignored
		filtered += summary.filteredOut
		if summary.duration == "" {
			hasDuration = false
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSuffix(summary.duration, "s"), 64)
		if err != nil {
			hasDuration = false
			continue
		}
		seconds += value
	}
	if passed == 0 && ignored == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("%d passed", passed)}
	if ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignored", ignored))
	}
	if filtered > 0 {
		parts = append(parts, fmt.Sprintf("%d filtered out", filtered))
	}
	suites := "1 suite"
	if len(summaries) > 1 {
		suites = fmt.Sprintf("%d suites", len(summaries))
	}
	text := fmt.Sprintf("cargo test: %s (%s", strings.Join(parts, ", "), suites)
	if hasDuration {
		text += fmt.Sprintf(", %.2fs", seconds)
	}
	return text + ")"
}

// ---------------------------------------------------------------------------
// go test
// ---------------------------------------------------------------------------

// goTestEvent is one `go test -json` record.
type goTestEvent struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	Test        string `json:"Test"`
	Output      string `json:"Output"`
	FailedBuild string `json:"FailedBuild"`
	ImportPath  string `json:"ImportPath"`
}

// goPackage collects one package's results.
type goPackage struct {
	pass          int
	fail          int
	skip          int
	buildFailed   bool
	buildErrors   []string
	packageFailed bool
	packageOutput []string
	failedTests   []string
	failedOutput  map[string][]string
}

// filterGo compacts `go test` output: JSON records are parsed, the text form is read for package
// results and failure blocks, and passing tests collapse to a count (rtk's filter_go_test_json).
func filterGo(invocation Invocation, output string, _ int) (Result, bool) {
	if invocation.subcommand() != "test" {
		return Result{}, false
	}
	if strings.Contains(output, `"Action"`) {
		if text, ok := filterGoJSON(output); ok {
			return Result{Text: text, Omitted: true}, true
		}
	}
	return filterGoText(output)
}

// filterGoJSON parses NDJSON test records.
func filterGoJSON(output string) (string, bool) {
	packages := make(map[string]*goPackage)
	order := make([]string, 0, 8)
	buildOutput := make(map[string][]string)
	events := 0
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var event goTestEvent
		if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
			continue
		}
		events++
		switch event.Action {
		case "build-output":
			if event.ImportPath != "" && strings.TrimSpace(event.Output) != "" {
				buildOutput[event.ImportPath] = append(buildOutput[event.ImportPath], strings.TrimRight(event.Output, "\n"))
			}
			continue
		case "build-fail":
			continue
		}
		if event.Package == "" {
			continue
		}
		pkg, seen := packages[event.Package]
		if !seen {
			pkg = &goPackage{failedOutput: make(map[string][]string)}
			packages[event.Package] = pkg
			order = append(order, event.Package)
		}
		switch event.Action {
		case "pass":
			if event.Test != "" {
				pkg.pass++
			}
		case "skip":
			if event.Test != "" {
				pkg.skip++
			}
		case "fail":
			switch {
			case event.Test != "":
				pkg.fail++
				pkg.failedTests = append(pkg.failedTests, event.Test)
			case event.FailedBuild != "":
				pkg.buildFailed = true
				pkg.buildErrors = buildOutput[event.FailedBuild]
			default:
				pkg.packageFailed = true
			}
		case "output":
			if event.Test != "" {
				pkg.failedOutput[event.Test] = append(pkg.failedOutput[event.Test], strings.TrimRight(event.Output, "\n"))
				continue
			}
			if trimmed := strings.TrimSpace(event.Output); trimmed != "" {
				pkg.packageOutput = append(pkg.packageOutput, trimmed)
			}
		}
	}
	if events == 0 {
		return "", false
	}
	return formatGoSummary(packages, order)
}

// filterGoText reads the plain `go test` text form.
func filterGoText(output string) (Result, bool) {
	packages := make(map[string]*goPackage)
	order := make([]string, 0, 8)
	pendingTests := make([]string, 0, 8)
	pendingOutput := make(map[string][]string)
	currentTest := ""
	currentPackage := ""
	inBuildErrors := false
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "=== RUN"), strings.HasPrefix(line, "=== PAUSE"),
			strings.HasPrefix(line, "=== CONT"):
			continue
		case strings.HasPrefix(line, "--- FAIL: "):
			currentTest = testName(line)
			pendingTests = append(pendingTests, currentTest)
			continue
		case strings.HasPrefix(line, "--- PASS: "), strings.HasPrefix(line, "--- SKIP: "):
			currentTest = ""
			continue
		case trimmed == "FAIL", trimmed == "PASS", trimmed == "ok":
			// The package summary's own trailing line carries no package name.
			currentTest = ""
		case strings.HasPrefix(line, "ok  "), strings.HasPrefix(line, "ok\t"),
			strings.HasPrefix(line, "FAIL\t"), strings.HasPrefix(line, "FAIL "):
			name := packageField(line)
			if name == "" {
				continue
			}
			pkg := goPackageFor(packages, &order, name)
			if strings.HasPrefix(line, "ok") {
				pkg.pass++
			} else {
				pkg.fail += len(pendingTests)
				for _, test := range pendingTests {
					pkg.failedTests = append(pkg.failedTests, test)
					pkg.failedOutput[test] = pendingOutput[test]
				}
				if len(pendingTests) == 0 && !pkg.buildFailed {
					pkg.packageFailed = true
				}
			}
			pendingTests = pendingTests[:0]
			pendingOutput = make(map[string][]string)
			currentTest = ""
			currentPackage = ""
			inBuildErrors = false
		case strings.HasPrefix(trimmed, "# "):
			currentPackage = strings.TrimPrefix(trimmed, "# ")
			pkg := goPackageFor(packages, &order, currentPackage)
			pkg.buildFailed = true
			inBuildErrors = true
		case strings.HasPrefix(line, "?   "), strings.HasPrefix(line, "?\t"):
			continue
		default:
			if inBuildErrors && currentPackage != "" {
				pkg := goPackageFor(packages, &order, currentPackage)
				pkg.buildErrors = append(pkg.buildErrors, line)
				continue
			}
			if currentTest != "" && trimmed != "" {
				pendingOutput[currentTest] = append(pendingOutput[currentTest], line)
			}
		}
	}
	if len(packages) == 0 {
		return Result{}, false
	}
	text, ok := formatGoSummary(packages, order)
	if !ok {
		return Result{}, false
	}
	return Result{Text: text, Omitted: true}, true
}

// testName returns the test name of a `--- FAIL: Name (0.00s)` line.
func testName(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return ""
	}
	return strings.TrimSuffix(fields[2], ":")
}

// goPackageFor returns a package's accumulator, remembering insertion order.
func goPackageFor(packages map[string]*goPackage, order *[]string, name string) *goPackage {
	if pkg, ok := packages[name]; ok {
		return pkg
	}
	pkg := &goPackage{failedOutput: make(map[string][]string)}
	packages[name] = pkg
	*order = append(*order, name)
	return pkg
}

// packageField returns the package name of an `ok`/`FAIL`/`--- FAIL` line.
func packageField(line string) string {
	fields := strings.Fields(line)
	switch {
	case strings.HasPrefix(line, "--- FAIL"):
		// `--- FAIL: TestName (0.00s)`
		name := strings.TrimPrefix(fields[1], "FAIL:")
		return strings.TrimSuffix(name, ":")
	case len(fields) >= 2:
		return fields[1]
	default:
		return ""
	}
}

// formatGoSummary renders rtk's summary and per-package failure blocks.
func formatGoSummary(packages map[string]*goPackage, order []string) (string, bool) {
	passed, failed, skipped, buildFailed, packageFailed := 0, 0, 0, 0, 0
	for _, pkg := range packages {
		passed += pkg.pass
		failed += pkg.fail
		skipped += pkg.skip
		if pkg.buildFailed {
			buildFailed++
		}
		if pkg.packageFailed && pkg.fail == 0 && !pkg.buildFailed {
			packageFailed++
		}
	}
	hasFailures := failed > 0 || buildFailed > 0 || packageFailed > 0
	if !hasFailures && passed == 0 {
		return "Go test: No tests found", true
	}
	if !hasFailures {
		return fmt.Sprintf("Go test: %d passed in %d packages", passed, len(packages)), true
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Go test: %d passed, %d failed", passed, failed+buildFailed+packageFailed)
	if skipped > 0 {
		fmt.Fprintf(&body, ", %d skipped", skipped)
	}
	fmt.Fprintf(&body, " in %d packages\n", len(packages))
	sorted := append([]string(nil), order...)
	sort.Strings(sorted)
	for _, name := range sorted {
		pkg := packages[name]
		if pkg.packageFailed && pkg.fail == 0 && !pkg.buildFailed {
			fmt.Fprintf(&body, "\n%s [FAIL]\n", compactPackageName(name))
			for _, line := range pkg.packageOutput {
				fmt.Fprintf(&body, "  %s\n", truncateLine(line, 120))
			}
		}
		if pkg.buildFailed {
			fmt.Fprintf(&body, "\n%s [build failed]\n", compactPackageName(name))
			for _, line := range pkg.buildErrors {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				fmt.Fprintf(&body, "  %s\n", truncateLine(trimmed, 120))
			}
		}
		if pkg.fail == 0 {
			continue
		}
		fmt.Fprintf(&body, "\n%s (%d passed, %d failed)\n", compactPackageName(name), pkg.pass, pkg.fail)
		seen := make(map[string]bool, len(pkg.failedTests))
		for _, test := range pkg.failedTests {
			if seen[test] {
				// A subtest failure repeats its parent's name; the output is already printed.
				continue
			}
			seen[test] = true
			fmt.Fprintf(&body, "  [FAIL] %s\n", test)
			kept, dropped := selectFailureLines(pkg.failedOutput[test], goFailureHeader, failureDetailBudget, detailLineWidth)
			for _, line := range kept {
				fmt.Fprintf(&body, "     %s\n", line)
			}
			if dropped > 0 {
				fmt.Fprintf(&body, "     … (%d more lines)\n", dropped)
			}
		}
	}
	return trimTrailingBlankLines(body.String()), true
}

// goFailureHeader drops the lines the summary already carries.
func goFailureHeader(line string) bool {
	return strings.HasPrefix(line, "=== RUN") || strings.HasPrefix(line, "--- FAIL") ||
		strings.HasPrefix(line, "--- PASS") || strings.HasPrefix(line, "=== PAUSE") ||
		strings.HasPrefix(line, "=== CONT")
}

// compactPackageName keeps a package's last path component, as rtk does.
func compactPackageName(name string) string {
	if index := strings.LastIndex(name, "/"); index >= 0 {
		return name[index+1:]
	}
	return name
}

// ---------------------------------------------------------------------------
// pytest
// ---------------------------------------------------------------------------

// filterPytest compacts pytest output: the summary line plus the failure sections (rtk's
// filter_pytest).
func filterPytest(invocation Invocation, output string, _ int) (Result, bool) {
	counts, duration, ok := parsePytestSummary(output)
	if !ok {
		return Result{}, false
	}
	if counts.passed == 0 && counts.failed == 0 && counts.skipped == 0 && counts.xfailed == 0 && counts.xpassed == 0 {
		return Result{}, false
	}
	var text strings.Builder
	if counts.failed == 0 && counts.passed > 0 && counts.skipped == 0 && counts.xfailed == 0 && counts.xpassed == 0 {
		fmt.Fprintf(&text, "Pytest: %d passed", counts.passed)
		if duration != "" {
			fmt.Fprintf(&text, " in %s", duration)
		}
		return Result{Text: text.String(), Omitted: true}, true
	}
	fmt.Fprintf(&text, "Pytest: %d passed, %d failed", counts.passed, counts.failed)
	if counts.skipped > 0 {
		fmt.Fprintf(&text, ", %d skipped", counts.skipped)
	}
	if counts.xfailed > 0 {
		fmt.Fprintf(&text, ", %d xfailed", counts.xfailed)
	}
	if counts.xpassed > 0 {
		fmt.Fprintf(&text, ", %d xpassed", counts.xpassed)
	}
	if duration != "" {
		fmt.Fprintf(&text, " in %s", duration)
	}
	failures := parsePytestFailures(output)
	if len(failures) > 0 {
		text.WriteString("\n\nFailures:\n")
		shown := failures
		if len(shown) > capWarnings {
			shown = shown[:capWarnings]
		}
		for index, failure := range shown {
			fmt.Fprintf(&text, "%d. [FAIL] %s\n", index+1, failure.name)
			kept, dropped := selectFailureLines(failure.lines, nil, failureDetailBudget, detailLineWidth)
			for _, line := range kept {
				fmt.Fprintf(&text, "     %s\n", line)
			}
			if dropped > 0 {
				fmt.Fprintf(&text, "     … (%d more lines)\n", dropped)
			}
			if index < len(shown)-1 {
				text.WriteString("\n")
			}
		}
		if len(failures) > capWarnings {
			fmt.Fprintf(&text, "\n… +%d more failures\n", len(failures)-capWarnings)
		}
	}
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: true}, true
}

// pytestCounts is pytest's summary tally.
type pytestCounts struct {
	passed, failed, skipped, xfailed, xpassed, errors int
}

// pytestFailureLines keeps every line of a failure section: the selector decides what to show.
// Sections are capped by collecting a generous window rather than by filtering here, so a
// traceback's frames reach the selector.
const pytestFailureLines = 400

// pytestFailure is one failure section.
type pytestFailure struct {
	name  string
	lines []string
}

// parsePytestSummary reads pytest's final `=== 4 passed, 1 failed in 0.5s ===` line.
func parsePytestSummary(output string) (pytestCounts, string, bool) {
	counts := pytestCounts{}
	found := false
	duration := ""
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "=") || !strings.HasSuffix(trimmed, "=") {
			continue
		}
		body := strings.Trim(trimmed, "= ")
		if !strings.Contains(body, "passed") && !strings.Contains(body, "failed") &&
			!strings.Contains(body, "error") && !strings.Contains(body, "no tests ran") {
			continue
		}
		found = true
		counts, duration, _ = parsePytestTally(body)
	}
	if !found {
		// `pytest -q` prints the same tally without the `===` decoration, as the run's last line.
		if quiet, quietDuration, ok := parsePytestQuietSummary(output); ok {
			return quiet, quietDuration, true
		}
		return counts, "", false
	}
	if counts.errors > 0 {
		counts.failed += counts.errors
	}
	return counts, duration, true
}

// parsePytestTally reads `<n> passed, <n> failed, ... in <duration>` into counts.
func parsePytestTally(body string) (pytestCounts, string, bool) {
	counts := pytestCounts{}
	duration := ""
	parsed := false
	for _, part := range strings.Split(body, ",") {
		words := strings.Fields(strings.TrimSpace(part))
		for index, word := range words {
			if index == 0 {
				continue
			}
			value := 0
			if allDigits(words[index-1]) {
				value = atoi(words[index-1])
			}
			switch word {
			case "passed":
				counts.passed = value
				parsed = true
			case "failed":
				counts.failed = value
				parsed = true
			case "skipped":
				counts.skipped = value
				parsed = true
			case "xfailed":
				counts.xfailed = value
				parsed = true
			case "xpassed":
				counts.xpassed = value
				parsed = true
			case "error", "errors":
				counts.errors = value
				parsed = true
			case "in":
				duration = words[len(words)-1]
			}
		}
	}
	return counts, duration, parsed
}

// parsePytestQuietSummary reads the undecorated tally a quiet pytest run ends with.
func parsePytestQuietSummary(output string) (pytestCounts, string, bool) {
	lines := splitLines(output)
	checked := 0
	for index := len(lines) - 1; index >= 0 && checked < 3; index-- {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" {
			continue
		}
		checked++
		if !strings.Contains(trimmed, "passed") && !strings.Contains(trimmed, "failed") &&
			!strings.Contains(trimmed, "error") {
			continue
		}
		if !allDigits(strings.Fields(trimmed)[0]) {
			continue
		}
		counts, duration, ok := parsePytestTally(trimmed)
		if ok {
			return counts, duration, true
		}
	}
	return pytestCounts{}, "", false
}

// parsePytestFailures collects the `___ test ___` sections and the short summary lines.
func parsePytestFailures(output string) []pytestFailure {
	failures := make([]pytestFailure, 0, 8)
	summaryLines := make([]pytestFailure, 0, 8)
	current := pytestFailure{}
	inSection := false
	flush := func() {
		if current.name != "" {
			failures = append(failures, current)
		}
		current = pytestFailure{}
	}
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "___") && strings.HasSuffix(trimmed, "_") && len(trimmed) > 6 {
			flush()
			current = pytestFailure{name: strings.Trim(trimmed, "_ ")}
			inSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "=====") {
			flush()
			inSection = false
			continue
		}
		if strings.HasPrefix(trimmed, "FAILED ") || strings.HasPrefix(trimmed, "ERROR ") {
			name := strings.TrimPrefix(strings.TrimPrefix(trimmed, "FAILED "), "ERROR ")
			message := ""
			if index := strings.Index(name, " - "); index >= 0 {
				message, name = name[index+3:], name[:index]
			}
			summaryLines = append(summaryLines, pytestFailure{name: name, lines: compactNonEmpty(message)})
			continue
		}
		if !inSection {
			continue
		}
		if len(current.lines) < pytestFailureLines {
			current.lines = append(current.lines, trimmed)
		}
	}
	flush()
	if len(failures) == 0 {
		// The short summary is the only failure detail pytest printed.
		return summaryLines
	}
	return failures
}

// compactNonEmpty wraps a non-empty message as a single failure line.
func compactNonEmpty(message string) []string {
	if message == "" {
		return nil
	}
	return []string{message}
}

// ---------------------------------------------------------------------------
// npm test / jest / vitest
// ---------------------------------------------------------------------------

// filterNodeTest strips npm's boilerplate from any npm-family run and collapses passing tests
// in jest/vitest output (rtk's filter_npm_output plus its test-runner summaries).
func filterNodeTest(invocation Invocation, output string, _ int) (Result, bool) {
	lines, stripped := stripNodeBoilerplate(output)
	if len(lines) == 0 {
		if stripped == 0 {
			return Result{}, false
		}
		return Result{Text: "ok"}, true
	}
	if !runsNodeTests(invocation) {
		if stripped == 0 {
			return Result{}, false
		}
		return Result{Text: strings.Join(lines, "\n")}, true
	}
	kept, collapsed := collapseNodeTests(lines)
	if collapsed == 0 {
		if stripped == 0 {
			return Result{}, false
		}
		return Result{Text: strings.Join(lines, "\n")}, true
	}
	var text strings.Builder
	for _, line := range kept {
		text.WriteString(line + "\n")
	}
	fmt.Fprintf(&text, "… %d passing test lines collapsed", collapsed)
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: true}, true
}

// stripNodeBoilerplate drops npm's script banner, warnings, notices, progress bars, and blank
// lines, returning the kept lines and how many were dropped.
func stripNodeBoilerplate(output string) ([]string, int) {
	lines := make([]string, 0, len(output)/8)
	dropped := 0
	banner := false
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			dropped++
		case strings.HasPrefix(trimmed, "> ") && strings.Contains(trimmed, "@"):
			banner = true
			dropped++
		case strings.HasPrefix(trimmed, "> ") && banner:
			// The command echo npm prints under its package banner.
			dropped++
		case strings.HasPrefix(trimmed, "npm WARN"), strings.HasPrefix(trimmed, "npm notice"):
			dropped++
		case strings.Contains(trimmed, "⸨"), strings.Contains(trimmed, "⸩"):
			dropped++
		default:
			banner = false
			lines = append(lines, line)
		}
	}
	return lines, dropped
}

// runsNodeTests reports whether an npm/pnpm/yarn/bun/npx invocation runs a test suite.
func runsNodeTests(invocation Invocation) bool {
	switch invocation.Program {
	case "jest", "vitest", "mocha":
		return true
	case "npx", "bunx":
		switch invocation.firstPositional() {
		case "jest", "vitest", "mocha":
			return true
		}
		return false
	}
	switch invocation.firstPositional() {
	case "test", "t":
		return true
	case "run":
		switch secondPositional(invocation) {
		case "test", "jest", "vitest":
			return true
		}
	}
	return false
}

// secondPositional returns the positional argument after the first.
func secondPositional(invocation Invocation) string {
	seen := false
	for _, arg := range invocation.Args {
		if arg == "--" {
			return ""
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if seen {
			return arg
		}
		seen = true
	}
	return ""
}

// collapseNodeTests drops passing-test lines, keeping summary and failure lines.
func collapseNodeTests(lines []string) ([]string, int) {
	kept := make([]string, 0, len(lines))
	collapsed := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case isNodeSummaryLine(trimmed):
			kept = append(kept, line)
		case strings.HasPrefix(trimmed, "FAIL"), strings.HasPrefix(trimmed, "●"), strings.HasPrefix(trimmed, "✕"):
			kept = append(kept, line)
		case strings.HasPrefix(trimmed, "PASS"):
			collapsed++
		case strings.HasPrefix(trimmed, "✓"), strings.HasPrefix(trimmed, "√"):
			collapsed++
		case strings.HasPrefix(trimmed, "at "), strings.HasPrefix(trimmed, "Expected"), strings.HasPrefix(trimmed, "Received"):
			kept = append(kept, line)
		default:
			kept = append(kept, line)
		}
	}
	return kept, collapsed
}

// isNodeSummaryLine reports whether a line is a jest/vitest summary row.
func isNodeSummaryLine(line string) bool {
	for _, prefix := range []string{"Tests:", "Test Files", "Test Suites:", "Snapshots:", "Time:",
		"Tests ", "Test Files ", "Ran all test suites"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ruff check
// ---------------------------------------------------------------------------

// ruffViolation is one parsed ruff diagnostic.
type ruffViolation struct {
	file    string
	row     int
	column  int
	code    string
	message string
}

// filterRuff groups `ruff check` diagnostics by rule and file (rtk's ruff report), reading the
// default text output rather than the JSON rtk injects.
func filterRuff(invocation Invocation, output string, _ int) (Result, bool) {
	if invocation.subcommand() != "check" {
		return Result{}, false
	}
	violations := make([]ruffViolation, 0, 32)
	fixable := 0
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Found ") || strings.HasPrefix(trimmed, "[*]") {
			if strings.Contains(trimmed, "fixable") {
				fields := strings.Fields(trimmed)
				if len(fields) > 1 {
					count := strings.TrimPrefix(fields[1], "[*]")
					if allDigits(count) {
						fixable = atoi(count)
					}
				}
			}
			continue
		}
		violation, ok := parseRuffLine(trimmed)
		if !ok {
			return Result{}, false
		}
		violations = append(violations, violation)
	}
	if len(violations) == 0 {
		return Result{}, false
	}
	byRule := make(map[string]int)
	byFile := make(map[string]int)
	files := make([]string, 0, 8)
	for _, violation := range violations {
		byRule[violation.code]++
		if _, seen := byFile[violation.file]; !seen {
			files = append(files, violation.file)
		}
		byFile[violation.file]++
	}
	sort.Slice(files, func(left, right int) bool {
		if byFile[files[left]] != byFile[files[right]] {
			return byFile[files[left]] > byFile[files[right]]
		}
		return files[left] < files[right]
	})
	var text strings.Builder
	fmt.Fprintf(&text, "Ruff: %d issues in %d files", len(violations), len(files))
	if fixable > 0 {
		fmt.Fprintf(&text, " (%d fixable)", fixable)
	}
	text.WriteString("\nTop rules:\n")
	for _, rule := range sortByCount(byRule, capWarnings) {
		fmt.Fprintf(&text, "  %s (%dx)\n", rule.name, rule.count)
	}
	text.WriteString("\nTop files:\n")
	for _, file := range files[:min(len(files), capWarnings)] {
		fmt.Fprintf(&text, "  %s (%d issues)\n", compactPath(file), byFile[file])
		for _, rule := range sortByCount(rulesOfFile(violations, file), 3) {
			fmt.Fprintf(&text, "    %s (%d)\n", rule.name, rule.count)
		}
	}
	if len(files) > capWarnings {
		fmt.Fprintf(&text, "\n... +%d more files\n", len(files)-capWarnings)
	}
	text.WriteString("\nViolations:\n")
	shown := violations
	if len(shown) > capWarnings {
		shown = shown[:capWarnings]
	}
	for _, violation := range shown {
		fmt.Fprintf(&text, "  %s:%d:%d %s %s\n", compactPath(violation.file), violation.row, violation.column,
			violation.code, truncateLine(violation.message, 100))
	}
	if len(violations) > capWarnings {
		fmt.Fprintf(&text, "  … +%d more\n", len(violations)-capWarnings)
	}
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: true}, true
}

// parseRuffLine parses `path:row:col: CODE message`.
func parseRuffLine(line string) (ruffViolation, bool) {
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 4 {
		return ruffViolation{}, false
	}
	if !allDigits(strings.TrimSpace(parts[1])) || !allDigits(strings.TrimSpace(parts[2])) {
		return ruffViolation{}, false
	}
	rest := strings.TrimSpace(parts[3])
	code, message, _ := strings.Cut(rest, " ")
	if code == "" {
		return ruffViolation{}, false
	}
	return ruffViolation{
		file:    parts[0],
		row:     atoi(strings.TrimSpace(parts[1])),
		column:  atoi(strings.TrimSpace(parts[2])),
		code:    code,
		message: strings.TrimSpace(message),
	}, true
}

// countEntry is one name/count pair of a grouped report.
type countEntry struct {
	name  string
	count int
}

// sortByCount orders a count map by descending count, then name, and caps the result.
func sortByCount(counts map[string]int, limit int) []countEntry {
	entries := make([]countEntry, 0, len(counts))
	for name, count := range counts {
		entries = append(entries, countEntry{name: name, count: count})
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].count != entries[right].count {
			return entries[left].count > entries[right].count
		}
		return entries[left].name < entries[right].name
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// rulesOfFile counts the rules a file's violations break.
func rulesOfFile(violations []ruffViolation, file string) map[string]int {
	counts := make(map[string]int)
	for _, violation := range violations {
		if violation.file == file {
			counts[violation.code]++
		}
	}
	return counts
}

// ---------------------------------------------------------------------------
// sqlfluff lint
// ---------------------------------------------------------------------------

// sqlfluffViolation is one parsed sqlfluff lint finding.
type sqlfluffViolation struct {
	file    string
	line    int
	column  int
	code    string
	message string
}

// filterSqlfluff groups `sqlfluff lint` findings by rule and file (rtk's report), reading the
// default text output.
func filterSqlfluff(invocation Invocation, output string, _ int) (Result, bool) {
	if invocation.subcommand() != "lint" {
		return Result{}, false
	}
	violations := make([]sqlfluffViolation, 0, 32)
	file := ""
	for _, line := range splitLines(output) {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "== ["):
			file = sqlfluffFile(trimmed)
			continue
		case strings.HasPrefix(trimmed, "L:"):
			if file == "" {
				return Result{}, false
			}
			violation, ok := parseSqlfluffLine(file, trimmed)
			if !ok {
				continue
			}
			violations = append(violations, violation)
			continue
		default:
			// Header, warning, and rule-documentation lines carry no finding.
			continue
		}
	}
	if len(violations) == 0 {
		return Result{}, false
	}
	byRule := make(map[string]int)
	byFile := make(map[string]int)
	files := make([]string, 0, 8)
	firstAt := make(map[string]string)
	for _, violation := range violations {
		byRule[violation.code]++
		if _, seen := byFile[violation.file]; !seen {
			files = append(files, violation.file)
		}
		byFile[violation.file]++
		if _, seen := firstAt[violation.code]; !seen {
			firstAt[violation.code] = fmt.Sprintf("%s:%d", compactPath(violation.file), violation.line)
		}
	}
	sort.Slice(files, func(left, right int) bool {
		if byFile[files[left]] != byFile[files[right]] {
			return byFile[files[left]] > byFile[files[right]]
		}
		return files[left] < files[right]
	})
	var text strings.Builder
	fmt.Fprintf(&text, "SQLFluff: %d violations in %d files\n", len(violations), len(files))
	text.WriteString("Top rules:\n")
	for _, rule := range sortByCount(byRule, capWarnings) {
		fmt.Fprintf(&text, "  %s (%dx, first at %s)\n", rule.name, rule.count, firstAt[rule.name])
	}
	text.WriteString("\nTop files:\n")
	for _, name := range files[:min(len(files), capWarnings)] {
		fmt.Fprintf(&text, "  %s (%d violations)\n", compactPath(name), byFile[name])
		for _, rule := range sortByCount(sqlfluffRulesOfFile(violations, name), 3) {
			fmt.Fprintf(&text, "    %s (%d)\n", rule.name, rule.count)
		}
	}
	if len(files) > capWarnings {
		fmt.Fprintf(&text, "\n... +%d more files\n", len(files)-capWarnings)
	}
	text.WriteString("\nViolations:\n")
	shown := violations
	if len(shown) > capWarnings {
		shown = shown[:capWarnings]
	}
	for _, violation := range shown {
		fmt.Fprintf(&text, "  %s:%d:%d %s %s\n", compactPath(violation.file), violation.line, violation.column,
			violation.code, truncateLine(violation.message, 100))
	}
	if len(violations) > capWarnings {
		fmt.Fprintf(&text, "  … +%d more\n", len(violations)-capWarnings)
	}
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: true}, true
}

// sqlfluffFile extracts the path from a `== [path] FAIL` header.
func sqlfluffFile(line string) string {
	open := strings.Index(line, "[")
	closing := strings.Index(line, "]")
	if open < 0 || closing <= open {
		return ""
	}
	return line[open+1 : closing]
}

// parseSqlfluffLine parses `L:   1 | P:   1 | LT01 | message`.
func parseSqlfluffLine(file, line string) (sqlfluffViolation, bool) {
	fields := strings.Split(line, "|")
	if len(fields) < 3 {
		return sqlfluffViolation{}, false
	}
	violation := sqlfluffViolation{file: file}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		switch {
		case strings.HasPrefix(field, "L:"):
			value := strings.TrimSpace(strings.TrimPrefix(field, "L:"))
			if allDigits(value) {
				violation.line = atoi(value)
			}
		case strings.HasPrefix(field, "P:"):
			value := strings.TrimSpace(strings.TrimPrefix(field, "P:"))
			if allDigits(value) {
				violation.column = atoi(value)
			}
		case violation.code == "" && field != "" && !strings.Contains(field, " "):
			violation.code = field
		case violation.message == "":
			violation.message = field
		}
	}
	if violation.code == "" {
		return sqlfluffViolation{}, false
	}
	return violation, true
}

// sqlfluffRulesOfFile counts the rules a file's findings break.
func sqlfluffRulesOfFile(violations []sqlfluffViolation, file string) map[string]int {
	counts := make(map[string]int)
	for _, violation := range violations {
		if violation.file == file {
			counts[violation.code]++
		}
	}
	return counts
}

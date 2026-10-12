package bashcompress

import (
	"fmt"
	"path"
	"strings"
)

// noiseDirectories are the trees rtk drops from listings: build products, caches, and editor
// state that answer no question an agent asked.
var noiseDirectories = []string{
	"node_modules", ".git", "target", "__pycache__", ".next", "dist", "build", ".cache",
	".turbo", ".vercel", ".pytest_cache", ".mypy_cache", ".tox", ".venv", "venv", "env",
	"coverage", ".nyc_output", ".DS_Store", "Thumbs.db", ".idea", ".vscode", ".vs",
}

func isNoiseDirectory(name string) bool {
	for _, noise := range noiseDirectories {
		if name == noise {
			return true
		}
	}
	return strings.HasSuffix(name, ".egg-info") || name == ".eggs"
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

// lsEntry is one parsed long-format listing line.
type lsEntry struct {
	name    string
	size    string
	octal   string
	isDir   bool
	isNoise bool
}

// filterLs compacts a long-format listing into one line per entry: directories first as `name/`,
// then files as `name  size`, each with its octal mode (rtk's compact_ls).
func filterLs(invocation Invocation, output string, _ int) (Result, bool) {
	if invocation.hasFlag('R', "recursive") {
		return Result{}, false
	}
	showAll := invocation.hasFlag('a', "all") || invocation.hasFlag('A', "almost-all")
	entries := make([]lsEntry, 0, 32)
	parsed := 0
	dotDirs := 0
	for _, line := range splitLines(output) {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		entry, ok := parseLsLine(line)
		if !ok {
			// An unparsable line means the listing is not the long shape this filter understands
			// (`ls -R` headers, a locale it cannot read, a column set it does not know): keep raw.
			return Result{}, false
		}
		if entry.name == "." || entry.name == ".." {
			// The parent links are never what the listing was asked for, but a listing of nothing
			// else is still an empty directory.
			dotDirs++
			continue
		}
		parsed++
		entry.isNoise = !showAll && entry.isDir && isNoiseDirectory(entry.name)
		entries = append(entries, entry)
	}
	if parsed == 0 {
		if dotDirs > 0 {
			return Result{Text: "(empty)", Omitted: true}, true
		}
		return Result{}, false
	}
	dirs := make([]lsEntry, 0, len(entries))
	files := make([]lsEntry, 0, len(entries))
	filtered := 0
	for _, entry := range entries {
		if entry.isNoise {
			filtered++
			continue
		}
		if entry.isDir {
			dirs = append(dirs, entry)
			continue
		}
		files = append(files, entry)
	}
	shown := make([]string, 0, len(entries))
	for _, entry := range dirs {
		shown = append(shown, entry.octal+"  "+entry.name+"/")
	}
	for _, entry := range files {
		shown = append(shown, entry.octal+"  "+entry.name+"  "+entry.size)
	}
	if len(shown) == 0 {
		if filtered > 0 {
			return Result{Text: fmt.Sprintf("(empty) ... (%d filtered)", filtered), Omitted: true}, true
		}
		return Result{Text: "(empty)", Omitted: true}, true
	}
	truncated := 0
	if len(shown) > capInventory {
		truncated = len(shown) - capInventory
		shown = shown[:capInventory]
	}
	text := strings.Join(shown, "\n")
	switch {
	case truncated > 0 && filtered > 0:
		text += fmt.Sprintf("\n... (%d more, %d filtered)", truncated, filtered)
	case truncated > 0:
		text += fmt.Sprintf("\n... (%d more)", truncated)
	case filtered > 0:
		text += fmt.Sprintf("\n... (%d filtered)", filtered)
	}
	// The compact form keeps the mode, the name, and the size; the owner, group, link count, and
	// timestamp columns are not in it, so the raw listing is named as the recovery path even when
	// no entry was capped or filtered.
	return Result{Text: text, Omitted: true}, true
}

// parseLsLine parses one `ls -l` line into its type, size, mode, and name.
func parseLsLine(line string) (lsEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 || !isPermissions(fields[0]) {
		return lsEntry{}, false
	}
	size := fields[4]
	if !isFileSize(size) {
		return lsEntry{}, false
	}
	name := nameAfterFields(line, 8)
	if name == "" {
		return lsEntry{}, false
	}
	return lsEntry{
		name:  name,
		size:  humanSize(size),
		octal: permissionsToOctal(fields[0]),
		isDir: fields[0][0] == 'd',
	}, true
}

// nameAfterFields returns everything after the first count whitespace-separated fields, so a name
// containing spaces survives.
func nameAfterFields(line string, count int) string {
	index := 0
	for index < len(line) && line[index] == ' ' {
		index++
	}
	for field := 0; field < count; field++ {
		for index < len(line) && line[index] != ' ' {
			index++
		}
		for index < len(line) && line[index] == ' ' {
			index++
		}
	}
	return strings.TrimRight(line[index:], " ")
}

// isPermissions reports whether a field is an `ls -l` permission column.
func isPermissions(field string) bool {
	if len(field) < 10 {
		return false
	}
	switch field[0] {
	case '-', 'd', 'l', 'b', 'c', 'p', 's', 'D':
	default:
		return false
	}
	for index := 1; index < 10; index++ {
		if !strings.ContainsRune("rwxsStT-", rune(field[index])) {
			return false
		}
	}
	return true
}

// isFileSize reports whether a field is a size column, with or without a human suffix.
func isFileSize(field string) bool {
	digits := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(field, "K"), "M"), "B")
	if digits == "" {
		return false
	}
	for _, character := range digits {
		if character != '.' && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

// humanSize renders a byte count the way rtk does.
func humanSize(field string) string {
	if !allDigits(field) {
		return field
	}
	bytes := 0
	for _, character := range field {
		bytes = bytes*10 + int(character-'0')
	}
	switch {
	case bytes >= 1_048_576:
		return fmt.Sprintf("%.1fM", float64(bytes)/1_048_576)
	case bytes >= 1024:
		return fmt.Sprintf("%.1fK", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func allDigits(field string) bool {
	if field == "" {
		return false
	}
	for _, character := range field {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// permissionsToOctal renders the rwx column as three octal digits.
func permissionsToOctal(perms string) string {
	octal := 0
	for index := 1; index < 10; index++ {
		octal <<= 1
		if perms[index] != '-' {
			octal |= 1
		}
	}
	return fmt.Sprintf("%03o", octal)
}

// ---------------------------------------------------------------------------
// tree
// ---------------------------------------------------------------------------

// filterTree drops noise subtrees from `tree` output, caps the body, and keeps the counts line as
// a header. rtk drops the counts line instead, which is the one line that survives a cap.
func filterTree(_ Invocation, output string, _ int) (Result, bool) {
	lines := splitLines(output)
	entries := make([]treeLine, 0, len(lines))
	summary := ""
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if isTreeSummary(line) {
			summary = strings.TrimSpace(line)
			continue
		}
		entry, ok := parseTreeLine(line)
		if !ok {
			return Result{}, false
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return Result{}, false
	}
	kept := make([]treeLine, 0, len(entries))
	dropped := 0
	skipDepth := -1
	for _, entry := range entries {
		if skipDepth >= 0 && entry.depth > skipDepth {
			dropped++
			continue
		}
		skipDepth = -1
		if entry.isNoise {
			skipDepth = entry.depth
			dropped++
			continue
		}
		kept = append(kept, entry)
	}
	omitted := dropped > 0
	capped := 0
	if len(kept) > capInventory {
		capped = len(kept) - capInventory
		kept = kept[:capInventory]
		omitted = true
	}
	var text strings.Builder
	if summary != "" {
		text.WriteString(summary + "\n")
	}
	for _, entry := range kept {
		text.WriteString(entry.line + "\n")
	}
	if capped > 0 {
		fmt.Fprintf(&text, "... +%d more entries\n", capped)
	}
	if dropped > 0 {
		fmt.Fprintf(&text, "... (%d noise entries dropped)\n", dropped)
	}
	return Result{Text: trimTrailingBlankLines(text.String()), Omitted: omitted}, true
}

// treeLine is one parsed tree entry: its box-drawing depth, name, and raw text.
type treeLine struct {
	line    string
	name    string
	depth   int
	isNoise bool
}

// parseTreeLine splits a tree line into its prefix groups and name, reporting false when the line
// is not tree-shaped (so a filter never rewrites output it does not understand).
func parseTreeLine(line string) (treeLine, bool) {
	depth := 0
	rest := line
	for {
		prefix, width, ok := treePrefix(rest)
		if !ok {
			break
		}
		rest = rest[width:]
		if prefix == treeBranch {
			depth++
			continue
		}
		// The leaf prefix names the entry; the branch groups before it are its depth.
		entryName := treeEntryName(rest)
		return treeLine{line: line, name: entryName, depth: depth, isNoise: isNoiseDirectory(entryName)}, true
	}
	if depth == 0 {
		// The root entry carries no prefix at all.
		entryName := treeEntryName(rest)
		return treeLine{line: line, name: entryName, isNoise: isNoiseDirectory(entryName)}, true
	}
	return treeLine{}, false
}

// treePrefix classifies a line's next prefix group, returning its kind and its byte width. The
// box-drawing glyphs are three bytes each, so the widths are not uniform.
func treePrefix(rest string) (int, int, bool) {
	for _, candidate := range []struct {
		prefix string
		kind   int
	}{
		{"│   ", treeBranch},
		{"|   ", treeBranch},
		{"    ", treeBranch},
		{"├── ", treeLeaf},
		{"└── ", treeLeaf},
		{"|-- ", treeLeaf},
		{"`-- ", treeLeaf},
	} {
		if strings.HasPrefix(rest, candidate.prefix) {
			return candidate.kind, len(candidate.prefix), true
		}
	}
	return 0, 0, false
}

const (
	treeBranch = iota
	treeLeaf
)

// treeEntryName strips a symlink target and a directory slash from a tree entry.
func treeEntryName(rest string) string {
	name := rest
	if index := strings.Index(name, " -> "); index >= 0 {
		name = name[:index]
	}
	return strings.TrimSuffix(strings.TrimSpace(name), "/")
}

// isTreeSummary reports whether a line is `tree`'s trailing count line.
func isTreeSummary(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "director") || !strings.Contains(trimmed, "file") {
		return false
	}
	return !strings.ContainsAny(trimmed, "├└│")
}

// ---------------------------------------------------------------------------
// cat / head / tail
// ---------------------------------------------------------------------------

const (
	catCompactLines = 200
	catCompactBytes = 8 * 1024
)

// filterCat compacts a large file dump: comment-only lines and blank runs go, the body is capped,
// and the note says how much of the file the compact form carries. rtk applies the same filter
// behind `-l minimal`; gax cannot add flags to the model's command, so it applies it to large
// dumps. A non-zero exit is never filtered, so an error message stays intact.
func filterCat(invocation Invocation, output string, exitCode int) (Result, bool) {
	if exitCode != 0 {
		return Result{}, false
	}
	if invocation.hasFlag('n', "number") || invocation.hasFlag('b', "number-nonblank") {
		// Line numbers would stop matching the file once comment lines are dropped.
		return Result{}, false
	}
	lines := splitLines(output)
	if len(lines) <= catCompactLines && len(output) <= catCompactBytes {
		return Result{}, false
	}
	style, ok := commentStyleFor(sourcePath(invocation))
	if !ok {
		return Result{}, false
	}
	kept, trimmed := stripComments(lines, style)
	kept = splitLines(collapseBlankRuns(strings.Join(kept, "\n")))
	omitted := trimmed > 0
	capped := 0
	if len(kept) > catCompactLines {
		capped = len(kept) - catCompactLines
		kept = kept[:catCompactLines]
		omitted = true
	}
	if !omitted {
		return Result{}, false
	}
	text := strings.Join(kept, "\n")
	if capped > 0 {
		text += fmt.Sprintf("\n… +%d lines", capped)
	}
	text += fmt.Sprintf("\n[%d of %d lines; comments and blank lines trimmed]", len(kept), len(lines))
	return Result{Text: text, Omitted: true}, true
}

// sourcePath returns the single file argument of a dump command, or "" when the command names no
// file, names several, or takes its input from a pipe.
func sourcePath(invocation Invocation) string {
	file := ""
	skipNext := false
	for _, arg := range invocation.Args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-" {
			return ""
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			switch arg {
			case "-n", "-c", "-m", "--lines", "--bytes", "--max-count":
				skipNext = true
			}
			continue
		}
		if file != "" {
			return ""
		}
		file = arg
	}
	return file
}

// commentStyle is the comment syntax of one language, following rtk's filter table.
type commentStyle struct {
	line       string
	blockStart string
	blockEnd   string
	keepDoc    string
	keepLine   string
}

// commentStyleFor returns the comment style for a file's extension.
func commentStyleFor(file string) (commentStyle, bool) {
	switch strings.ToLower(strings.TrimPrefix(path.Ext(file), ".")) {
	case "rs":
		return commentStyle{line: "//", blockStart: "/*", blockEnd: "*/", keepDoc: "/**", keepLine: "///"}, true
	case "go", "js", "mjs", "cjs", "ts", "tsx", "jsx", "c", "h", "cc", "cpp", "cxx", "hpp", "hh",
		"java", "cs", "kt", "kts", "swift", "scala", "php", "proto", "dart", "sol", "zig":
		return commentStyle{line: "//", blockStart: "/*", blockEnd: "*/", keepDoc: "/**"}, true
	case "py", "pyw", "pyi":
		return commentStyle{line: "#", blockStart: `"""`, blockEnd: `"""`, keepDoc: `"""`}, true
	case "rb", "sh", "bash", "zsh", "ksh", "yaml", "yml", "toml", "ini", "cfg", "conf", "tf",
		"tfvars", "pl", "pm", "r", "jl", "nix", "mk", "makefile", "dockerfile", "gitignore",
		"properties", "env":
		return commentStyle{line: "#"}, true
	case "sql", "lua", "hs", "elm", "ada":
		return commentStyle{line: "--"}, true
	case "lisp", "clj", "cljs", "scm", "el", "asm", "s", "vhd":
		return commentStyle{line: ";"}, true
	default:
		return commentStyle{}, false
	}
}

// stripComments drops comment-only lines and comment blocks, keeping documentation comments the
// way rtk's minimal filter does. It returns the kept lines and how many were dropped.
func stripComments(lines []string, style commentStyle) ([]string, int) {
	kept := make([]string, 0, len(lines))
	dropped := 0
	inBlock := false
	inDocBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inDocBlock {
			kept = append(kept, line)
			if strings.Contains(trimmed, style.blockEnd) {
				inDocBlock = false
			}
			continue
		}
		if inBlock {
			dropped++
			if strings.Contains(trimmed, style.blockEnd) {
				inBlock = false
			}
			continue
		}
		if style.blockStart != "" && strings.HasPrefix(trimmed, style.blockStart) {
			if style.keepDoc != "" && strings.HasPrefix(trimmed, style.keepDoc) {
				kept = append(kept, line)
				inDocBlock = !strings.Contains(trimmed[len(style.keepDoc):], style.blockEnd)
				continue
			}
			dropped++
			inBlock = !strings.Contains(trimmed[len(style.blockStart):], style.blockEnd)
			continue
		}
		if style.line != "" && strings.HasPrefix(trimmed, style.line) {
			if style.keepLine != "" && strings.HasPrefix(trimmed, style.keepLine) {
				kept = append(kept, line)
				continue
			}
			if strings.HasPrefix(trimmed, "#!") {
				kept = append(kept, line)
				continue
			}
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	return kept, dropped
}

// ---------------------------------------------------------------------------
// grep / rg
// ---------------------------------------------------------------------------

const (
	searchLineWidth  = 80
	searchMaxResults = 200
	searchPerFile    = 25
)

// searchMatch is one parsed grep/rg match or context line.
type searchMatch struct {
	file    string
	line    int
	content string
	context bool
}

// filterSearch groups grep/rg matches by file once the caps bite, and truncates long lines either
// way (rtk's search filter).
func filterSearch(invocation Invocation, output string, _ int) (Result, bool) {
	matches := make([]searchMatch, 0, 64)
	for _, line := range splitLines(output) {
		if line == "" || line == "--" {
			continue
		}
		match, ok := parseSearchLine(line)
		if !ok {
			return Result{}, false
		}
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		return Result{}, false
	}
	truncated := 0
	for index := range matches {
		if width := displayWidth(matches[index].content); width > searchLineWidth {
			matches[index].content = truncateLine(matches[index].content, searchLineWidth)
			truncated++
		}
	}
	files := make([]string, 0, 8)
	perFile := make(map[string][]searchMatch)
	for _, match := range matches {
		if _, seen := perFile[match.file]; !seen {
			files = append(files, match.file)
		}
		perFile[match.file] = append(perFile[match.file], match)
	}
	plain := make([]string, 0, len(matches))
	for _, match := range matches {
		separator := ":"
		if match.context {
			separator = "-"
		}
		plain = append(plain, fmt.Sprintf("%s%s%d%s%s", match.file, separator, match.line, separator, match.content))
	}
	shown := 0
	skippedFiles := 0
	var body strings.Builder
	for _, file := range files {
		entries := perFile[file]
		if shown >= searchMaxResults {
			skippedFiles++
			continue
		}
		display := compactPath(file)
		shownHere := 0
		for _, match := range entries {
			if shown >= searchMaxResults || shownHere >= searchPerFile {
				break
			}
			separator := ":"
			if match.context {
				separator = "-"
			}
			fmt.Fprintf(&body, "%s%s%d%s%s\n", display, separator, match.line, separator, match.content)
			shown++
			shownHere++
		}
		if remaining := len(entries) - shownHere; remaining > 0 {
			fmt.Fprintf(&body, "  +%d more in %s\n", remaining, display)
		}
	}
	if skippedFiles > 0 {
		fmt.Fprintf(&body, "+%d more files\n", skippedFiles)
	}
	capped := shown < len(matches) || skippedFiles > 0
	if !capped {
		text := strings.Join(plain, "\n")
		if truncated == 0 {
			// Nothing changed: report no filter so the raw bytes stay authoritative.
			return Result{}, false
		}
		text += fmt.Sprintf("\n[%d long lines truncated]", truncated)
		return Result{Text: text, Omitted: true}, true
	}
	grouped := fmt.Sprintf("%d matches in %d files:\n\n%s", len(matches), len(files), body.String())
	grouped = trimTrailingBlankLines(grouped)
	if truncated > 0 {
		grouped += fmt.Sprintf("\n[%d long lines truncated]", truncated)
	}
	if len(grouped) >= len(plain) && truncated == 0 {
		return Result{}, false
	}
	return Result{Text: grouped, Omitted: true}, true
}

// parseSearchLine parses one grep/rg output line into a match. Paths containing the separator are
// ambiguous without `--null`, so a line whose leading field is not `path:line` is refused.
func parseSearchLine(line string) (searchMatch, bool) {
	file, rest, ok := splitField(line, ':')
	if ok {
		if number, content, ok := splitNumber(rest); ok {
			return searchMatch{file: file, line: number, content: content}, true
		}
		// A context line uses '-' as both separators (`path-12-content`).
		if number, content, ok := splitNumberWith(rest, '-'); ok {
			return searchMatch{file: file, line: number, content: content, context: true}, true
		}
	}
	if number, content, ok := splitNumber(line); ok {
		return searchMatch{file: "", line: number, content: content}, true
	}
	return searchMatch{}, false
}

// splitField splits a line at its first separator.
func splitField(line string, separator byte) (string, string, bool) {
	index := strings.IndexByte(line, separator)
	if index <= 0 || index == len(line)-1 {
		return "", "", false
	}
	return line[:index], line[index+1:], true
}

// splitNumber splits `12:content` into its number and content.
func splitNumber(rest string) (int, string, bool) {
	return splitNumberWith(rest, ':')
}

// splitNumberWith splits `12<sep>content` into its number and content.
func splitNumberWith(rest string, separator byte) (int, string, bool) {
	index := strings.IndexByte(rest, separator)
	if index <= 0 || index == len(rest)-1 {
		return 0, "", false
	}
	number := 0
	for _, character := range rest[:index] {
		if character < '0' || character > '9' {
			return 0, "", false
		}
		number = number*10 + int(character-'0')
	}
	return number, rest[index+1:], true
}

// displayWidth counts a string's runes, which is what the line cap is measured in.
func displayWidth(text string) int {
	return len([]rune(text))
}

// ---------------------------------------------------------------------------
// ast-grep
// ---------------------------------------------------------------------------

const (
	astGrepPerFile = 5
	astGrepTotal   = capInventory
)

// filterAstGrep groups `ast-grep run` matches by file, capping each file and the whole listing
// (rtk's filter_ast_grep). Any line the filter cannot parse means a shape it does not understand
// (scan diagnostics, heading mode), so the raw output stays.
func filterAstGrep(_ Invocation, output string, _ int) (Result, bool) {
	matches := make([]searchMatch, 0, 64)
	for _, line := range splitLines(output) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		match, ok := parseSearchLine(line)
		if !ok || match.file == "" {
			return Result{}, false
		}
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		return Result{}, false
	}
	files := make([]string, 0, 8)
	perFile := make(map[string][]searchMatch)
	for _, match := range matches {
		if _, seen := perFile[match.file]; !seen {
			files = append(files, match.file)
		}
		perFile[match.file] = append(perFile[match.file], match)
	}
	shown := 0
	skippedFiles := 0
	skippedLines := 0
	var body strings.Builder
	for _, file := range files {
		entries := perFile[file]
		if shown >= astGrepTotal {
			skippedFiles++
			skippedLines += len(entries)
			continue
		}
		shownHere := 0
		for _, match := range entries {
			if shown >= astGrepTotal || shownHere >= astGrepPerFile {
				break
			}
			fmt.Fprintf(&body, "%s:%d:%s\n", file, match.line, match.content)
			shown++
			shownHere++
		}
		if remaining := len(entries) - shownHere; remaining > 0 {
			skippedLines += remaining
			fmt.Fprintf(&body, "  … %d more match line(s) in %s\n", remaining, file)
		}
	}
	if skippedFiles > 0 {
		fmt.Fprintf(&body, "… %d more match line(s) in %d more file(s) not shown\n", skippedLines, skippedFiles)
	}
	text := trimTrailingBlankLines(body.String())
	if text == "" {
		return Result{}, false
	}
	return Result{Text: text, Omitted: true}, true
}

package bashcompress

import (
	"fmt"
	"strings"
	"time"
)

// gitValueFlags are git's global options that consume the next argument, so the subcommand after
// them is not mistaken for one.
var gitValueFlags = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--exec-path": true, "--config-env": true,
}

// filterGit dispatches on the git subcommand.
func filterGit(invocation Invocation, output string, exitCode int) (Result, bool) {
	switch gitSubcommand(invocation) {
	case "status":
		return filterGitStatus(output)
	case "diff":
		return filterGitDiff(output)
	case "log":
		return filterGitLog(invocation, output)
	case "add", "commit", "push", "pull", "fetch", "stash":
		return filterGitConfirmation(gitSubcommand(invocation), output, exitCode)
	default:
		return Result{}, false
	}
}

// gitSubcommand returns the git subcommand, skipping global options.
func gitSubcommand(invocation Invocation) string {
	args := invocation.Args
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if gitValueFlags[arg] {
			index++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

// ---------------------------------------------------------------------------
// git status
// ---------------------------------------------------------------------------

// statusGroups maps git's section headers to the porcelain column they stand for.
var statusStagedCodes = map[string]string{
	"modified":     "M",
	"new file":     "A",
	"deleted":      "D",
	"renamed":      "R",
	"copied":       "C",
	"typechange":   "T",
	"unmerged":     "U",
	"both added":   "AA",
	"both modifed": "UU",
}

var statusUnstagedCodes = map[string]string{
	"modified":   "M",
	"deleted":    "D",
	"typechange": "T",
}

var statusUnmergedCodes = map[string]string{
	"both modified":   "UU",
	"both added":      "AA",
	"deleted by us":   "DU",
	"deleted by them": "UD",
	"added by us":     "AU",
	"added by them":   "UA",
	"both deleted":    "DD",
}

// filterGitStatus rewrites `git status` into the porcelain shape rtk prints: a `* branch` header,
// one `XY path` line per entry, and the working-tree state that the porcelain form drops.
func filterGitStatus(output string) (Result, bool) {
	lines := splitLines(output)
	if len(lines) == 0 {
		return Result{}, false
	}
	header := ""
	clean := false
	state := ""
	section := ""
	staged := make([]string, 0, 16)
	unstaged := make([]string, 0, 16)
	untracked := make([]string, 0, 16)
	unmerged := make([]string, 0, 8)
	sawStatus := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "(use \"git") || strings.HasPrefix(trimmed, "(create/copy") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "On branch "):
			sawStatus = true
			header = "* " + strings.TrimPrefix(trimmed, "On branch ")
		case strings.HasPrefix(trimmed, "HEAD detached "):
			sawStatus = true
			header = "* " + trimmed
		case strings.HasPrefix(trimmed, "Your branch is ahead of "):
			header += " [ahead " + trailingCount(trimmed) + "]"
		case strings.HasPrefix(trimmed, "Your branch is behind "):
			header += " [behind " + trailingCount(trimmed) + "]"
		case trimmed == "Changes to be committed:":
			section = "staged"
		case trimmed == "Changes not staged for commit:":
			section = "unstaged"
		case trimmed == "Untracked files:":
			section = "untracked"
		case trimmed == "Unmerged paths:":
			section = "unmerged"
		case strings.HasPrefix(trimmed, "nothing to commit") || strings.HasPrefix(trimmed, "no changes added"):
			sawStatus = true
			clean = true
		case strings.Contains(trimmed, "in progress") || strings.HasPrefix(trimmed, "You are currently") ||
			strings.HasPrefix(trimmed, "You have unmerged paths"):
			state = statusState(trimmed)
		default:
			if section == "untracked" {
				untracked = append(untracked, "?? "+strings.TrimSuffix(trimmed, "/"))
				continue
			}
			if section == "" || !strings.Contains(trimmed, ":") {
				continue
			}
			kind, path, ok := splitStatusEntry(trimmed)
			if !ok {
				continue
			}
			switch section {
			case "staged":
				code := statusStagedCodes[kind]
				if code == "" {
					continue
				}
				staged = append(staged, code+"  "+path)
			case "unstaged":
				code := statusUnstagedCodes[kind]
				if code == "" {
					continue
				}
				unstaged = append(unstaged, " "+code+" "+path)
			case "unmerged":
				code := statusUnmergedCodes[kind]
				if code == "" {
					continue
				}
				unmerged = append(unmerged, code+" "+path)
			}
		}
	}
	if !sawStatus {
		// An error message or a shape this filter does not know stays raw.
		return Result{}, false
	}
	out := make([]string, 0, len(staged)+len(unstaged)+len(untracked)+4)
	if header != "" {
		out = append(out, header)
	}
	if state != "" {
		out = append(out, state)
	}
	omitted := false
	for _, group := range [][]string{unmerged, staged, unstaged, untracked} {
		if len(group) > capList {
			omitted = true
			out = append(out, group[:capList]...)
			out = append(out, fmt.Sprintf("… +%d more", len(group)-capList))
			continue
		}
		out = append(out, group...)
	}
	if clean && len(out) <= 2 && len(staged) == 0 && len(unstaged) == 0 && len(untracked) == 0 && len(unmerged) == 0 {
		out = append(out, "clean — nothing to commit")
	}
	if len(out) == 0 {
		return Result{}, false
	}
	return Result{Text: strings.Join(out, "\n"), Omitted: omitted}, true
}

// splitStatusEntry splits `modified:   path` into its kind and path.
func splitStatusEntry(trimmed string) (string, string, bool) {
	index := strings.Index(trimmed, ":")
	if index <= 0 {
		return "", "", false
	}
	kind := strings.TrimSpace(trimmed[:index])
	path := strings.TrimSpace(trimmed[index+1:])
	if path == "" {
		return "", "", false
	}
	return kind, path, true
}

// trailingCount returns the number in `... by 2 commits.`
func trailingCount(line string) string {
	fields := strings.Fields(strings.TrimSuffix(line, "."))
	for index, field := range fields {
		if field == "by" && index+1 < len(fields) {
			return fields[index+1]
		}
	}
	return "0"
}

// statusState maps an in-progress operation's line to rtk's short summary.
func statusState(line string) string {
	switch {
	case strings.Contains(line, "rebase") || strings.Contains(line, "rebasing"):
		return "rebase in progress"
	case strings.Contains(line, "cherry-pick"):
		return "cherry-pick in progress"
	case strings.Contains(line, "revert"):
		return "revert in progress"
	case strings.Contains(line, "bisect"):
		return "bisect in progress"
	case strings.Contains(line, "am session"):
		return "am session in progress"
	case strings.Contains(line, "unmerged paths"):
		return "merge in progress. unresolved conflicts"
	case strings.Contains(line, "still merging"):
		return "merge in progress. no conflicts"
	case strings.Contains(line, "sparse checkout"):
		return "sparse checkout enabled"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// git diff
// ---------------------------------------------------------------------------

const (
	diffMaxLines      = 500
	diffMaxHunkLines  = 100
	diffLeadingLines  = 3
	diffLeadingBudget = diffMaxLines / 10
)

// filterGitDiff compacts a unified diff the way rtk's compact_diff does: file headers, per-file
// change counts, hunks with bounded leading context, and notes for everything the caps held back.
func filterGitDiff(output string) (Result, bool) {
	if !strings.Contains(output, "diff --") && !strings.Contains(output, "@@") {
		return Result{}, false
	}
	text, omitted := compactDiff(output, diffMaxLines)
	if text == "" {
		return Result{}, false
	}
	return Result{Text: text, Omitted: omitted}, true
}

// compactDiff ports rtk's compact_diff.
func compactDiff(diff string, maxLines int) (string, bool) {
	result := make([]string, 0, 64)
	file := ""
	added, removed := 0, 0
	inHunk := false
	hunkLines := 0
	skippedAdd, skippedDel := 0, 0
	leading := make([]string, 0, diffLeadingLines)
	leadingTotal := 0
	omitted := false

	flushLeading := func() {
		room := diffLeadingBudget - leadingTotal
		if room < 0 {
			room = 0
		}
		keep := len(leading)
		if keep > room {
			keep = room
		}
		for _, context := range leading[len(leading)-keep:] {
			result = append(result, context)
		}
		leadingTotal += keep
		leading = leading[:0]
	}
	flushCounts := func() {
		if file != "" && (added > 0 || removed > 0) {
			result = append(result, fmt.Sprintf("  +%d -%d", added, removed))
		}
		added, removed = 0, 0
	}
	flushTruncation := func() {
		if skippedAdd == 0 && skippedDel == 0 {
			return
		}
		switch {
		case skippedDel == 0:
			result = append(result, fmt.Sprintf("  ... (%d addition(s) truncated)", skippedAdd))
		case skippedAdd == 0:
			result = append(result, fmt.Sprintf("  ... (%d deletion(s) truncated)", skippedDel))
		default:
			result = append(result, fmt.Sprintf("  ... (%d deletion(s), %d addition(s) truncated)", skippedDel, skippedAdd))
		}
		skippedAdd, skippedDel = 0, 0
		omitted = true
	}

	for _, line := range splitLines(diff) {
		switch {
		case strings.HasPrefix(line, "diff --"):
			flushLeading()
			flushTruncation()
			flushCounts()
			file = diffHeaderPath(line)
			result = append(result, "\n"+file)
			inHunk = false
			hunkLines = 0
		case strings.HasPrefix(line, "@@"):
			flushLeading()
			flushTruncation()
			inHunk = true
			hunkLines = 0
			result = append(result, line)
		case inHunk:
			if strings.HasPrefix(line, "\\") {
				continue
			}
			marker := byte(0)
			if line != "" {
				marker = line[0]
			}
			switch marker {
			case '+', '-':
				if marker == '+' {
					added++
				} else {
					removed++
				}
				if hunkLines < diffMaxHunkLines {
					flushLeading()
					result = append(result, line)
					hunkLines++
				} else if marker == '-' {
					skippedDel++
				} else {
					skippedAdd++
				}
				leading = leading[:0]
			default:
				if hunkLines > 0 {
					if hunkLines < diffMaxHunkLines {
						result = append(result, line)
						hunkLines++
					}
				} else if leadingTotal < diffLeadingBudget {
					if len(leading) == diffLeadingLines {
						leading = leading[1:]
					}
					leading = append(leading, line)
				}
			}
		case strings.HasPrefix(line, "Binary files"):
			// The header alone would imply an empty change, so the note is kept.
			result = append(result, line)
		}
		if len(result)-leadingTotal >= maxLines {
			result = append(result, "\n... (more changes truncated)")
			omitted = true
			break
		}
	}
	flushLeading()
	flushTruncation()
	flushCounts()
	return strings.TrimLeft(strings.Join(result, "\n"), "\n"), omitted
}

// diffHeaderPath extracts the destination path from a `diff --git a/x b/x` header.
func diffHeaderPath(line string) string {
	// `diff --git <src> <dst>`: everything after the second field.
	rest := line
	for field := 0; field < 2; field++ {
		index := strings.Index(rest, " ")
		if index < 0 {
			return "unknown"
		}
		rest = rest[index+1:]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "unknown"
	}
	if halves := strings.SplitN(rest, " ", 2); len(halves) == 2 {
		left, right := unquoteGitPath(halves[0]), unquoteGitPath(halves[1])
		if left == right {
			return stripDiffPrefix(left)
		}
		if leftTail, rightTail := tailPath(left), tailPath(right); leftTail == rightTail && leftTail != "" {
			return rightTail
		}
		return stripDiffPrefix(right)
	}
	return stripDiffPrefix(unquoteGitPath(rest))
}

// stripDiffPrefix removes git's a/ or b/ prefix.
func stripDiffPrefix(path string) string {
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(path, prefix) {
			return path[len(prefix):]
		}
	}
	return path
}

// tailPath returns a path without its first component.
func tailPath(path string) string {
	if index := strings.Index(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return ""
}

// unquoteGitPath undoes git's quoting of a path that contains special characters.
func unquoteGitPath(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	quoted := path[1 : len(path)-1]
	var out strings.Builder
	for index := 0; index < len(quoted); index++ {
		if quoted[index] != '\\' || index+1 >= len(quoted) {
			out.WriteByte(quoted[index])
			continue
		}
		index++
		if quoted[index] >= '0' && quoted[index] <= '7' {
			value := 0
			for length := 0; length < 3 && index < len(quoted) && quoted[index] >= '0' && quoted[index] <= '7'; length++ {
				value = value*8 + int(quoted[index]-'0')
				index++
			}
			index--
			out.WriteByte(byte(value))
			continue
		}
		switch quoted[index] {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		default:
			out.WriteByte(quoted[index])
		}
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// git log
// ---------------------------------------------------------------------------

const (
	gitLogDefaultLimit = 10
	gitLogBodyLines    = 3
	gitLogLineWidth    = 120
)

// filterGitLog rewrites the default `git log` shape into rtk's one-line-per-commit form:
// `hash subject (age) <author>` with up to three body lines.
func filterGitLog(invocation Invocation, output string) (Result, bool) {
	limit := gitLogDefaultLimit
	if value, ok := invocation.flagValue('n', "max-count"); ok && allDigits(value) {
		limit = atoi(value)
	}
	if strings.Contains(output, "@@") || strings.HasPrefix(output, "commit ") && strings.Contains(output, "diff --git") {
		// A patch log is a diff, not a listing.
		return Result{}, false
	}
	commits := make([]gitCommit, 0, 16)
	current := gitCommit{}
	flush := func() {
		if current.hash != "" {
			commits = append(commits, current)
		}
		current = gitCommit{}
	}
	for _, line := range splitLines(output) {
		switch {
		case strings.HasPrefix(line, "commit "):
			flush()
			current.hash = strings.TrimSpace(strings.TrimPrefix(line, "commit "))
		case strings.HasPrefix(line, "Merge: "):
		case strings.HasPrefix(line, "Author: "):
			current.author = strings.TrimSpace(strings.TrimPrefix(line, "Author: "))
		case strings.HasPrefix(line, "Date: "):
			current.date = strings.TrimSpace(strings.TrimPrefix(line, "Date: "))
		default:
			trimmed := strings.TrimSpace(line)
			if current.hash == "" || trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "Signed-off-by:") || strings.HasPrefix(trimmed, "Co-authored-by:") {
				continue
			}
			if current.subject == "" {
				current.subject = trimmed
				continue
			}
			current.body = append(current.body, trimmed)
		}
	}
	flush()
	if len(commits) == 0 {
		return Result{}, false
	}
	omitted := false
	hidden := 0
	if len(commits) > limit {
		omitted = true
		hidden = len(commits) - limit
		commits = commits[:limit]
	}
	entries := make([]string, 0, len(commits))
	for _, commit := range commits {
		hash := commit.hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		entry := hash + " " + truncateLine(commit.subject, gitLogLineWidth)
		if age := relativeAge(commit.date); age != "" {
			entry += " (" + age + ")"
		}
		if commit.author != "" {
			entry += " <" + commit.author + ">"
		}
		body := commit.body
		if len(body) > gitLogBodyLines {
			omitted = true
			remaining := len(body) - gitLogBodyLines
			body = body[:gitLogBodyLines]
			for _, line := range body {
				entry += "\n  " + truncateLine(line, gitLogLineWidth)
			}
			entry += fmt.Sprintf("\n  [+%d lines omitted]", remaining)
		} else {
			for _, line := range body {
				entry += "\n  " + truncateLine(line, gitLogLineWidth)
			}
		}
		entries = append(entries, entry)
	}
	if hidden > 0 {
		entries = append(entries, fmt.Sprintf("… (%d older commits not shown; pass -n to see more)", hidden))
	}
	return Result{Text: strings.Join(entries, "\n"), Omitted: omitted}, true
}

// gitCommit is one commit parsed from `git log` output.
type gitCommit struct {
	hash    string
	author  string
	date    string
	subject string
	body    []string
}

// gitDateLayouts are the date shapes git prints for `Date:`.
var gitDateLayouts = []string{
	"Mon Jan _2 15:04:05 2006 -0700",
	"Mon Jan _2 15:04:05 2006",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02",
}

// relativeAge renders git's own "2 days ago" phrasing for a commit date.
func relativeAge(value string) string {
	if value == "" {
		return ""
	}
	var when time.Time
	parsed := false
	for _, layout := range gitDateLayouts {
		if candidate, err := time.Parse(layout, value); err == nil {
			when, parsed = candidate, true
			break
		}
	}
	if !parsed {
		return ""
	}
	seconds := int64(time.Since(when).Seconds())
	switch {
	case seconds < 0:
		return "in the future"
	case seconds < 60:
		return pluralAge(seconds, "second")
	case seconds < 3600:
		return pluralAge(seconds/60, "minute")
	case seconds < 86400:
		return pluralAge(seconds/3600, "hour")
	case seconds < 86400*7:
		return pluralAge(seconds/86400, "day")
	case seconds < 86400*30:
		return pluralAge(seconds/(86400*7), "week")
	case seconds < 86400*365:
		return pluralAge(seconds/(86400*30), "month")
	default:
		return pluralAge(seconds/(86400*365), "year")
	}
}

func pluralAge(count int64, unit string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", count, unit)
}

// ---------------------------------------------------------------------------
// git add / commit / push / pull / fetch / stash
// ---------------------------------------------------------------------------

// filterGitConfirmation replaces a successful git command's progress output with one line, the way
// rtk does. A failure keeps its own text: the error is the answer there.
func filterGitConfirmation(subcommand, output string, exitCode int) (Result, bool) {
	if exitCode != 0 {
		return Result{}, false
	}
	switch subcommand {
	case "add":
		if strings.TrimSpace(output) == "" {
			// Nothing to replace: an empty output is already the smallest form.
			return Result{}, false
		}
		return Result{}, false
	case "commit":
		if strings.TrimSpace(output) == "" {
			return Result{Text: "ok"}, true
		}
		// The commit summary replaces a header line plus a diffstat, so the full text is named
		// as the recovery path.
		return Result{Text: commitConfirmation(output), Omitted: true}, true
	case "push":
		return dropped(pushConfirmation(output))
	case "pull":
		return dropped(pullConfirmation(output))
	case "fetch":
		return dropped(fetchConfirmation(output))
	case "stash":
		return dropped(stashConfirmation(output))
	default:
		return Result{}, false
	}
}

// dropped marks a confirmation as lossy: it replaces progress output the raw command printed, so
// the caller keeps the full output recoverable.
func dropped(result Result, ok bool) (Result, bool) {
	if ok {
		result.Omitted = true
	}
	return result, ok
}

// commitConfirmation reduces `git commit` output to `ok <hash>` (rtk's parse_commit_output).
func commitConfirmation(output string) string {
	line := ""
	for _, candidate := range splitLines(output) {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	open := strings.Index(line, "[")
	closing := strings.Index(line, "]")
	if open < 0 || closing <= open {
		return "ok"
	}
	fields := strings.Fields(line[open+1 : closing])
	if len(fields) == 0 {
		return "ok"
	}
	hash := fields[len(fields)-1]
	if len([]rune(hash)) >= 7 {
		return "ok " + string([]rune(hash)[:7])
	}
	return "ok"
}

// pushConfirmation reduces `git push` output to `ok <branch>`.
func pushConfirmation(output string) (Result, bool) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || strings.Contains(trimmed, "Everything up-to-date") {
		return Result{Text: "ok"}, true
	}
	for _, line := range splitLines(output) {
		line = strings.TrimSpace(line)
		branch := pushBranch(line)
		if branch == "" {
			continue
		}
		switch {
		case strings.Contains(line, "[new branch]"), strings.Contains(line, "[new tag]"):
			return Result{Text: "ok " + branch + " (new)"}, true
		case strings.Contains(line, "[deleted]"):
			return Result{Text: "ok " + branch + " (deleted)"}, true
		case strings.Contains(line, ".."):
			return Result{Text: "ok " + branch}, true
		}
	}
	return Result{}, false
}

// pushBranch returns the destination ref of a push progress line.
func pushBranch(line string) string {
	if index := strings.Index(line, "->"); index >= 0 {
		rest := strings.TrimSpace(line[index+2:])
		if space := strings.IndexByte(rest, ' '); space >= 0 {
			rest = rest[:space]
		}
		return rest
	}
	if strings.Contains(line, "[deleted]") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			return fields[len(fields)-1]
		}
	}
	return ""
}

// pullConfirmation reduces `git pull` output to `ok [N files +A -D]`.
func pullConfirmation(output string) (Result, bool) {
	trimmed := strings.TrimSpace(output)
	if strings.Contains(trimmed, "Already up to date") {
		return Result{Text: "ok"}, true
	}
	for _, line := range splitLines(output) {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "changed") || !strings.Contains(line, "insertion") && !strings.Contains(line, "deletion") {
			continue
		}
		files := 0
		for _, field := range strings.Fields(line) {
			if allDigits(field) {
				files = atoi(field)
				break
			}
		}
		added := signedCount(line, "insertion")
		removed := signedCount(line, "deletion")
		return Result{Text: fmt.Sprintf("ok %d files +%d -%d", files, added, removed)}, true
	}
	return Result{}, false
}

// signedCount reads the `N insertions(+)` count of a diffstat line.
func signedCount(line, noun string) int {
	fields := strings.Fields(line)
	for index, field := range fields {
		if strings.HasPrefix(field, noun) && index > 0 {
			count := strings.TrimRight(fields[index-1], ",")
			if allDigits(count) {
				return atoi(count)
			}
		}
	}
	return 0
}

// fetchConfirmation counts the refs a fetch reported.
func fetchConfirmation(output string) (Result, bool) {
	refs := 0
	for _, line := range splitLines(output) {
		if strings.Contains(line, "->") {
			refs++
		}
	}
	if refs > 0 {
		return Result{Text: fmt.Sprintf("ok fetched (%d refs)", refs)}, true
	}
	if strings.TrimSpace(output) == "" {
		return Result{Text: "ok"}, true
	}
	return Result{}, false
}

// stashConfirmation reduces `git stash` output to `ok stash <action>`.
func stashConfirmation(output string) (Result, bool) {
	trimmed := strings.TrimSpace(output)
	switch {
	case strings.HasPrefix(trimmed, "Saved working directory"):
		return Result{Text: "ok stash saved"}, true
	case strings.Contains(trimmed, "Dropped refs/stash"):
		return Result{Text: "ok stash dropped"}, true
	case strings.Contains(trimmed, "No local changes to save"):
		return Result{Text: "ok stash (nothing to save)"}, true
	default:
		return Result{}, false
	}
}

// atoi parses a non-negative decimal that allDigits already accepted.
func atoi(value string) int {
	number := 0
	for _, character := range value {
		number = number*10 + int(character-'0')
	}
	return number
}

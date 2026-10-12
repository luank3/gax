# bashcompress spec

## Scope

Input: one shell command line, the bytes it captured, and its exit code. Output: a `Result` carrying
the compact text, an `Omitted` flag, and whether the compact form replaces the raw bytes. The
boundary is the shell parse and the command's own output; `internal/tools/bash.go` owns caps,
display, and recovery files. Non-goal: gax never rewrites the command line.

## Invariants

### Only one simple command is ever filtered

- **Rule:** Refuse the command unless it is a single simple command; a pipe, redirect, `;`, `&&`,
  `||`, background `&`, backtick, `$(`, or newline means the captured bytes are another program's
  output.
- **Source:** `internal/bashcompress/command.go:31` (`Parse`).
- **Assertion:** `TestParseRefusesEverythingButOneSimpleCommand` in `internal/bashcompress/files_test.go:39`.
- **Rejected:** Splitting on `strings.Fields` and filtering `git status` out of
  `git status | head -2`; the bytes then come from `head`, not from `git`.

---
### The program word decides which filter runs

- **Rule:** Look the program's basename, lowercased, up in `filters`; an unknown program, a known
  program whose output the filter does not recognize, and a subcommand no branch handles all keep
  the raw bytes.
- **Source:** `internal/bashcompress/compress.go:75` (`Compress`; `filters` at `internal/bashcompress/compress.go:37`).
- **Assertion:** `TestCompressDispatchesByProgramAndSubcommand` in `internal/bashcompress/files_test.go:63`.
- **Rejected:** Dispatching on the subcommand alone or on a substring of the command line, which
  would run the `git status` filter over `git stash list` output.

---
### A result replaces the raw bytes only when it is strictly smaller

- **Rule:** Return false when `len(result.Text) >= len(output)`, so a filter can never grow the text
  the model reads (rtk's `never_worse`, compared in bytes here, not in estimated tokens).
- **Source:** `internal/bashcompress/compress.go:92` (`Compress`).
- **Assertion:** `TestCompressRefusesWhenTheCompactFormIsNotSmaller` in `internal/bashcompress/files_test.go:76`.
- **Rejected:** Comparing rune counts or line counts instead of bytes, which would let an 80-rune
  line pass as a win over the raw line it replaces.

---
### A filter must refuse output whose shape it cannot read

- **Rule:** Any line a filter cannot parse aborts the whole filter, so an unrecognized column set,
  heading, or diagnostic keeps the capture verbatim instead of being rewritten.
- **Source:** `internal/bashcompress/files.go:41` (`filterLs`, first refusal at `internal/bashcompress/files.go:57`); the
  same guard in `filterTree` (`internal/bashcompress/files.go:256`), `filterSearch` (`internal/bashcompress/files.go:570`), `filterAstGrep`
  (`internal/bashcompress/files.go:724`), `filterRuff` (`internal/bashcompress/tests.go:964`), and `filterSqlfluff` (`internal/bashcompress/tests.go:1110`).
- **Assertion:** `TestFilterLsRefusesShapesItCannotRead` in `internal/bashcompress/files_test.go:152`; corroborated by
  `TestFilterRuffRefusesUnreadableOutput` in `internal/bashcompress/tests_test.go:442`.
- **Rejected:** Skipping unparsable lines and compacting the rest; `ls -R` directory headers would
  then vanish from a listing that spans several directories.

---
### `Omitted` means content the raw output still holds

- **Rule:** Set `Omitted` whenever the compact form leaves out lines or columns the raw output
  carries, and leave it false for a pure reformatting.
- **Source:** `internal/bashcompress/compress.go:24` (`Result`); `filterGitStatus` returns
  `Omitted: false` at `internal/bashcompress/git.go:192`, while `filterDocker` returns `Omitted: true` unconditionally at
  `internal/bashcompress/docker.go:90` because it drops the `COMMAND` and `CREATED` columns.
- **Assertion:** `TestFilterGitStatusGroupsByState` in `internal/bashcompress/git_test.go:25` (false);
  `TestFilterLsCompactsLongListings` in `internal/bashcompress/files_test.go:111` (true).
- **Rejected:** Setting `Omitted` only for capped lists; the `ls` compact form drops the owner,
  group, and timestamp columns, and the raw listing is its only recovery path.

---
### A non-zero exit keeps the command's own error text

- **Rule:** `cat`, `head`, `tail`, `bat`, `docker ps`, and the git confirmation filters bail out on
  a non-zero exit code, because there the error message is the answer.
- **Source:** `internal/bashcompress/files.go:396` (`filterCat`, guard at `internal/bashcompress/files.go:397`);
  `internal/bashcompress/git.go:636` (`filterGitConfirmation`).
- **Assertion:** `TestFilterGitStatusLeavesErrorsAlone` in `internal/bashcompress/git_test.go:61`; and
  `TestFilterDockerPsLeavesOtherOutputAlone` in `internal/bashcompress/tests_test.go:511`.
- **Rejected:** Applying the `cat` filter because the output is large enough; `cat missing.go` would
  then report a trimmed dump instead of the error.

---
### Failure blocks survive, passing tests collapse to a count

- **Rule:** Test-runner and linter filters print failures before anything else and keep each failing
  test's name, its `file.ext:line` locations, and its outcome lines; passing test lines become
  counts.
- **Source:** `internal/bashcompress/tests.go:26` (`filterCargo`, `FAILURES (%d):` at
  `internal/bashcompress/tests.go:109`); `internal/bashcompress/tests.go:465` (`formatGoSummary`); `internal/bashcompress/tests.go:556` (`filterPytest`).
- **Assertion:** `TestFilterCargoTestKeepsFailureLocations` in `internal/bashcompress/tests_test.go:70`;
  `TestFilterGoTestReadsJSONOutput` in `internal/bashcompress/tests_test.go:250`;
  `TestFilterPytestKeepsTracebackFrames` in `internal/bashcompress/tests_test.go:154`.
- **Rejected:** Keyword-filtering the cargo failure block with `failureDetailLine`; libtest's
  `  left: 1,` carries no keyword and would be dropped, taking the compared values with it.

---
### Failure details are location-first and budgeted

- **Rule:** Keep a line carrying a `file.ext:line` reference, the line after each such reference,
  and lines stating an assertion outcome, up to `failureDetailBudget` (6) lines per failure at
  `detailLineWidth` (200) columns, and count the rest as dropped.
- **Source:** `internal/bashcompress/compress.go:217` (`selectFailureLines`).
- **Assertion:** `TestFilterGoTestSaysWhatTheFailureBudgetDropped` in `internal/bashcompress/tests_test.go:135`.
- **Rejected:** Keeping only lines matching `failureDetailLine`; a Python traceback's
  `/tmp/tests/helper.py:12: ValueError` frame would be dropped with the code that failed.

---
### A silent command stays silent

- **Rule:** No filter may add lines the raw output does not support; `git add` returns false on both
  paths, so a silent success is never replaced by a confirmation.
- **Source:** `internal/bashcompress/git.go:641` (`filterGitConfirmation`).
- **Assertion:** `TestFilterGitConfirmationsLeaveSilentCommandsAlone` in `internal/bashcompress/git_test.go:242`.
- **Rejected:** Re-running `git diff --cached --shortstat` the way rtk does to print an
  `ok N files changed` line; the filter sees only the command the model ran and its captured bytes.

## Gaps

- No test drives `filterLs`'s `hasFlag('R', "recursive")` guard at `internal/bashcompress/files.go:42`: `ls -R` output is
  refused by the unparsable-line guard instead, which `TestFilterLsRefusesShapesItCannotRead` covers
  indirectly.
- `statusStagedCodes` maps `"both modifed"` (`internal/bashcompress/git.go:63`), a misspelling git never prints, while
  `statusUnmergedCodes` has the correct `"both modified"` (`internal/bashcompress/git.go:73`); no test covers a staged
  `both modified` entry, so that entry is dead.
- The plan lists `git show --patch` under the `git diff` task, but `filterGit` (`internal/bashcompress/git.go:17`)
  dispatches only `status`, `diff`, `log`, `add`, `commit`, `push`, `pull`, `fetch`, `stash`.
- The plan's task 2 asks for `CAP_ERRORS` (20); `internal/bashcompress/compress.go:8` declares only `capWarnings`,
  `capList`, and `capInventory`.

## Review

- `filterGitConfirmation`'s `add` branch (`internal/bashcompress/git.go:641`) returns false on both paths; check whether
  the non-empty case was meant to produce anything, since no test covers a non-empty `git add`.
- `filterSearch`'s `capped` branch (`internal/bashcompress/files.go:645`) applies the never-worse guard only when nothing
  was truncated, so `[N long lines truncated]` can push the result past the raw length; verify that
  is deliberate and not an oversight of `internal/bashcompress/compress.go:92`.
- `relativeAge` (`internal/bashcompress/git.go:587`) parses five layouts, two without a timezone; a `Date:` line whose
  offset does not match its layout may parse into the wrong zone without any test noticing.
- `filterSqlfluff` treats a `L:` line before any `== [` header as unreadable (`internal/bashcompress/tests.go:1108`) but
  skips unparsable `L:` lines after one; confirm a truncated header cannot move a file's findings
  onto the previous file.

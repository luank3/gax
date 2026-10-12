# Bash tool spec

## Scope

- Inputs: the `bash` and `task_wait` arguments (`command`, `timeout_seconds`, `background`, `name`,
  `raw`, `id`, `kill`), the `bash.*` and `task.*` settings, and an optional `DisplaySink`.
- Outputs: model-facing result text (preview plus status footer) and, for `bash`, a live byte
  stream to the display sink.
- Non-goal: the filter tables in `internal/bashcompress` that build a command's compact form.

## Invariants

### A write-shaped command is never exploration

- **Rule:** `isExplorationBash` answers false when `bashCommandHasDisqualifier` sees a command
  substitution, a background `&`, or a redirect other than `2>/dev/null` and `2>&1`, when
  `bashKnownCommandWrites` matches `-delete`, `-exec`, `--output`, `sed`'s `w`, or `awk -i inplace`,
  or when a segment names a program the classifier does not know.
- **Source:** `internal/tools/bash_classify.go:372` (`bashKnownCommandWrites`).
- **Assertion:** `TestIsExplorationBashRejectsWritesAndUnknownSegments` in
  `internal/tools/bash_classify_test.go:27`.
- **Rejected:** Trusting the program name; `find . -delete` shares a program with `find . -type f`.

---
### Segments split on statements and pipes

- **Rule:** `forEachBashSegment` splits on `&&`, `||`, `;`, newline, and carriage return as
  statements and on `|` as a pipe, and a `bashFilter` counts only after a producer in its statement.
- **Source:** `internal/tools/bash_classify.go:78` (`forEachBashSegment`).
- **Assertion:** `TestIsExplorationBashUpstreamFixtures` in
  `internal/tools/bash_classify_upstream_test.go:6`.
- **Rejected:** Classifying the whole line as one segment; `cat foo | tee out.txt` must not qualify.

---
### Only a provably no-op leading `cd` is stripped

- **Rule:** `stripBashCDPrefix` returns a nonzero offset only for `cd <target> && <rest>` where
  `<target>` resolves to the working directory, and 0 for relative paths, globs, and `$(...)`.
- **Source:** `internal/tools/bash_preprocess.go:45` (`stripBashCDPrefix`).
- **Assertion:** `TestStripBashCDPrefixOnlyWhenItIsProvablyTheCurrentDirectory` in
  `internal/tools/bash_test.go:258`.
- **Rejected:** Stripping `cd $HOME/proj &&` when `HOME` holds a space (bash_test.go:273).

---
### The rewrite changes only the `command` member

- **Rule:** `preprocessBashArguments` re-marshals the parsed object with the stripped command, so
  `background`, `name`, and unknown members keep their values.
- **Source:** `internal/tools/bash_preprocess.go:18` (`preprocessBashArguments`).
- **Assertion:** `TestPreprocessBashArgumentsRewritesOnlyTheCommand` in
  `internal/tools/bash_test.go:292`.
- **Rejected:** Emitting the stripped command text alone; bash_test.go:295 re-parses the object.

---
### Task names are validated before the command starts

- **Rule:** `bash` refuses a bad `name` before starting a process, answering
  `'name' must not look like an automatic task id (t<number>)`, `task name '%s' is already in use`,
  `'name' too long (max 32 characters)`, or `'name' may contain only letters, digits, '-' and '_'`.
- **Source:** `internal/tools/bash.go:564` (`taskManager.nameError`).
- **Assertion:** `TestBashRejectsInvalidAndDuplicateTaskNamesBeforeLaunching` in
  `internal/tools/bash_test.go:547`.
- **Rejected:** Validating in `adopt` after launch; the marker file must stay absent.

---
### A command that outlives its window detaches instead of dying

- **Rule:** On `waitTimedOut` with tasks enabled, `runBashContext` adopts the still-running process
  and answers `[detached as task %s after %s timeout]`, or `[detached as task %s]` for `background`.
- **Source:** `internal/tools/bash.go:784` (`runBashContext`).
- **Assertion:** `TestBashTimeoutDetachesAndWaitReportsNewOutput` in
  `internal/tools/bash_test.go:329`.
- **Rejected:** Killing at the timeout; the later `after-timeout` bytes must still arrive.

---
### Collection is incremental

- **Rule:** `collectTaskOutput` hands the model only bytes past `task.delivered` and advances that
  mark, so a repeat wait on a drained task answers `finished (exit 0)` plus `; no new output`.
- **Source:** `internal/tools/bash.go:1314` (`collectTaskOutput`).
- **Assertion:** `TestBashTimeoutDetachesAndWaitReportsNewOutput` in
  `internal/tools/bash_test.go:329` (second wait at bash_test.go:343).
- **Rejected:** Re-sending `snapshot(0)` each time; the repeat wait asserts `; no new output`.

---
### Overflow at the drain limit drops the crossing chunk

- **Rule:** In task mode the write whose total reaches `bashDrainLimit` is discarded while `total`
  keeps counting, and the overflow handler kills the process group with `SIGKILL`.
- **Source:** `internal/tools/bash.go:85` (`commandCapture.Write`).
- **Assertion:** `TestCommandCaptureTaskDropsChunkThatReachesDrainLimit` in
  `internal/tools/bash_output_test.go:119`.
- **Rejected:** Storing the crossing chunk; the test needs `stored == 4` at `total == 8`.

---
### Truncated output always names its recovery file

- **Rule:** A preview that dropped bytes carries `[output truncated: ...]` naming a spool file, and
  every preserved path stays readable until `Registry.Close` removes it.
- **Source:** `internal/tools/bash.go:1511` (`shapeCommandOutput`).
- **Assertion:** `TestBashTruncationKeepsFullOutputAvailableUntilRegistryCloses` in
  `internal/tools/bash_test.go:165`.
- **Rejected:** Trimming without saving; that file must hold `HEAD` through `TAIL`.

---
### Compression needs command ownership and a recoverable full text

- **Rule:** `compressCommandOutput` compresses only when `bash.compress` is on, `raw` is unset,
  `bashcompress.Compress` accepts the command (no unquoted shell operator), and, when the filter
  omitted lines, `preserveFull` returned a path appended as `[full output saved to <path>]`.
- **Source:** `internal/tools/bash.go:1427` (`compressCommandOutput`).
- **Assertion:** `TestBashCompressionSkipsCommandsItCannotOwn` in
  `internal/tools/bash_compress_test.go:86`.
- **Rejected:** Compressing `ls -l <dir> | head -3`; those bytes belong to another command.

## Gaps

- `[finished during launch; no task created]` and `[finished during launch; task %s not created]`
  (bash.go:767) have no asserting test; the nearest indirect coverage is
  `TestBashCompressionLeavesFailuresAlone` (bash_compress_test.go:246), a synchronous run.
- `[output limit reached: %d further bytes discarded, task stopped]` (bash.go:801),
  `[binary output suppressed: %s]` (bash.go:793), and `[output unavailable: spool write failed]`
  (bash.go:795) are unasserted; bash_output_test.go:119 covers only the capture fields behind them.
- `commandExitCode` returning -1 for a still-running task (bash.go:1297) has no direct test; the
  nearest indirect coverage is `TestBashCompressionNamesTheFullOutputOfACollectedTask`
  (bash_compress_test.go:209).
- bash_preprocess.go:16 says the original tool call stays in conversation history, but no test
  inspects the conversation item after preprocessing.
- docs/superpowers/plans/2026-10-11-busy-terminal-operations.md, the plan cited for this area,
  never mentions the bash tool, so these rules rest on 2026-10-10-bash-task-tools.md alone.

## Review

- `bashSedScriptWrites` (bash_classify.go:428) and `allowedBashStderrRedirectEnd`
  (bash_classify.go:179) are the classifier's loosest parsers, exercised only by the two tables.
- The overflow rule is asserted at the capture level, not through `runBashContext`; confirm the
  task-mode drop at bash.go:77 is the branch the test reaches.
- `preserveFull` (bash.go:293) owns spool lifetime; the raw fallback (bash.go:1441) and the literal
  `[output truncated]` for `savedPath == ""` (bash.go:1523) are unexercised.
- `waitOtherDone` (bash.go:1207) is checked only by substring matches in bash_test.go:493.

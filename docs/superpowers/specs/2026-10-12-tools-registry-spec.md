# Registry spec

## Scope

- Inputs: a tool call item (tool name plus raw JSON arguments) sent to `ExecuteWithDisplayContext`,
  an `imageInput` flag, and an optional `DisplaySink`.
- Outputs: a `conversation.Item` of kind `KindToolResult` holding the canonical model-facing text,
  plus `ToolPresentation` metadata and a `FallbackOutput` for the terminal.
- Also in scope: what `Definitions()` advertises, the `task_wait` kill path, and the concurrency
  contract of an `internal/tools.Registry` shared across calls.
- Non-goal: the internals of the bash, web, and skill tools beyond registration and that kill path.

## Invariants

### Read output is line-numbered and never renumbered

- **Rule:** `read` renders a returned line as `%6d` plus `→` plus the line's own bytes, so a slice
  keeps the file's absolute line numbers instead of restarting at 1.
- **Source:** `internal/tools/files.go:210` (`runRead` via `readTextSlice`).
- **Assertion:** `TestReadTextSlicesAndCapsOutput` in `internal/tools/registry_test.go:43`.
- **Rejected:** Numbering the returned slice from 1. The test requires `     2→second\n` for
  `offset=2, limit=1`, so slice-relative numbering is falsified.

---
### An over-cap whole-file read is refused, not quietly truncated

- **Rule:** With no `offset`/`limit`, `read` refuses an over-cap file: `%s is %d bytes; cap is %d.
  Pass offset/limit to read a slice, or use bash with grep/head/tail.`
- **Source:** `internal/tools/files.go:77` (`runRead`).
- **Assertion:** `TestReadTextSlicesAndCapsOutput` in `internal/tools/registry_test.go:43`.
- **Rejected:** Returning the first cap bytes. The test asserts `cap is 24` with no slice requested
  and `[truncated at 24 bytes` for a requested one (internal/tools/registry_test.go:68).

---
### Edit refuses an ambiguous match unless `replace_all` is set

- **Rule:** `edit` counts with `bytes.Count` and, when `old_string` matches more than once and
  `replace_all` is not true, answers `'old_string' matches %d places in %s` and writes nothing.
- **Source:** `internal/tools/files.go:505` (`runEdit`).
- **Assertion:** `TestEditRequiresUniqueExactMatchAndCanReplaceAll` in `internal/tools/registry_test.go:226`.
- **Rejected:** Replacing the first match and reporting a diff. The test re-reads the file and
  requires `foo\nfoo\nfoo\n` to survive the refusal.

---
### Write is a no-op for byte-identical content

- **Rule:** `write` returns an empty result when the new bytes equal the file's bytes, and otherwise
  installs them by renaming a temp file created in the target's directory, preserving its mode.
- **Source:** `internal/tools/write.go:14` (`writeWithDiff`).
- **Assertion:** `TestWriteCreatesAndUpdatesFiles` in `internal/tools/registry_test.go:180`.
- **Rejected:** Returning the diff unconditionally. The test requires empty result text for an
  unchanged write, so a no-op cannot be reported as an edit.

---
### Diffs are GNU-style unified diffs with sanitized bytes

- **Rule:** Hunk ranges include `@@ -0,0 +1 @@` and `@@ -1 +0,0 @@`; a final line without a newline
  gets `\ No newline at end of file`; NUL and invalid UTF-8 bytes become U+FFFD.
- **Source:** `internal/tools/diff.go:387` (`unifiedDiff`).
- **Assertion:** `TestUnifiedDiffUsesGNUHunkRangesAndHandlesMissingFinalNewline` in
  `internal/tools/write_diff_test.go:10`.
- **Rejected:** Returning raw bytes; write_diff_test.go:37 requires no NUL byte in the result.

---
### Line comparison is budgeted

- **Rule:** `diffContext.equal` returns false once the shared work budget is spent, so a region that
  exhausts it is emitted as wholly changed instead of split on further comparisons.
- **Source:** `internal/tools/diff.go:129` (`diffContext.equal`).
- **Assertion:** `TestBoundedMyersKeepsUnchangedLinesPairedForRandomInputs` in
  `internal/tools/write_diff_test.go:94`.
- **Rejected:** Comparing past the budget. `diffWorkLimit` bounds the fully-changed case, and the
  random-input test rejects any pairing of lines that no longer compare equal.

---
### Display bytes are presentation only

- **Rule:** The result text lives in `Result.Text`; bytes handed to the `DisplaySink` never replace
  it, and `OutputSummarizesDisplay` is set whenever any byte was displayed.
- **Source:** `internal/tools/registry.go:553` (`ExecuteWithDisplayContext`).
- **Assertion:** `TestExecuteWithDisplayStreamsBashAndTaskWaitOutput` in
  `internal/tools/bash_test.go:114`.
- **Rejected:** Forwarding streamed bytes as the tool result. `runBash` never sets
  `OutputSummarizesDisplay`, so the flag there comes from the wrapped sink (registry.go:524).

---
### task_wait kill shortens its own wait

- **Rule:** `task_wait` with `kill` forces its timeout to zero before waiting, then terminates the
  task with SIGTERM and escalates to SIGKILL after `bash.timeout_grace`.
- **Source:** `internal/tools/bash.go:1169` (`runTaskWaitContext`).
- **Assertion:** `TestTaskWaitCanStopBackgroundProcess` in `internal/tools/bash_test.go:480`.
- **Rejected:** Waiting out `task.wait_timeout` before killing. The test waits on `sleep 10` with
  `kill` and requires `killed (signal`, which only holds when the kill fires at once.

---
### The registry tolerates concurrent calls

- **Rule:** Every field mutated during a call is guarded (`activeMu`, `selectionMu`, `shellMu`,
  `spoolMu`) and a finished foreground command leaves no entry in `ListTasks`.
- **Source:** `internal/tools/registry.go:107` (`Registry`).
- **Assertion:** `TestRegistryConcurrentBashAndTaskCollection` in
  `internal/tools/concurrency_test.go:15`.
- **Rejected:** Treating the task manager as single-threaded. The test runs tools, note collection,
  task listing, and task stopping concurrently, and requires the task list to be empty at the end.

---
### Definitions are ordered and gated

- **Rule:** `Definitions()` emits read, edit, write, bash, task_wait, web_fetch, web_search, skill;
  `no_tasks`, `no_web`, and `no_skills` drop their tools from that list.
- **Source:** `internal/tools/registry.go:837` (`builtInDefinitions`).
- **Assertion:** `TestBuiltInToolDefinitions` in `internal/tools/registry_test.go:18`.
- **Rejected:** Advertising `skill` while refusing it at execution.
  `TestSkillToolDefinitionsAndDisable` (internal/tools/skill_test.go:171) requires the definition
  itself to be gone.

## Gaps

- The nil-registry branches (`internal/tools/registry.go:534` and `:569`) and the line-count
  truncation marker `[truncated at %d lines` (`internal/tools/files.go:104`) are pinned by
  `TestNilRegistryReportsUnavailable` and `TestReadWholeFileIsCappedAtTheLineLimit` in
  `internal/tools/registry_test.go`; only the byte variant was checked before (`internal/tools/registry_test.go:68`).
- `resolveWriteTarget`'s 32-step loop and its `too many symbolic links` error (write.go:103) are
  unexercised; write_test.go:12 covers only a link whose target has non-existent components.
- `writeWithDiff`'s failure paths (mkstemp, chmod, fsync, rename at write.go:45-77) have no test;
  `TestWriteCreatesAndUpdatesFiles` asserts the happy path only.

## Review

- The write rule's cited assertion covers the no-op; its mode and symlink half is pinned by
  `TestWriteFollowsDanglingSymlinkAndRespectsPermissions` (registry_test.go:200).
- The two read rules share one assertion (registry_test.go:42), which matches the refusal and
  truncation strings by substring; rewording either message would not fail the suite.
- The `waitOtherDone` branch of `runTaskWaitContext` (bash.go:1207) is the loosest path:
  `TestTaskWaitReturnsWhenAnotherTaskFinishes` (bash_test.go:493) checks two substrings only.
- `unifiedDiff`'s oversized-window branch (diff.go:397) and the budget-exhaustion path are not
  exercised by any test.

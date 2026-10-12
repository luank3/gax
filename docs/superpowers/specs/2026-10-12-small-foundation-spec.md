# Small foundation spec

## Scope

Process boundary and cross-cutting infrastructure: `internal/cli` (option parsing and one-shot input),
`internal/subagent` (recursion cap), `internal/version` (baseline commit), `internal/diag` (stderr
diagnostics), `internal/wiretrace` (redacted traffic traces), `internal/transcript` (model-facing
mirror). Output is either parsed `Options`, an error the caller prints, or a byte stream.
Non-goal: terminal rendering, which the terminal specs own.

---

### Options, not exits

- **Rule:** `cli.Parse`/`ParseWithPresets` never call `os.Exit` and never load config; they return
  `Options` plus an error, and a malformed option returns `*OptionError` whose `Diagnostic(program)`
  renders `<program>: <message>\nTry 'gax --help' for usage.\n`, defaulting the program to `gax`.
- **Source:** `internal/cli/cli.go:70`, `internal/cli/cli.go:57`
- **Assertion:** `TestParseLongOptionErrorsMatchGetoptDiagnostics` in `internal/cli/cli_test.go:211`
- **Rejected:** returning a plain `errors.New` for option syntax; the caller cannot then reproduce
  getopt's diagnostic shape.

---

### Long options accept unambiguous prefixes

- **Rule:** Long options resolve by exact name or unique prefix (`--pr` is `--print`), an ambiguous
  prefix lists every candidate as `option '--<spelling>' is ambiguous; possibilities: '--a' '--b'`,
  and an unknown one is `unrecognized option '--<spelling>'`.
- **Source:** `internal/cli/cli.go:268`
- **Assertion:** `TestParseSupportsUniqueLongOptionPrefixes` in `internal/cli/cli_test.go:192`
- **Rejected:** exact-match-only lookup; upstream getopt_long accepts prefixes.

---

### Boolean options reject attached values

- **Rule:** `--help`, `--version`, `--print`, `--continue`, `--json`, `--raw`, `--bare`, and
  `--no-session` reject `=value` with `option '--<name>' doesn't allow an argument`.
- **Source:** `internal/cli/cli.go:117`
- **Assertion:** `TestParseRejectsArgumentsOnBooleanOptions` in `internal/cli/cli_test.go:119`

---

### Selection, resume, and preset conflicts

- **Rule:** `--continue` and `--resume` together fail with `use only one of --continue / --resume`; a
  leading preset word plus `--preset` fails with `use only one of a leading preset name / --preset`;
  `--resume=` with an empty value fails with `--resume= requires a session id`; and every empty
  selection value fails with `--provider= requires a value` style text.
- **Source:** `internal/cli/cli.go:189`
- **Assertion:** `TestParseRejectsInvalidSelectionResumeAndPositionalForms` in `internal/cli/cli_test.go:45`
- **Rejected:** letting a later flag silently win, which would hide a mistaken invocation.

---

### Positional arguments need one-shot mode or a preset

- **Rule:** A non-one-shot positional argument fails with `positional arguments require -p / --print`;
  a single first-word positional that is not a preset instead reports
  `'<word>' is not a preset (presets: a, b)\nA prompt needs -p / --print. Try 'gax --help' for usage.`,
  listing only applicable preset names.
- **Source:** `internal/cli/cli.go:213`
- **Assertion:** `TestParseUnknownLeadingWordListsKnownPresets` in `internal/cli/cli_test.go:168`

---

### One-shot input rules

- **Rule:** `ReadPrompt` returns nil for interactive mode and for a promptless resume; otherwise it
  joins positional args with spaces, else reads all of stdin when it is not a TTY and strips at most
  one trailing `\n` plus a preceding `\r`, and errors `-p prompt is empty` / `-p requires a prompt
  (positional args or piped stdin)` when that leaves nothing on a fresh run.
- **Source:** `internal/cli/cli.go:231`
- **Assertion:** `TestReadPromptRemovesOnlyOneFinalNewline` in `internal/cli/cli_test.go:127`
- **Rejected:** trimming all trailing whitespace; a heredoc body's internal blank lines must
survive.

---

### Recursion cap fails closed

- **Rule:** `subagent.CheckDepth` allows empty or `< MaxDepth` (3) values and rejects `Val >= 3` with
  `subagent depth limit (3) reached — run the task directly instead of spawning another gax`;
  malformed or negative values are treated as `MaxDepth`, so `"invalid"` and `"-1"` reject and
  `NextChildDepth` clamps children at 3.
- **Source:** `internal/subagent/depth.go:11`, `internal/subagent/depth.go:26`
- **Assertion:** `TestCheckSubagentDepthMatchesRecursionCap` in `internal/cli/cli_test.go:228`
- **Rejected:** treating a malformed `GAX_SUBAGENT_DEPTH` as depth 0, which would let a corrupted
  inherited marker spawn unbounded nested runs.

---

### Diagnostics are single-line and counted

- **Rule:** `diag.Warn`/`diag.Error` write exactly one `gax: <formatted>\n` line to the writer,
  colored only when the writer is a terminal, and advance the process-wide `diag.Sequence()` once per
  emitted diagnostic; a nil writer emits nothing and does not advance.
- **Source:** `internal/diag/diag.go:34`
- **Assertion:** `TestWarningsAndErrorsWriteOneLineAndAdvanceSequence` in `internal/diag/diag_test.go:8`

---

### Traces redact registered secrets

- **Rule:** `wiretrace` replaces every registered secret with `<redacted>` in headers and bodies,
  matching the longest secret when several overlap at one position, redacts `Authorization`,
  `x-api-key`, and `api-key` headers unconditionally, and grows the fence beyond the longest backtick
  run inside fenced content.
- **Source:** `internal/wiretrace/trace.go:161`, `internal/wiretrace/trace.go:226`
- **Assertion:** `TestLoggerRedactsLongestOverlappingSecretAndFencesBackticks` in `internal/wiretrace/trace_test.go:48`
- **Rejected:** redacting only the first matching secret per position; a shorter secret would leave
  the tail of a longer credential in the file.

---

### Disabled tracing touches no file

- **Rule:** `Configure("")` disables the logger, and every record method is a no-op while disabled, so
  no file is created or written; `Configure(path)` truncates an existing destination.
- **Source:** `internal/wiretrace/trace.go:31`, `internal/wiretrace/trace.go:139`
- **Assertion:** `TestDisabledLoggerDoesNotCreateOrWriteAFile` in `internal/wiretrace/trace_test.go:73`

---

### Transcript mirrors the model-facing context

- **Rule:** `transcript.Log.Sync` appends only newly visible items, but reopens and rewrites the whole
  file when the header (`SystemPrompt` or tool definitions) changed or the history shrank, which is
  what selection changes, compaction, undo, and `/new` produce.
- **Source:** `internal/transcript/transcript.go:787`
- **Assertion:** `TestLogSyncAppendsAndResetRewritesContext` in `internal/transcript/transcript_test.go:192`
- **Rejected:** appending unconditionally; a compacted conversation would leave stale items in the
mirror.

---

### Parallel tool results render in call order

- **Rule:** `RenderItems` pairs each tool result with its call by `CallID` and marks it consumed, so a
  parallel batch prints call-then-result pairs in call order even though results are appended later
  in
  the item slice.
- **Source:** `internal/transcript/transcript.go:100`
- **Assertion:** `TestRenderPairsParallelToolResultsInCallOrder` in `internal/transcript/transcript_test.go:129`

---

---

## Gaps

- `wiretrace.Logger.Close` and `diag.Sequence` are exercised only inside broader assertions, so a
  regression in either would surface as a neighboring failure rather than a named one.
- `transcript.Init` returns an error for an unreadable destination; only the success path is tested
  (`TestInitTruncatesConfiguredTranscript`).
- `internal/cli` covers `CheckSubagentDepth` only inside `cli_test.go`, so the `internal/subagent`
  package's own table is the sole direct check of `NextChildDepth` clamping.
- `version.Value` is pinned by its own test, but `gax --version` output is asserted only as
  containing `gax ` (`internal/app/app_test.go:378`), so the printed value is unpinned.
- `internal/diag` color selection depends on `terminaltheme.Open`, whose palette comes from config;
  no test pins the colored branch.

## Review

- `internal/cli/cli.go:171` iterates runes for short flags while long options are resolved before it;
  check that combined short options (`-pc`) stay legal and that `-` alone stays positional.
- `internal/wiretrace/trace.go:175` scans for the earliest secret and breaks on the first position;
  confirm a secret appearing after a partial non-match cannot be skipped.
- `internal/transcript/transcript.go:791` keys the rewrite on `sameHeader` plus a length comparison,
  so an in-place edit that keeps the item count would not rewrite. No test covers that shape.

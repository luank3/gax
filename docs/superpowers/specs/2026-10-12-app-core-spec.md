# app.Run spec

## Scope

`app.Run(args []string, stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) int` is the
entry point: `cmd/gax/main.go` hands it `os.Args[1:]`, stdio, and a `stdinTTY` flag from
`os.Stdin.Stat()`. Run parses options, layers config and provider selection, then runs one turn or
a line REPL and returns the exit status. stdout carries the answer and JSONL records; stderr
carries diagnostics. Non-goal: the agent loop and slash-command implementations Run dispatches into.

## Invariants

### Outcome decides the exit status
- **Rule:** A completed run exits 0; `agent.LoopInterrupted` exits 130; `agent.LoopPaused` prints
  `gax: paused at a turn boundary` and exits 1; other non-complete outcomes exit 1, writing the
  error to stderr unless JSON mode records it in the result.
- **Source:** `internal/app/json_output.go:27`: `finishRun`.
- **Assertion:** `TestRunOneShotSIGUSR1PausesAndKeepsSessionResumable` in
  `internal/app/app_test.go:388`: wants exit status 1 and `"outcome":"paused"`.
- **Rejected:** Returning 0 for a paused run; the test wants exit 1 and a session kept for resume.

---

### Without `-p` or `--json`, Run runs the line REPL
- **Rule:** `options.OneShot` alone selects the single-turn path; otherwise Run runs the line REPL,
  which returns 0 at stdin EOF instead of reporting a read error.
- **Source:** `internal/app/app.go:790`: the `if options.OneShot` branch.
- **Assertion:** `TestRunInteractiveEOFLeavesUpstreamTrailingSeparator` in
  `internal/app/app_test.go:1364`: no `-p`, empty stdin, code 0, three trailing newlines.
- **Rejected:** Reporting EOF as `gax: read prompt: <err>`; the scanner reports no error at EOF.

---

### The banner picks a channel
- **Rule:** An interactive run writes the identity banner to stdout; a one-shot run writes
  `writeOneShotStartBanner` to stderr and leaves stdout carrying only the answer.
- **Source:** `internal/app/app.go:408`: the `options.OneShot && jsonOutput == nil` guard.
- **Assertion:** `TestOneShotPrintsContextStatsAfterAnswer` in `internal/app/app_test.go:323`:
  stdout is exactly `answer\n`, stderr starts `gax: openai-compatible · test-model\n\n`.
- **Rejected:** Putting the one-shot banner on stdout; the test compares stdout byte for byte.

---

### A one-shot run needs a model
- **Rule:** A one-shot run with no model writes
  `gax: no model selected for provider '<name>' — pass --model or set GAX_MODEL` and exits 1.
- **Source:** `internal/app/app.go:205`: the `model == nil && options.OneShot` guard.
- **Assertion:** `TestRunOneShotStillRequiresAModel` in `internal/app/app_test.go:975`: compares
  stderr exactly and wants code 1 with empty stdout.
- **Rejected:** Substituting a provider default here; app.go:202 does that only before the guard.

---

### The REPL notes a missing provider instead of failing
- **Rule:** With no provider, `run` writes `no provider selected — use /provider to choose one, then
  resend` to stdout and returns a completed loop result.
- **Source:** `internal/app/app.go:722`: the `provider == nil` branch of `run`.
- **Assertion:** `TestRunAllowsLoginCommandsWhenNoProviderIsAvailable` in
  `internal/app/app_test.go:1210`: wants code 0 and that note on stdout.
- **Rejected:** Exiting 1 when selection fails; that test requires 0 with no provider available.

---

### JSONL order is header, items, result
- **Rule:** JSON mode writes the session header first, each new item exactly once in order, then one
  `result` record whose fields keep the declared struct order.
- **Source:** `internal/app/app.go:1169`: `func (stream *jsonRun) finish`.
- **Assertion:** `TestRunJSONEmitsHeaderNewItemsAndResult` in `internal/app/app_test.go:3183`:
  line 1 is the `"session"` header, five or more lines, and result keys `type, outcome, text,
  turns, elapsed_ms, context_tokens, cost`.
- **Rejected:** Writing the result before pending items; `finish` drains the cursor first, and
  `TestRunJSONEmitsTaskExitNoteBeforeResult` (app_test.go:3403) pins that task notes precede it.

---

### JSON records stay unescaped one-liners
- **Rule:** Each record is encoded with `SetEscapeHTML(false)` and written as one newline-terminated
  line, flushed when the writer supports `Flush`.
- **Source:** `internal/app/app.go:1140`: `func (stream *jsonRun) record`.
- **Assertion:** `TestJSONRecordsLeaveHTMLCharactersUnescaped` in `internal/app/app_test.go:3344`:
  expects exactly `{"text":"<tag> & value"}\n`.
- **Rejected:** `json.Marshal` plus `fmt.Fprintln`; `Marshal` escapes `<`, `>`, and `&`.

---

### Signals escalate from cancel to kill
- **Rule:** The first SIGINT or SIGTERM cancels the run context; a second kills tool children,
  restores the terminal, and re-raises the signal; SIGQUIT restores and re-raises at once.
- **Source:** `internal/app/signals.go:14`: `watchTerminationEscalation`.
- **Assertion:** `TestRunOneShotSecondSIGINTEscalatesAndKillsBashGroup` in
  `internal/app/app_test.go:468`: wants SIGINT termination and a stopped bash heartbeat.
- **Rejected:** `os.Exit(128+signal)` on the first signal; the bash child would keep running.

---

### SIGUSR1 pauses one-shot runs only
- **Rule:** SIGUSR1 is registered only when `options.OneShot` is set and is consumed at a turn
  boundary, turning the run into `agent.LoopPaused`.
- **Source:** `internal/app/app.go:376`: the `if options.OneShot` guard around `signal.Notify`.
- **Assertion:** `TestRunOneShotSIGUSR1PausesAndKeepsSessionResumable` in
  `internal/app/app_test.go:388`: signals a blocked provider request and wants outcome `paused`.
- **Rejected:** Waiting for the loop to return; `takePauseSignal` (app.go:381) is consulted mid-run.

---

### Pager resolution is fixed
- **Rule:** `$PAGER` wins when its first shell word resolves via `exec.LookPath`; a `less` head gets
  ` -R` inserted after that word; otherwise `less`, then `more`, is used.
- **Source:** `internal/app/pager.go:105`: `resolvePager` (`addLessColorFlag` at pager.go:181).
- **Assertion:** `TestResolvePagerAddsLessRawColorFlag` in `internal/app/pager_test.go:208`: expects
  `less -R --quit-if-one-screen`.
- **Rejected:** Adding `-R` to any configured pager; `addLessColorFlag` compares
  `filepath.Base(head)` with `less` and leaves other commands unchanged.

## Gaps

- The pager failure branches in `internal/app/pager.go:29`, `:56`, and `:92` write `gax: <error>`
  plus a blank line to stdout and have no asserting test;
  `TestResolvePagerRejectsMissingConfiguredCommand` (`internal/app/pager_test.go:24`) covers only
  `resolvePager` itself.
- `runREPL`'s `return 130` for a canceled context (`internal/app/repl.go:62`) has no test; the
  nearest is `TestRunOneShotSecondSIGINTEscalatesAndKillsBashGroup`
  (`internal/app/app_test.go:468`), which is one-shot.
- The non-TTY prompt gate at `internal/app/repl.go:58`, which writes `> ` only when `inputTTY` is
  true, has no test; `TestRunInteractiveEOFLeavesUpstreamTrailingSeparator` covers the loop only.
- Plan text disagrees with behavior: `docs/superpowers/plans/2026-10-10-command-entrypoint.md:22`
  says `/provider`, `/model`, and `/effort` "reject arguments", yet
  `TestRunREPLSelectsSelectionByArgument` (`internal/app/app_test.go:1162`) applies one.

## Review

- `internal/app/json_output.go:29`: paused, interrupted, and other outcomes share one chain; the 130
  return reached through `finishRun` rather than a re-raised signal is the least-covered line.
- `internal/app/repl.go:288`: the `default` branch returns 1 and ends the REPL, but every
  `agent.LoopOutcome` is handled above it; confirm no outcome can reach it.
- `internal/app/selection.go:58`: the `--resume needs an id in this build` error is unreachable from
  Run, because `ResumeSelect` without an id is intercepted by the picker at app.go:118.
- `internal/app/app.go:644`: the run clock starts after `WaitModelMetadata`, and the only guard is
  `TestRunOneShotElapsedExcludesStartupModelProbe` in `internal/app/app_test.go:3261`.

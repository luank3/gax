# app command handler spec

## Scope

The handlers behind `/undo`, `/fork`, `/resume`, `/session`, `/copy`, `/tasks`, ctrl-o history,
and ctrl-t transcript, in `internal/app`: `handleSlash`, `handleUndo`, `handleFork`,
`pickHistoryCommand`, `pickSession`, `printSessionSummary`, `runCopyCommand`, `runTasksCommand`,
`showHistory`. Inputs: a `slash.Parsed`, the live `*agent.Session`, `*session.Log`,
`*config.Store`, stdio, and a TTY flag. Outputs: stdout notes and rows, stderr diagnostics, and
session-log or branch files. Non-goal: `internal/slash` grammar and `internal/app/app.go` order.

## Invariants

### Turn counts count typed prompts only
- **Rule:** Count turns with `typedUserTurnCount`, which counts an item only where
  `conversation.IsTypedPrompt` holds, so compact seeds and continuations are not turns.
- **Source:** `internal/app/history_commands.go:88`: `typedUserTurnCount`.
- **Assertion:** `TestTypedUserTurnCountSkipsNonPromptUserItems` in `internal/app/app_test.go:3144`.
- **Rejected:** Counting every `conversation.KindUser` item; that fixture holds three of them.

---

### `/undo` persists before it mutates
- **Rule:** Call `log.Undo(keep, cut)` before `state.Retire(cut)`; on failure print to stderr
  `gax: could not record the undo in the session file; conversation left unchanged` and return.
- **Source:** `internal/app/history_commands.go:72`: the `log.Undo` guard in `handleUndo`.
- **Assertion:** `TestRunREPLUndoRemovesLiveTailAndRetiresItInSessionLog` in
  `internal/app/app_test.go:2948`: live `first request,third request`, retired `second request`.
- **Rejected:** Retiring before logging; that note promises the conversation is unchanged.

---

### `/fork` needs a materialized session log
- **Rule:** Refuse `/fork` when the log is nil or `!log.Materialized()`, printing
  `gax: /fork needs session recording (it is disabled or unavailable)` to stderr.
- **Source:** `internal/app/history_commands.go:120`: the `log == nil || !log.Materialized()` guard.
- **Assertion:** `TestHandleForkRequiresSessionRecording` in `internal/app/app_test.go:3118`:
  nil result, unchanged `len(state.Items)`, and `len(state.Retired) == 0`.
- **Rejected:** Forking from live items alone when recording is off; the test passes a nil log.

---

### `/fork 0` clones the tip and drops source-only accounting
- **Rule:** On `/fork 0`, mark every copied item `Inherited`, clear `state.Retired`, reset
  `state.WorkedMS` to 0, and reset `state.LastUserTurnMS` to -1.
- **Source:** `internal/app/history_commands.go:143`: the `Inherited` loop, resets at :148 to :151.
- **Assertion:** `TestHandleForkZeroClonesTheCurrentTip` in `internal/app/app_test.go:3079`: two
  items, `Items[1].Inherited` set, `WorkedMS == 0`, `LastUserTurnMS == -1`, empty `Retired`.
- **Rejected:** Carrying source `Retired` into the branch; `internal/app/app_test.go:3007` wants
  `len(branch.Retired) == 0`.

---

### A bare `/undo` or `/fork` never guesses a count
- **Rule:** On a TTY the turn picker supplies `typedUserTurnCount(state.Items)-index`, titled
  `revert to before which message` or `fork before which message`; otherwise the handler prints a
  count hint, or `nothing to undo yet` / `nothing to fork yet` when there are no typed turns.
- **Source:** `internal/app/history_picker.go:55`: `pickHistoryCommand`.
- **Assertion:** `TestRunTTYForkWithoutArgumentPicksLastUserTurn` in
  `internal/app/live_tty_linux_test.go:376`: after Enter, the next request holds `first prompt`
  and not `second prompt`.
- **Rejected:** Treating a bare `/fork` as `/fork 1`; `InitialIndex: len(items)-1` maps to a
  `turnsBack` of 1, which is why that test sees the newest prompt dropped.

---

### `/tasks` validates before it touches the registry
- **Rule:** Validate the argument first and write usage to stdout: `usage: /tasks [kill <id>... |
  kill all]` for an unknown verb, `usage: /tasks kill <id>... | kill all` for a bare `kill`, with
  no call to `ListTasks` or `StopTasks`.
- **Source:** `internal/app/tasks_command.go:20`: the argument branch of `runTasksCommand`,
  messages at :24 and :36.
- **Assertion:** `TestRunTasksUsageErrorsAreDisplayedOnStdout` in `internal/app/app_test.go:1476`:
  both rows compare stdout exactly and require empty stderr.
- **Rejected:** Listing first and checking the verb afterwards; that test passes a nil `tools`, so a
  registry call before the usage return panics.

---

### `/copy` takes the newest non-empty assistant text
- **Rule:** Scan `state.Items` backwards for a `conversation.KindAssistant` item whose text is
  non-nil and non-empty, copy exactly that string, and report `copied %d %s to clipboard`
  (singular `byte` at length 1); with no such item print `no assistant response to copy`.
- **Source:** `internal/app/copy_command.go:30`: `lastAssistantResponse`, byte unit at :23.
- **Assertion:** `TestRunCopyCopiesLatestAssistantResponse` in `internal/app/app_test.go:1022`:
  the clipboard helper receives `latest answer` and stdout holds `copied 13 bytes to clipboard`.
- **Rejected:** Dropping the `*item.Text != ""` test; a trailing empty assistant item would end
  the scan.

---

### `/session` re-resolves effort before printing
- **Rule:** When `state.Model` is set, wait on `provider.WaitModelMetadata` and assign
  `state.Effort = provider.ResolveEffort(*state.Model, requestedEffort)` before rendering the
  provider row, so a stored `effort` override is clamped to the model's levels.
- **Source:** `internal/app/session_summary.go:56`: `printSessionSummaryWithStyle`, guard at :50.
- **Assertion:** `TestSessionSummaryResolvesEffortFromModelMetadata` in
  `internal/app/app_test.go:2845` prints `openai-compatible · test-model · high` for override `max`.
- **Rejected:** Printing `store.String("effort")` verbatim; `max` is absent from the model's
  `reasoning_options`, so the raw value is not a printable level.

---

### History resolves cursor controls before paging
- **Rule:** Buffer the rendered history and pass it through `terminalvt.Resolve` before writing to
  the pager, so carriage returns in stored text are applied instead of emitted.
- **Source:** `internal/app/pager.go:77`: `resolved := terminalvt.Resolve(raw.Bytes())`.
- **Assertion:** `TestShowHistoryResolvesModelCursorControlsBeforePaging` in
  `internal/app/pager_test.go:191`: `abcdef\rXY` pages as `XYcdef` with no `\r` left.
- **Rejected:** Streaming items straight into the pager command; a surviving `\r` lets model output
  overwrite pager lines, which the test forbids.

## Gaps

- `handleUndo`'s failure branch (`internal/app/history_commands.go:73`) has no test; nothing drives
  `log.Undo` to fail, so the `conversation left unchanged` note is never asserted.
- The `OriginSkipped` and `OriginRefused` markers and the forced `PreviewHead` in
  `internal/app/history_render.go:152` to `:164` have no fixture in any test.
- The `/resume` picker's empty-state note, `sessionPickerMaxEntries` cap, `relativeSessionTime`,
  and `sessionProvenance` (`internal/app/session_picker.go:40`, `:21`, `:75`, `:92`) have no direct
  test; nearest is `TestRunTTYSlashResumeSwitchesConversationAndExcludesCurrent`
  (`internal/app/live_tty_linux_test.go:1191`).
- The `kill` boundary clause `argument[4] != ' ' && argument[4] != '\t'`
  (`internal/app/tasks_command.go:23`) has no case: the table covers only `bogus` and `kill`.
- Plan text disagrees: the superseded note at
  `docs/superpowers/plans/2026-10-10-slash-command-front-end.md:10` says `/provider`, `/model`, and
  `/effort` "take no arguments", yet `TestRunREPLConsumesSlashCommands`
  (`internal/app/app_test.go:2064`) expects `/model fast` to apply.

## Review

- `internal/app/session_summary.go:36`: `printSessionSummary` passes `styled=true` even when
  redirected, so `/session` emits dim escapes on piped output;
  `TestSessionSummaryPreservesUpstreamRowStylingWhenRedirected` (`internal/app/app_test.go:1977`).
- `internal/app/history_commands.go:54`: the `inputTTY` hint branch is unreachable from Run, since
  `pickHistoryCommand` (`internal/app/app.go:945`) consumes a bare argument or cancels first.
- `internal/app/session_summary.go:51` and `:365` call `WaitModelMetadata(..., nil)`, so `/session`
  can block for `runtime.ModelMetadataWait` with no busy status or cancellation tick.
- `internal/app/history_render.go:61`: `postDispatchFrom` makes streamed items after a turn's last
  tool result render last; `internal/app/pager_test.go:80` asserts presence only, never order.

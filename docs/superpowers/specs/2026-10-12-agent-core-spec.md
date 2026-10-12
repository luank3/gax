# Agent core spec

## Scope

Inputs are a `Session` (item history, provider settings, advertised tools) plus the hook structs
`TurnOptions` and `LoopOptions` in `internal/agent`. Outputs are the mutated `Session.Items`
history and the `LoopTurn`/`LoopResult` records (outcome, `FinalItemsFrom`, `FinalItemsTo`,
`AbortMarkerPlaced`, `ErrorMessage`). The boundary is one session, one provider round-trip
(`RunTurn`), and the loop that repeats it (`RunLoop`). Non-goal: request bodies, wire parsing,
rendering, persistence, and compaction policy.

## Invariants

### Observe calls before dispatch

- **Rule:** Report every streamed `KindToolCall` in the turn's range through `ToolSeen` before
  dispatching any of them.
- **Source:** `internal/agent/loop.go:162`: `RunLoop`
- **Assertion:** `TestRunLoopObservesEveryToolCallBeforeDispatchingAny` in `internal/agent/loop_test.go:555`
- **Rejected:** Reporting each call from inside the dispatch loop as it runs; calls that are
  refused, skipped, or dropped by a failed turn would then never reach the frontend.

---

### Refuse calls when the session advertises no tools

- **Rule:** When `len(Session.Tools) == 0`, answer each call with origin `OriginRefused` and the
  text `error: tool calls are disabled in this session` instead of executing it.
- **Source:** `internal/agent/loop.go:191`: `RunLoop`
- **Assertion:** `TestRunLoopRefusesUndeclaredToolsThenContinuesToFinalAnswer` in
  `internal/agent/loop_test.go:183`
- **Rejected:** Dispatching through `ToolRegistry` when one is configured but the session
  advertises nothing; the model would receive results for tools it was never offered.

---

### Abort skips the rest of the tool batch

- **Rule:** After `SignalAbort`, each batch call that has not run yet is answered with
  `SkippedResult` (`[interrupted]`) and origin `OriginSkipped`, and at most one interrupt marker
  is added.
- **Source:** `internal/agent/loop.go:193`: `RunLoop`
- **Assertion:** `TestRunLoopAbortSkipsRestOfToolBatchWithoutDuplicateMarker` in `internal/agent/loop_test.go:447`
- **Rejected:** Appending an unconditional assistant marker after the batch; `MarkInterrupt`
  returns early when the tail already carries `OriginInterrupted` or `OriginSkipped`
  (`internal/agent/session.go:172`).

---

### Pause lands after the whole batch

- **Rule:** A `SignalPause` seen while dispatching a batch sets `pausePending`, and the loop
  returns `LoopPaused` only once every call in that batch has a result.
- **Source:** `internal/agent/loop.go:184`: `RunLoop`
- **Assertion:** `TestRunLoopPauseFinishesToolBatchBeforeReturning` in `internal/agent/loop_test.go:490`
- **Rejected:** Returning `LoopPaused` from inside the dispatch loop; history would keep a
  `KindToolCall` with no paired `KindToolResult`, which `AbsorbAbort` exists to prevent
  (`internal/agent/loop_turn.go:217`).

---

### A provider failure keeps assistant text only

- **Rule:** Repairing a failed turn calls `Turn.KeepText`, so the repaired range holds only
  `KindAssistant` items and no unrun `KindToolCall` is observed or answered.
- **Source:** `internal/agent/loop_turn.go:230`: `AbsorbAbort`
- **Assertion:** `TestRunLoopProviderFailureDropsUnrunCallBeforeObservation` in `internal/agent/loop_test.go:591`
- **Rejected:** Reusing the cancel repair here; it would keep the assembled call and pair a
  never-run call with a synthetic `[interrupted]` result.

---

### A cancellation keeps sealed reasoning and pairs every call

- **Rule:** Repairing a cancelled turn discards unsealed reasoning, keeps sealed reasoning and
  completed calls, and appends one `KindToolResult` with output `[interrupted]` and origin
  `OriginSkipped` per kept call; a turn holding only unsealed reasoning leaves no history.
- **Source:** `internal/agent/loop_turn.go:232`: `AbsorbAbort`
- **Assertion:** `TestAbsorbAbortPairsCompletedCallsAndEvaporatesThinkingOnlyCancel` in
  `internal/agent/loop_test.go:141`
- **Rejected:** Absorbing the partial turn unchanged; the repair exists so the next provider
  request sees replayable history (`internal/agent/loop_turn.go:217`).

---

### A retry discards the previous attempt's assembly

- **Rule:** An `EventRetry` resets the turn, so text streamed by the failed attempt never reaches
  history, while that attempt's usage is still recorded as its own `Attempt`.
- **Source:** `internal/conversation/turn.go:175`: `(*Turn).consume`
- **Assertion:** `TestRunTurnRetryDropsPartialAssemblyButKeepsAttemptUsage` in
  `internal/agent/loop_test.go:50`
- **Rejected:** Continuing the same assembly across attempts; the delivered item would then be
  `discard me` plus `keep me` instead of `keep me`.

---

### Requests start at the newest compaction seed

- **Rule:** `Session.Context` slices items from `conversation.ContextFloor`, the newest
  `OriginCompactSeed` user item, to the end of the history.
- **Source:** `internal/agent/session.go:128`: `Session.Context`
- **Assertion:** `TestSessionContextStartsAtNewestCompactionSeed` in
  `internal/agent/session_test.go:12`
- **Rejected:** Slicing at index 0; compaction appends its seed in place
  (`internal/agent/compact.go:185`) and leaves pre-compaction items live in `Session.Items`.

## Gaps

- The refusal text `error: tool calls are disabled in this session` (`internal/agent/loop.go:15`)
  has no asserting test; `internal/agent/loop_test.go:183` checks only `OriginRefused` and a
  non-nil `Output`.
- `RunLoop` escalates `transport.ErrCancelled` to `SignalAbort` when the checkpoint returns
  `SignalNone` (`internal/agent/loop.go:135`); the only cancellation test
  (`internal/agent/loop_test.go:353`) returns `SignalPause`, so the abort half is unexercised.
- The fallback diagnostic `provider stream ended before completion`
  (`internal/agent/loop_turn.go:87`) is never reached by a test; every stream stub emits either
  `EventDone` or `EventError`.
- `Retire` clears the shared backing array in place (`internal/agent/session.go:87`), so an
  earlier `Context()` must be discarded (`internal/agent/session.go:36`); no test asserts that
  invalidation, and `TestSessionRetireMovesItemsAndResetClearsHistory` checks only lengths.
- The `internal/conversation` package has one test, covering only the `ReasoningField` codec;
  `ContextFloor`, `UserTurnCut`, `KeepText`, and `DiscardReasoning` have no direct assertion and
  are covered only through `TestSessionContextStartsAtNewestCompactionSeed`,
  `TestSessionRetireMovesItemsAndResetClearsHistory`, and the two `AbsorbAbort` tests.

## Review

- `internal/agent/loop.go:307`: the `error: no tool executor is configured` fallback is pinned by
  `TestRunLoopAdvertisedCallWithoutExecutorReportsTheGap` (`internal/agent/loop_test.go`); it fires only
  with both `ToolCall` and `ToolRegistry` absent; no test reaches it, and its origin is `""`.
- `internal/agent/loop.go:156`: `pausedEmpty` suppresses the continuation boundary, but the
  `turn.HasUsage()` half of the condition on line 157 has no test of its own.
- `internal/conversation/turn.go:187`: `Consume` silently drops events after a terminal state;
  no test pins that, so a double `EventDone` is unverified.

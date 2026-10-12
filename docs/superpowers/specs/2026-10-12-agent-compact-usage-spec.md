# Agent compaction and usage spec

## Scope

Inputs: `*agent.Session` history, a `runtime.Provider`, and `CompactOptions`.
Outputs: `CompactOutcome`, `KindTurnUsage` footers, `SessionStats`, and the `/session` rows.
Boundary: the trigger, the seed, footer construction, and the totals these files own.
Non-goal: provider account usage shown by `/usage` (`internal/slash/slash.go:54`) and its hooks.

## Invariants

### The trigger is a ceiling on the context window percentage

- **Rule:** `CompactOverThreshold` reports true only when `contextTokens` reaches
  `ceil(contextLimit * thresholdPercent / 100)`; invalid inputs report false.
- **Source:** `internal/agent/compact.go:86` `CompactOverThreshold`
- **Assertion:** `TestCompactOverThresholdUsesCeilingAndValidatesInputs` in
  `internal/agent/compact_test.go:62`
- **Rejected:** `contextLimit / 100 * thresholdPercent`, which truncates toward zero; the source
  rounds the remainder up (`internal/agent/compact.go:93`), so truncation compacts early.

---

### A successful compaction seeds the context with one summary item

- **Rule:** `CompactComplete` leaves a `KindTurnBoundary`, then exactly one `KindUser` item whose
  text is `compactSeedPreamble` plus the summary and whose `Origin` is `OriginCompactSeed`.
- **Source:** `internal/agent/compact.go:183` `CompactConversation`
- **Assertion:** `TestCompactConversationAddsSummarySeedAndCompactionUsage` in
  `internal/agent/compact_test.go:17`
- **Rejected:** storing the summary as a `KindAssistant` item; `ContextFloor` moves the request
  start only for `KindUser` items whose `Origin` is `OriginCompactSeed`.

---

### Compaction usage is recorded but never becomes the context snapshot

- **Rule:** attempt footers are appended with `Origin` set to `conversation.OriginCompaction`, all
  but the last before the boundary and the last after the seed, and `LastContextTokens` skips them.
- **Source:** `internal/agent/compact.go:180` `CompactConversation`; skip at
  `internal/agent/session.go:245`
- **Assertion:** `TestCompactConversationRejectsToolCallsAndRetriesWithoutExecutingThem` in
  `internal/agent/compact_test.go:83`, and
  `TestSessionUsageQueriesIgnoreCompactionFootersAndInheritedRetiredUsage` in
  `internal/agent/session_test.go:124`
- **Rejected:** letting the summarizing request define the context reading; `/session` would then
  report the scratch request's tokens as the live context.

---

### Provider-reported cost wins over the catalog estimate

- **Rule:** a supplied `Cost >= 0` becomes `CostTotal` with `CostEstimated` false; a nil or
  negative `Cost` clears both, and an estimate fills only a nil `CostTotal`.
- **Source:** `internal/agent/usage.go:26` `AddTurnUsage`
- **Assertion:** `TestAddTurnUsageKeepsExactProviderCostAndUnknownFields` in
  `internal/agent/usage_test.go:66`, estimate in
  `TestAddTurnUsageEstimatesCatalogCostAndRecordsProvenance` (`internal/agent/usage_test.go:15`)
- **Rejected:** preferring the catalog price; `/session` would print a `~` prefix over an exact
  charge (`internal/app/session_summary.go:150`) and overwrite a billed amount.

---

### Unknown token counts never produce a priced footer

- **Rule:** the catalog price is computed only when `tokenReported(InputTokens) ||
  tokenReported(OutputTokens)`; with both counts `-1` the footer keeps `CostInput`, `CostOutput`,
  and `CostTotal` nil.
- **Source:** `internal/agent/usage.go:35` `AddTurnUsage`
- **Assertion:** `TestAddTurnUsageDoesNotEstimateWhenBothTokenCountsAreUnknown` in
  `internal/agent/usage_test.go:89`
- **Rejected:** passing `-1` through as 0; `catalog.Price` would return a zero estimate
  (`internal/agent/usage.go:42`) and the session would claim a spend it never estimated.

---

### Totals separate live, undone, and inherited items

- **Rule:** user turns, tool calls, and usage come from live `Items`, `UndoneUserTurns` from
  `Retired`, retired usage still feeds `Total` and `Models`, and `Inherited` items feed only
  `Inherited` and `InheritedUserTurns`.
- **Source:** `internal/agent/stats.go:83` `CollectStatsWithRateLookup`; split at
  `internal/agent/stats.go:99`
- **Assertion:** `TestCollectStatsSeparatesLiveUndoneAndInheritedAccounting` in
  `internal/agent/stats_test.go:16`, and `TestRunREPLSessionShowsRecordedUsageSummary` in
  `internal/app/app_test.go:2795`
- **Rejected:** folding inherited usage into `Total`; the source session paid that spend, and
  `/session` prints it on the `inherited` row (`internal/app/session_summary.go:155`).

---

### Missing prices flag the totals instead of inventing spend

- **Rule:** a footer with no cost and no reported tokens still adds one `Requests`; a footer with
  no cost otherwise sets `SpendEstimated`, and `Unpriced` when tokens were reported with no rate.
- **Source:** `internal/agent/stats.go:198` `accountUsage`
- **Assertion:** `TestCollectStatsCountsDurationOnlyRequestWithoutInventingSpend` in
  `internal/agent/stats_test.go:65`
- **Rejected:** setting `Unpriced` for duration-only footers; that triggers a metadata retry
  (`internal/app/session_summary.go:364`) for a request that never reported tokens.

---

### Every compact outcome maps to one note

- **Rule:** each `CompactOutcome` prints one dim note: `CompactComplete` prints
  `conversation compacted`, `CompactEmpty` prints `nothing to compact`, and `CompactProviderError`
  falls back to `stream failed` when `ErrorMessage` is empty.
- **Source:** `internal/app/compact_command.go:38` `runCompactCommand`
- **Assertion:** `TestRunCompactUsesFocusAndReplacesHistoryWithSummarySeed` in
  `internal/app/app_test.go:1494`, and `TestRunCompactWithoutHistoryUsesRecordedStyle` in
  `internal/app/app_test.go:2194`
- **Rejected:** printing outcomes through the live renderer instead of `compactNote`
  (`internal/app/compact_command.go:102`); the notes go to `stdout` with their dim wrapper.

## Gaps

- The ceiling adjustment at `internal/agent/compact.go:93` has no asserting case; every row of the
  threshold table divides the limit by 100 evenly.
- The `CompactNoSummary` outcome (`internal/agent/compact.go:59`) and the `stream failed` fallback
  (`internal/app/compact_command.go:46`) have no asserting test; nearest is
  `TestRunCompactWithoutHistoryUsesRecordedStyle` (`internal/app/app_test.go:2194`).
- The `Unpriced = true` assignment (`internal/agent/stats.go:211`) is never asserted; the tests
  naming `Unpriced` assert false (`internal/agent/stats_test.go:70`, `:138`, `:159`).
- The negative `Cost` reset (`internal/agent/usage.go:26`) is unasserted;
  `TestAddTurnUsageKeepsExactProviderCostAndUnknownFields` covers nil and `0.012` only.
- The plan `docs/superpowers/plans/2026-10-09-agent-usage.md:5` scopes only per-attempt usage
  footers and lists no compaction task, yet `internal/agent/compact.go` owns compaction accounting.

## Review

- `internal/agent/compact.go:90`: the trigger number is what auto-compaction rests on, and the
  rounding-up path is invisible to the tests named above.
- `internal/agent/usage.go:26`: `CostTotal` and `CostEstimated` hinge on nil versus negative, and
  the cost fallback at `internal/agent/stats.go:171` reads both back, unexercised.
- `internal/app/session_summary.go:364`: the second metadata pass runs only when `Unpriced` is set,
  a state no test produces.
- `internal/app/session_summary.go:446`: `costDisplayMinimum` decides whether a per-category cost
  is printed at all, and neither branch has a test.

# Provider hooks and helpers spec

## Scope

- Inputs: DeepSeek `/models` and `/user/balance`, OpenRouter `/key` and `/credits`, OpenCode Go
  `/usage`, mock script files, and raw model-ID lists.
- Outputs: `Model` metadata, `UsageReport` values as plain terminal text, ordered ID slices, and
  the stream events the mock emits for one scripted or interactive turn.
- Boundary: `internal/provider/{deepseek,openrouter,opencodego,mock,modelsort,usagerender}`.
- Non-goal: hook dispatch in `internal/provider/runtime` and the wire parsers under
  `internal/provider/{openai,anthropic}`.

## Invariants

### Unreported model metadata stays unknown
- **Rule:** Leave a capability `catalog.SupportUnknown` when its list is missing, null, or not an
  array, and `catalog.SupportNo` when a valid list omits it; leave `EffortsKnown` false unless
  `effort.supported_levels` is a JSON array, which then yields an effort list starting with `none`.
- **Source:** `internal/provider/openrouter/models.go:147` (`capabilityFromArray`) and
  `internal/provider/deepseek/models.go:37` (`ParseModel`).
- **Assertion:** `TestParseModelDistinguishesUnknownAndUnsupportedCapabilities` in
  `internal/provider/openrouter/models_test.go:22` and `TestParseModel` in
  `internal/provider/deepseek/models_test.go:10`.
- **Rejected:** Reading `"supported_levels":"high"` as a one-entry ladder; the sparse
  `TestParseModelKeepsMissingAndMalformedMetadataUnknown` case keeps `EffortsKnown` false.
---
### DeepSeek balances are filtered, capped, and default available
- **Rule:** Keep at most four rows whose `currency` is non-empty and whose `total_balance` starts
  with a digit or `-` and parses finite, blank a `granted_balance` unless it parses positive, treat
  any `is_available` other than the bare JSON `false` as available, and fail with
  `unrecognized balance response shape (no balances)` when no row survives.
- **Source:** `internal/provider/deepseek/usage.go:55` (`ParseUsage`), row cap at
  `internal/provider/deepseek/usage.go:59`.
- **Assertion:** `TestParseUsageFiltersAndCapsBalances` in
  `internal/provider/deepseek/usage_test.go:14`; the availability default and the shape error are
  asserted by `TestParseUsageDefaultsAvailabilityAndRejectsNoBalances` at `:37`.
- **Rejected:** Omitting a row whose grant is negative; the test expects
  `Balance{Currency: "EUR", Total: "3.25"}` with an empty grant.
---
### Usage query failures use fixed, credential-free text
- **Rule:** Missing keys fail with `no DEEPSEEK_API_KEY configured`, `no OpenRouter API key
  configured`, or `no OPENCODE_API_KEY configured`; 401 responses with `DeepSeek rejected the
  configured API key (401)`, `OpenRouter rejected the configured API key (401)`, or `opencode
  rejected the configured API key (401)`; other failures with `failed to fetch usage from <url>`.
- **Source:** `internal/provider/deepseek/usage.go:88`, `internal/provider/openrouter/usage.go:119`,
  and `internal/provider/opencodego/usage.go:94`.
- **Assertion:** `TestQueryUsageDoesNotExposeCredentialOnUnauthorized` in
  `internal/provider/deepseek/usage_test.go:80` and
  `TestQueryUsageRejectsMissingCredentialAndKeyResponseErrors` in
  `internal/provider/openrouter/usage_test.go:80`.
- **Rejected:** Composing the message from the transport error or the request headers; both tests
  assert the configured key substring is absent from `err.Error()`.
---
### OpenRouter hides key-like labels and clamps credit remainders
- **Rule:** Drop a `label` starting with `sk-`, render `free tier` only when `is_free_tier` is the
  literal JSON `true`, and report `credits` only when `total_credits` parses positive, clamping the
  remainder at `$0.00`.
- **Source:** `internal/provider/openrouter/usage.go:90` (`WriteUsageReport`), credits block at
  `internal/provider/openrouter/usage.go:74`.
- **Assertion:** `TestParseUsageFormatsKeyAndCredits` in
  `internal/provider/openrouter/usage_test.go:15` requires the line
  `  credits $0.00 of $10.00 remaining` when `total_usage` exceeds `total_credits`.
- **Rejected:** Printing `label` verbatim; `TestParseUsageKeepsSafeLabelsAndSkipsMissingValues` at
  `internal/provider/openrouter/usage_test.go:31` requires the heading `openrouter · team-key`.
---
### OpenCode Go windows keep source order and strict UTC reset times
- **Rule:** Emit at most eight windows in `/usage` object order, skipping any member without a
  numeric `percent` and a `resetsAt` matching `YYYY-MM-DDThh:mm:ss[.fff]Z` with a calendar-valid
  day, and fail with `unrecognized usage response shape (no usage windows)` when none survive.
- **Source:** `internal/provider/opencodego/usage.go:49` (`ParseUsage`), timestamp check at
  `internal/provider/opencodego/usage.go:208`.
- **Assertion:** `TestParseUsagePreservesWindowOrderAndUTCResetTimes` in
  `internal/provider/opencodego/usage_test.go:17` and
  `TestParseUsageSkipsMalformedWindowsAndCapsAtEight` at `:39`.
- **Rejected:** `time.Parse(time.RFC3339, resetsAt)`; the tests require an offset-bearing
  `2026-08-24T00:00:00+02:00` and the non-existent `2026-02-31T00:00:00Z` to be skipped.
---
### Rendering strips controls, clamps percentages, and follows the locale
- **Rule:** Strip terminal control sequences from every label, note, and value before printing,
  clamp a window percentage into `0`..`100`, and draw the 20-cell bar with `█` and `░` only when
  `terminalocale.HaveUTF8()` reports UTF-8, otherwise with `#` and `-`.
- **Source:** `internal/provider/usagerender/text.go:92` (`StripLineControls`), clamp at
  `internal/provider/usagerender/text.go:54`, glyphs at `internal/provider/usagerender/text.go:59`.
- **Assertion:** `TestWriteWindowUsesLocaleAppropriateProgressGlyphs` in
  `internal/provider/usagerender/text_test.go:10` and
  `TestWriteUsageReportClampsAndSanitizesWindowRows` in
  `internal/provider/opencodego/usage_test.go:60`, which requires `100% used` from percent `130`.
- **Rejected:** Deleting only the byte after `\x1b`; the sanitize tests also pass `\x1b(B` and an
  OSC sequence terminated by BEL, and require a newline to collapse to a space.
---
### Model IDs order version, then snapshot, then variant
- **Rule:** Compare IDs token by token so a numeric continuation precedes a bare ID, which precedes
  a four-digit-or-longer numeric run, which precedes a word; numeric runs compare newest first and
  words compare ASCII-case-insensitively.
- **Source:** `internal/provider/modelsort/modelsort.go:23` (`CompareIDs`), tokenizer at
  `internal/provider/modelsort/modelsort.go:54`.
- **Assertion:** `TestCompareIDsVersionSnapshotVariantOrder` in
  `internal/provider/modelsort/modelsort_test.go:8` and `TestSortIDsUpstreamFixtures` at `:43`.
- **Rejected:** `sort.Strings`; byte order puts `babbage-001` before `babbage-002`, while
  `internal/provider/modelsort/modelsort_test.go:22` requires the higher numeric run first.
---
### The mock advances one turn per stream and warnings never abort it
- **Rule:** Play the turn after the stored cursor from the captured script path, advancing that
  cursor only on a completed turn, and return `transport.Response{Cancelled: true}` with
  `transport.ErrCancelled` when the tick callback reports cancellation; unknown or argument-less
  directives write `gax mock: unknown directive: <line>` or
  `gax mock: 'tool' needs name and JSON args: <line>` to the diagnostic writer and keep playing.
- **Source:** `internal/provider/mock/mock.go:58` (`Stream`), cursor advance at
  `internal/provider/mock/mock.go:132`, warnings at `internal/provider/mock/mock.go:209`.
- **Assertion:** `TestCancelledScriptTurnDoesNotAdvance` in `internal/provider/mock/mock_test.go:80`
  and `TestScriptWarningsMatchUpstreamDiagnostics` at `:120`.
- **Rejected:** Advancing the cursor before the turn finishes; the cancellation test replays the
  first turn and expects it again.

## Gaps

- `usagerender.StripLineControls` has no direct test; `internal/provider/usagerender/text_test.go`
  only asserts bar glyphs, so it is covered indirectly by the report tests named above.
- No test passes a non-nil `tick` or asserts the `usageTimeout` and `maxBodySize` bounds, so the
  plan constraint to keep the query cancellable and bounded to 30 seconds is unverified.
- The cited plans disagree with the code: `2026-10-10-deepseek-provider-parity.md` defers DeepSeek's
  parser and `/user/balance` report to the hooks plan, which is `Superseded` and says the behavior
  "was removed from the Go port at the time".
- The mock's done event stop reason `end_turn` (`internal/provider/mock/mock.go:346`) is unasserted;
  `TestScriptedTurnsDecodeDirectivesAndExhaust` only inspects usage on that event.
- `parsePricing` caps `overrides` at `catalog.MaxTiers` (4) but the OpenRouter tests supply a single
  override, so the cap is untested in the package that owns it.

## Review

- Check the digit-count boundary in `nextToken` (`internal/provider/modelsort/modelsort.go:68`): a
  run of four or more digits changes the token kind, so `babbage-002` at
  `internal/provider/modelsort/modelsort_test.go:22` and `tts-1` at `:27` sort by opposing rules.
- `writeWindow` recomputes the label column as `usageBarColumn + usageBarWidth`
  (`internal/provider/usagerender/text.go:72`) instead of measuring `"  %-7s "`; the two must stay
  equal or wrapping fires at the wrong width, and only the width-30 case is asserted.
- DeepSeek availability accepts any spelling except bare `false`
  (`internal/provider/deepseek/usage.go:55`), so a string `"false"` or a numeric `0` reads as
  available, and no case covers either.

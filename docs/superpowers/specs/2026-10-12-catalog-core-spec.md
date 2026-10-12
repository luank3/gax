# Catalog spec

## Scope

`internal/catalog` resolves pricing and capability metadata for one provider/model pair from the
`catalog.models` node of a `config.Store`, a validated models.dev snapshot, a bounded cache file,
and the `catalog.url` and `catalog.refresh` settings. It returns an `Entry` plus a found flag and
an estimate from `Price`, and exposes the refresh controls. Non-goal: it never picks the wire.

## Invariants

### Config beats catalog config beats snapshot

- **Rule:** Resolve provider-scoped config first, then catalog-ID config, then the snapshot,
  keeping the first value seen for each field independently.
- **Source:** `internal/catalog/catalog.go:116` (`Lookup`)
- **Assertion:** `TestLookupMergesRuntimeProviderCatalogAndSnapshotFields` in
  `internal/catalog/catalog_test.go:11`
- **Rejected:** Merging whole structs so the last source wins; the test expects `CostInput` 99 from
  config sitting next to `CostOutput` 8 from the snapshot.

---
### Unfilled fields stay unknown, not zero

- **Rule:** Leave unfilled fields at their sentinels (`CostInput` -1, `ContextWindow` 0,
  `ImageInput` `SupportUnknown`), read JSON `null` rates as unknown, and report no match from
  `Lookup` while `hasMetadata` is false.
- **Source:** `internal/catalog/catalog.go:69` (`unknownEntry`)
- **Assertion:** `TestLookupPreservesUnknownAndExplicitOffMetadata` in
  `internal/catalog/catalog_test.go:37`; `TestNullRatesStayUnknown` in
  `internal/catalog/catalog_test.go:87`
- **Rejected:** Treating a missing or `null` cost as rate 0; `memberRate` returns -1 for `null`, so
  a zero would price the model as free.

---
### Declared off values survive later sources

- **Rule:** Keep explicit off declarations (`reasoning` false yields `EffortsKnown` with an empty
  `Efforts`, `interleaved` false yields `InterleavedDeclared` with `InterleavedField` "",
  `"tiers": []` yields `TiersDeclared` with no tiers) instead of refilling them.
- **Source:** `internal/catalog/catalog.go:214` (`fillModel`)
- **Assertion:** `TestLookupPreservesUnknownAndExplicitOffMetadata` in
  `internal/catalog/catalog_test.go:37`;
  `TestTiersParseContextSelectorsAndEmptyConfigDisablesSnapshotTiers` in
  `internal/catalog/catalog_test.go:98`
- **Rejected:** Skipping an empty `tiers` array so snapshot tiers apply anyway; the test requires
  `len(entry.Tiers) == 0` while `CostInput` stays 2.

---
### Invalid input never replaces installed state

- **Rule:** Validate before assigning, rejecting a body that is not a JSON object of providers with
  a `models` object, and rejecting a cache file beyond `maxBytes` with
  `catalog cache exceeds %d bytes`.
- **Source:** `internal/catalog/catalog.go:92` (`parseSnapshot`)
- **Assertion:** `TestInvalidSnapshotDoesNotReplaceLastValidSnapshot` in
  `internal/catalog/catalog_test.go:73`; `TestLoadCacheIsBoundedAndPreservesLastSnapshot` in
  `internal/catalog/lifecycle_test.go:42`
- **Rejected:** Assigning `catalog.snapshot` before validation, or reading the cache without a cap;
  after each rejection the previous snapshot still resolves `CostInput` 1.

---
### Dialects are canonical or explicitly unsupported

- **Rule:** Map `api` to `openai-completions`, `openai-responses`, or `anthropic-messages`, map any
  other declared value to `unsupported`, and without an `api` derive the dialect from the model
  `provider.npm` ahead of the provider `npm`, mapping only `@ai-sdk/openai-compatible`,
  `@ai-sdk/openai`, and `@ai-sdk/anthropic`.
- **Source:** `internal/catalog/catalog.go:366` (`canonicalAPI`)
- **Assertion:** `TestLookupPreservesUnknownAndExplicitOffMetadata` in
  `internal/catalog/catalog_test.go:37`
- **Rejected:** Falling back to the provider-level npm when the model-level npm is unknown; the
  `unsupported` case must stay `unsupported`, not quietly become `openai-responses`.

---
### Only context tiers with a positive size count

- **Rule:** Keep tiers whose `tier.type` is `context` and whose `size` parses positive, stop after
  `MaxTiers` (4) entries, and reprice the whole request at the rates of the highest threshold
  strictly below the input token count.
- **Source:** `internal/catalog/catalog.go:268` (`fillTiers`)
- **Assertion:** `TestTiersParseContextSelectorsAndEmptyConfigDisablesSnapshotTiers` in
  `internal/catalog/catalog_test.go:98`; `TestPriceUsesContextTiersAndSeparatesSurchargeWrites` in
  `internal/catalog/catalog_test.go:127`
- **Rejected:** Repricing only the tokens above the threshold; 300000 input and 100000 output
  tokens at the 200k tier cost 2.8, the tier rate applied to every token.

---
### Unknown rates inherit, cache writes replace or surcharge

- **Rule:** Return -1 when the resolved input or output rate is negative, let an unknown cache read
  inherit the input rate and an unknown one-hour write inherit twice input, and let cache writes
  replace input token billing only when `CacheWriteReplacesInput` is true (`CostCacheWrite`
  negative or at least `CostInput`).
- **Source:** `internal/catalog/catalog.go:477` (`CacheWriteReplacesInput`)
- **Assertion:** `TestPriceUsesContextTiersAndSeparatesSurchargeWrites` in
  `internal/catalog/catalog_test.go:127`; `TestPriceUnknownRatesAndReplacementWrites` in
  `internal/catalog/catalog_test.go:146`
- **Rejected:** Subtracting surcharge writes from input; the test keeps `UncachedInputTokens` at
  298000 with 1000 cache writes charged separately.

---
### Refresh runs once, is age-gated, and keeps the last good state

- **Rule:** Consume the single refresh attempt on the first `PrefetchWithOptions` call, load the
  cache through `LoadCache` first, fetch only when `catalog.url` is set, `catalog.refresh` is
  positive, and the cache file's age reaches `catalog.refresh`, leave the cache file and snapshot
  untouched on failure, and report the stale age from `StaleDays` once, then 0.
- **Source:** `internal/catalog/lifecycle.go:67` (`PrefetchWithOptions`)
- **Assertion:** `TestPrefetchLoadsFreshCacheWithoutFetching` in
  `internal/catalog/lifecycle_test.go:61`; `TestPrefetchRefreshesStaleCacheAsynchronously` in
  `internal/catalog/lifecycle_test.go:78`; `TestFailedRefreshKeepsCacheAndReportsStaleAgeOnce` in
  `internal/catalog/lifecycle_test.go:105`
- **Rejected:** Reporting stale age on every poll or fetching while the cache is young; the fresh
  cache test sees zero requests and the second `StaleDays` call must return 0.

## Gaps

- `merge` at `internal/catalog/catalog.go:398` has no callers (`rg '\bmerge\('` matches only the
  definition), so its fallback order has no asserting test; the live path is `fillModel`.
- The transport branch of `fetchSnapshot` at `internal/catalog/lifecycle.go:139` is unasserted:
  `TestFailedRefreshKeepsCacheAndReportsStaleAgeOnce` returns HTTP 200 with an unparseable body.
- The one-shot guard at `internal/catalog/lifecycle.go:73` has no double-call test; the nearest
  indirect use is `internal/provider/client/client_test.go:168`.
- `RoutesModels` at `internal/catalog/catalog.go:144` has no test in this package; the nearest
  indirect test is `TestCatalogAPIHintsRouteOnlyAfterExplicitRules` in
  `internal/provider/runtime/runtime_test.go:1124`.

## Review

- `Wait`, `Drain`, and `Shutdown` are covered only by immediate returns:
  `TestDrainCancelsFetchAndShutdownIsSafe` passes `0` and no test flips the `Wait` tick mid-wait.
- The comparison `inputTokens <= tier.ContextThreshold` at `internal/catalog/catalog.go:456` is
  exclusive; a request of exactly 200000 tokens keeps base rates and no test pins that boundary.
- `StaleDays` compares `cacheGeneration` against the value captured at fetch start; an increment
  outside `fetchSnapshot` would silently suppress the warning.
- The early return at `internal/catalog/lifecycle.go:103` consumes the one-shot attempt when
  `catalog.url` is empty, so a store without a URL permanently disables refresh for the process.

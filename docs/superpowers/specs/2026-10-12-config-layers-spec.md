# Store spec

## Scope

`internal/config` owns the layered settings store: a run override, conversation overrides,
environment lookups, `state.json`, `config.json`, and the `DefaultSettings()` registry merge
into one resolved value plus the tier that supplied it, and persist calls write those tiers
back. Inputs are JSON bytes or key/value setters; outputs are resolved values, typed getter
results, or write errors. Non-goal: provider field vocabulary and credentials, owned by
`internal/provider/config`.

---
### Resolution walks six tiers

- **Rule:** A scalar resolves in tier order (run override, conversation, environment, state file,
  config file, registry default); `Source` names the winner; an empty value is skipped unless its
  setting declares `KeepEmpty`, and `(default)` stops the walk at its tier to yield that setting's
  registry default, or nil when it has none.
- **Source:** `internal/config/config.go:242` (`resolve`), `internal/config/config.go:238`
  (`Source`), `internal/config/config.go:297` (`applyDefault`)
- **Assertion:** `TestStoreResolutionAndSources`, `TestStoreNestedDottedScalarsAndDefaultSentinel`,
  and `TestProviderBoundSelectionAndEmptyValues` in `internal/config/config_test.go`
  at lines 13, 44, and 64.
- **Rejected:** skipping the sentinel; the config tier below would return `6060`.

---
### `model` and `effort` stay bound to their provider

- **Rule:** A `model` or `effort` stored beside a `provider` applies only while that provider is
  active; ids compare after `canonicalProviderID`, so `llama.cpp` equals `llamacpp`.
- **Source:** `internal/config/config.go:278` (`bindingAllows`), `internal/config/config.go:290`
- **Assertion:** `TestProviderBoundSelectionAndEmptyValues` in `internal/config/config_test.go:64`.
- **Rejected:** comparing raw ids; the state tier spells the provider `llama.cpp`, so the alias
  would stop matching and the config-file model would win.

---
### A failed config load clears the tier and blocks writes

- **Rule:** Invalid JSON or a non-object root clears the tier being loaded and returns an error, and
  later config writes fail with `config file could not be loaded; refusing to overwrite it`.
- **Source:** `internal/config/config.go:95` (`LoadConfig`), `internal/config/persist.go:57`
- **Assertion:** `TestInvalidLoadClearsOnlyThatTier` and
  `TestPersistConfigRefusesUnusableFilesAndKeepsOtherValues` in
  `internal/config/config_test.go` at lines 105 and 416.
- **Rejected:** writing anyway; the test requires the pre-existing bytes on disk.

---
### A flat dotted key outranks the nested spelling

- **Rule:** A flat key such as `providers.demo.port` wins over the nested object, and every persist
  deletes that flat spelling before writing the nested form.
- **Source:** `internal/config/config.go:405` (`objectGet`), `internal/config/persist.go:279`
- **Assertion:** `TestStoreNestedDottedScalarsAndDefaultSentinel` and
  `TestPersistStateAtomicallyWritesNestedKeysAndUpdatesStore` in
  `internal/config/config_test.go` at lines 44 and 289.
- **Rejected:** writing the nested value while the stale flat key survives; the next load would
  resolve the stale value over the fresh one.

---
### Persist is atomic and commits memory last

- **Rule:** Every persist writes the tier through a temp file in the destination directory and
  chmods it `0600`, renames it over the resolved symlink target, replaces the in-memory tier only
  after the write succeeds, and leaves the directory mode alone.
- **Source:** `internal/config/persist.go:418` (`writeOrderedAtomic`),
  `internal/config/persist.go:437`, `internal/config/persist.go:47`
- **Assertion:** `TestPersistStateAtomicallyWritesNestedKeysAndUpdatesStore`,
  `TestPersistDoesNotChangeExistingDirectoryPermissions`, and
  `TestPersistSelectionPreservesSameProviderDefaultsAndRollsBackFailure` in
  `internal/config/config_test.go` at lines 289, 335, and 362.
- **Rejected:** mutating the tier first; the rollback test blocks the write and requires the old
  provider.

---
### Preset saves validate before writing anything

- **Rule:** `PersistPreset` rejects a name failing `ValidPresetName`, a missing provider, an unknown
  tint (`unknown tint`), an unreadable `@path` prompt file, and a state-defined name, leaving
  `config.json` untouched.
- **Source:** `internal/config/persist.go:81`, `internal/config/persist.go:90`,
  `internal/config/persist.go:93`, `internal/config/persist.go:161`
- **Assertion:** `TestPersistPresetValidatesNameAndTintBeforeWriting`, `TestValidPresetName`,
  `TestPersistPresetRejectsUnreadablePromptFiles`, and
  `TestPersistPresetRefusesStateDefinitionAndInvalidPresetBlock` in
  `internal/config/config_test.go` at lines 539, 589, 563, and 502.
- **Rejected:** overwriting when state defines the name; state outranks config, so the save would
  never load.

---
### Typed getters re-parse strictly, then fall back

- **Rule:** `Int`, `Size`, `Tokens`, and `Duration` re-parse the resolved text with strtol's leading
  whitespace and sign rules, scale `k` by 1024 for sizes and 1000 for tokens, and fall back to the
  registry default when the value is invalid or outside `Min`/`Max`; `Setting.Valid` uses the same
  parse.
- **Source:** `internal/config/config.go:485` (`parseNonnegativeInt`),
  `internal/config/config.go:573` (`scaled`), `internal/config/config.go:653` (`parseDuration`)
- **Assertion:** `TestTypedParsingUsesBoundsAndUpstreamUnits`,
  `TestIntParsingMatchesUpstreamStrtolAndCIntBounds`, and
  `TestDurationKeepsMillisecondPrecisionBeyondTimeDurationRange` in
  `internal/config/config_test.go` at lines 121, 142, and 173.
- **Rejected:** `strconv.Atoi` after `strings.TrimSpace`; it accepts `6 `, which the table test
  requires to fall back to the default `4`.

---
## Gaps

- `MarkPresetWarning` (`internal/config/config.go:139`) has no test in `internal/config`; nearest
  indirect test is `TestUnknownLeadingWordWarnsOnceForInvalidPresetDefinitions` in
  `internal/app/app_test.go:97`.
- `PresetJSON` and `PresetExists` (`internal/config/config.go:318`, `:342`) have no direct test;
  `TestLoadPresetCoercesBooleanAndNumberScalarsLikeUpstream` in `internal/app/app_test.go:2447`
  reaches `PresetJSON` through `loadPreset`.
- The state half of the failed-load rule is unasserted: `internal/config/config_test.go:116` checks
  only that malformed JSON errors, never that the state tier is then empty.
- The cited plan says "all 64 ordered upstream setting entries"
  (`docs/superpowers/plans/2026-10-09-config-provider-registry.md:46`) while
  `TestSettingValidationAndDefaultRegistry` pins 69 entries at
  `internal/config/config_test.go:189`.

## Review

- The `fileUnusable` refusal appears twice (`internal/config/persist.go:57`, again at `:84`), but
  only the `PersistConfig` copy has a test; nothing loads a broken config and calls `PersistPreset`.
- No test in `internal/config` asserts a `conversation` or `config` source string;
  `internal/config/config_test.go:78` sets a conversation provider only to steer `bindingAllows`.
- `PresetJSON` reads nested `presets` members before the flat `presets.<name>` form
  (`internal/config/config.go:322`, `internal/config/config.go:331`), the reverse of `objectGet`'s
  precedence at `internal/config/config.go:410`.

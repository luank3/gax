# Provider runtime spec

## Scope

- Inputs: a provider id, the layered `config.Store`, and its `registry.Definition`; later calls add
  a model id, an effort, and a session id.
- Outputs: `*Provider` endpoint, wire, credential, and availability state, plus headers,
  `ExtraBody`, `RequestSettings`, model metadata, and `ListModels` results.
- Boundary: `internal/provider/runtime` over `registry` and `provider/config`; it does metadata
  HTTP but no completion request. Non-goal: request encoding and SSE translation, owned by
  `internal/provider/client` and the wire packages.

## Invariants

### A pinned provider keeps its shipped endpoint
- **Rule:** Ignore `providers.<id>.base_url` when the definition is `Pinned`, keep the shipped
  endpoint, and record a warning containing `base_url is pinned to`.
- **Source:** `internal/provider/runtime/runtime.go:1772`, the `!definition.Pinned` gate.
- **Assertion:** `TestPinnedProviderCannotBeRedirectedOrRewired` in
  `internal/provider/runtime/runtime_test.go:27`.
- **Rejected:** honoring the configured URL; a config file could then redirect a first-party
  credential to another host.

---
### Missing endpoint and credential reasons are fixed strings
- **Rule:** Report `AvailabilityUnconfigured` with the definition's `UnconfiguredReason` when no
  base URL resolves, and `AvailabilityMissingCredential` with `<APIKeyEnv> not set` when a key is
  required but absent.
- **Source:** `internal/provider/runtime/runtime.go:222`, `Resolve`.
- **Assertion:** `TestMissingConfigurationAndCredentialsHaveStableReasons` in
  `internal/provider/runtime/runtime_test.go:593`.

---
### Credentials never reach diagnostics through formatting
- **Rule:** Hold the request credential in `secret`, whose `String`, `GoString`, and `Format`
  methods all write `[redacted]`.
- **Source:** `internal/provider/runtime/runtime.go:181`.
- **Assertion:** `TestPinnedProviderCannotBeRedirectedOrRewired` in
  `internal/provider/runtime/runtime_test.go:27`.
- **Rejected:** returning the raw key from `String()`; `%+v` on a copied value also reaches
  `Format`, so one unguarded method leaks the key.

---
### Probe-backed readiness means reachable, not 2xx
- **Rule:** A provider in `AvailabilityNeedsProbe` becomes `Ready()` whenever its probe URL
  answers, whatever the status code; only a transport failure with no HTTP status keeps it
  unavailable.
- **Source:** `internal/provider/runtime/runtime.go:461`.
- **Assertion:** `TestProbeAvailabilityChecksReachabilityRatherThanHTTPStatus` in
  `internal/provider/runtime/runtime_test.go:224`.
- **Rejected:** requiring a 2xx status; the test handler answers 404 and the provider must still
  become `Ready()`.

---
### A selected-model probe requires a model
- **Rule:** Refuse to start or run a selected-model probe when `model == ""`, issuing no HTTP
  request in that case.
- **Source:** `internal/provider/runtime/runtime.go:614`, the guard in `ProbeModel`.
- **Assertion:** `TestModelProbeDoesNotListModelsWhenNoModelIsSelected` in
  `internal/provider/runtime/runtime_test.go:425`.

---
### Model wire selection: rule, then catalog hint, then default
- **Rule:** Return the wire of the first matching `providers.<id>.model_apis` glob; with no match
  use the catalog API hint, return an empty wire for the hint `unsupported`, else the provider's
  own wire.
- **Source:** `internal/provider/runtime/runtime.go:1381`, the rule loop in `WireForModel`.
- **Assertion:** `TestCatalogAPIHintsRouteOnlyAfterExplicitRules` in
  `internal/provider/runtime/runtime_test.go:1124`.
- **Rejected:** letting a later narrower glob win; `TestModelAPIRulesRouteByFirstMatchingGlob` at
  `internal/provider/runtime/runtime_test.go:1061` orders `acme/*` before `acme/gpt-4`.

---
### A session placeholder never expands to empty
- **Rule:** Substitute `ProcessSessionID()` when a caller has no conversation id, so `{session_id}`
  in configured headers always expands to a non-empty value.
- **Source:** `internal/provider/runtime/runtime.go:1462`, the fallback in `ConfiguredHeaders`.
- **Assertion:** `TestConfiguredHeadersFallBackToProcessSessionIDWithoutAConversation` in
  `internal/provider/runtime/runtime_test.go:1544`.
- **Rejected:** expanding to an empty string when recording is off;
  `internal/provider/registry/registry.go:104` records that the OpenCode gateway rejects requests
  without its session header.

---
### Effort requests clamp to supported levels
- **Rule:** Clamp a requested effort to a supported level: an exact match, else the closest
  supported level at or below the request, else the closest above; unknown requests fall back to
  `DefaultEffort` clamped, or nil.
- **Source:** `internal/provider/runtime/runtime.go:1247`, `clampEffort`.
- **Assertion:** `TestResolveEffortClampsUnsupportedRequestsToNearestSupportedLevel` in
  `internal/provider/runtime/runtime_test.go:1257`.
- **Rejected:** passing `max` through unchanged; the test model supports only `low` and `high` and
  expects `high`.

---
### Required reasoning without a field name warns and stays required
- **Rule:** When `reasoning_required` is true and no field name resolves, keep `ReasoningRequired`
  true, leave `ReasoningField` nil, and warn once that the required reasoning is not sent.
- **Source:** `internal/provider/runtime/runtime.go:856`, the warning block in `RequestSettings`.
- **Assertion:** `TestDeepSeekRequiredReasoningReplayAndOverride` in
  `internal/provider/runtime/runtime_test.go:1422`.
- **Rejected:** clearing the flag to silence the warning; the test asserts the flag stays true while
  the field is nil.

## Gaps

- `Resolve`'s error strings have no asserting test: `unknown provider %q` at
  `internal/provider/runtime/runtime.go:228` and `provider %q: unsupported api %q` at `:240`; the
  nearest indirect test is `internal/app/app_test.go:1304`, which wraps the first one.
- The `"no base_url"` reason at `internal/provider/runtime/runtime.go:306` is unreached by tests:
  every tested provider ships an `UnconfiguredReason`, and the invalid-`{port}` branch at `:1788`
  returns the same reason.
- The 32000 fallback in `resolveMaxTokens` at `internal/provider/runtime/runtime.go:923` has no
  asserting test; `TestRequestSettingsResolveAnthropicLimitsThinkingAndCache` in
  `internal/provider/runtime/runtime_test.go:1463` covers only the model-limit cap.
- `Provider.RestartDeferredModelProbe` at `internal/provider/runtime/runtime.go:584` has no
  asserting test; `TestCodexModelProbeSkipsTokensNearExpiry` at
  `internal/provider/runtime/runtime_test.go:888` asserts only the deferral.
- Plan text contradicts the code: `docs/superpowers/plans/2026-10-09-provider-resolution.md:5`
  promises resolution "without doing network I/O", but `ProbeAvailability` and `ProbeModel` at
  `internal/provider/runtime/runtime.go:441` and `:612` both perform HTTP.

## Review

- Rules 1 and 3 both lean on `internal/provider/runtime/runtime_test.go:27`; the protocol half of
  the pin is gated separately at `internal/provider/runtime/runtime.go:1551`, which no other test
  covers.
- `ModelMetadata` at `internal/provider/runtime/runtime.go:972` merges catalog, configured, and
  listed values per field through `mergeProbedModel` at `:1048` and `mergeListedRates` at `:1109`;
  `storeProbedModel` at `:1031` never writes `listedIDs`, so a probe adds no completion candidate,
  as `internal/provider/runtime/runtime_test.go:690` asserts.
- `filterEffortLevels` at `internal/provider/runtime/runtime.go:1296` drops `minimal` on the
  Anthropic wire; the ladder is `effortLadder` at `:98`, asserted only by
  `TestEffortLevelsUsesProviderLadderAndModelRestrictions` at
  `internal/provider/runtime/runtime_test.go:1181`.
- `{port}` expansion at `internal/provider/runtime/runtime.go:1780` is pinned only for `llamacpp`
  and `ollama` (`internal/provider/runtime/runtime_test.go:200`, `:575`); `Registry.ConfigValue` at
  `internal/provider/registry/registry.go:215` has no caller and no test.

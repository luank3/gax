# Model Wire Routing Plan

**Goal:** Resolve a provider's request wire per model using ordered `model_apis` glob rules, while preserving the provider default and wildcard behavior.

**Architecture:** Extend `internal/provider/runtime` with immutable parsed routing rules and helpers that select a wire and endpoint for a model. Keep catalog metadata inference for a later slice.

**Tech:** Go 1.25 standard library; no new dependencies.

## Task 1: Close runtime review findings

**Files:** `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Add a regression for secret leakage when a resolved provider is formatted as a copied value, then use a value-receiver formatter.
- [x] Make nil resolver stores use registered defaults without reading the host environment; expand local provider port defaults.
- [x] Isolate tests that assert missing credentials or local port defaults from the host environment.

## Task 2: Implement ordered model wire rules

**Files:** `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Test first-match precedence, canonical and short wire names, wildcard matching across slashes, bracket classes, default fallback, malformed rules, and per-model endpoints.
- [x] Parse `model_apis` in declaration order, ignore invalid members with warnings, and resolve each selected wire to its endpoint path.
- [x] Run package tests and the full repository checks.

## Review focus

- Rules keep JSON insertion order, with the first matching pattern winning.
- `*` matches slash-separated model identifiers as in `fnmatch(..., 0)`.
- Invalid configuration does not change the provider's default wire.
- Formatting never includes credentials, including for copied values.
- Nil configuration does not expose unresolved `{port}` placeholders or inherit ambient environment state.

# Catalog Core Plan

**Goal:** Parse and merge user model metadata with a loaded models.dev snapshot, calculate usage costs, and use catalog API hints for mixed-protocol providers.

**Architecture:** Add `internal/catalog` as a foreground-owned metadata store backed by `config.Store` plus a validated in-memory snapshot. Extend provider runtime to consult catalog API hints only for catalog-routed providers and after explicit `model_apis` rules.

**Tech:** Go 1.25 standard library; no new dependencies.

## Task 1: Parse and merge model metadata

**Files:** create `internal/catalog/catalog.go` and `catalog_test.go`.

- [x] Test unknown defaults, snapshot parsing, model API hints, config precedence by runtime and catalog IDs, and invalid snapshot replacement.
- [x] Run the focused package test and confirm the API is absent.
- [x] Implement field-wise config/snapshot merging and metadata-presence results.

## Task 2: Price model usage

**Files:** extend `internal/catalog/catalog.go` and `catalog_test.go`.

- [x] Test cache replacement versus surcharge accounting, one-hour rates, unknown rates, and context tiers.
- [x] Implement tier selection and per-category cost splits.

## Task 3: Route catalog-backed model wires

**Files:** `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Test catalog API hints, explicit-rule precedence, provider/catalog config precedence, unsupported selectors, and default fallback.
- [x] Consult catalog hints only for `api: catalog` or non-empty `model_apis` routing declarations.
- [x] Run package and full repository checks.

## Review focus

- User config wins field by field over the catalog-ID config, which wins over the snapshot.
- A declared empty field/list remains distinct from absent metadata where the format requires it.
- Unknown model selectors do not silently select a different protocol.
- Pricing uses the highest context threshold exceeded and accounts for surcharge-style writes without subtracting them from input.
- Explicit glob rules win before catalog hints; unmatched catalog-routed models fall back to the provider default only when the catalog has no selector.

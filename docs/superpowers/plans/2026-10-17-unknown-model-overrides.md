# Unknown-model context and vision overrides plan

**Goal:** Models missing from models.dev (e.g. `agnes-3.0-flash` on a generic
OpenAI-compatible endpoint) keep unknown context/vision by design — generic
`/models` listings retain IDs without inventing capabilities — so the
documented manual overrides must actually work.

**Architecture:** `Provider.ContextWindowForModel` and
`Provider.ImageInputForModel` are the single choke point for the `%` display,
auto-compact, and image gating (`internal/app`, `internal/agent`). They now
check the live `config.Store` first: `context_limit` via `Tokens()` wins over
auto-detect, and `image_input` `on`/`off` wins over per-model detection while
`auto` (the default) keeps the existing metadata behavior. Per-model pins via
`catalog.models.<provider>.<model>` (`limit.context`, `modalities.input`)
keep working for the exact-model case.

**Spec:** `context_limit` overrides auto-detect; `image_input` auto detects
per provider/model — both are live `/config` keys, so they must be read on
every `ForModel` call, not cached.

## Constraints

- No third-party modules; honor the existing tri-state vision convention
  (1 yes / 0 no / -1 unknown, unknown sends images).
- Generic listings still invent nothing; overrides apply at read time only and
  never write back into catalog or listed metadata.
- `auto` changes nothing; invalid values fall through to metadata.

## Tasks

### 1. Wire the overrides into the provider

- [x] `ContextWindowForModel` returns `store.Tokens("context_limit")` when > 0.
- [x] `ImageInputForModel` returns yes/no for `image_input` on/off, metadata for auto.
- [x] Verify focused runtime tests.

### 2. Pin the behavior with tests

- [x] `TestUnknownModelManualOverrides`: catalog-models pin for
  `agnes-3.0-flash`, `context_limit` over unknown, `image_input`
  on/off/auto over unknown.
- [x] Keep `TestGenericModelListingDoesNotInventModelMetadata` green.

### 3. Verify the tree

- [x] Run `make lint test-race`.

## Verification

- [x] Focused runtime tests pass.
- [x] `make lint test-race` passes.

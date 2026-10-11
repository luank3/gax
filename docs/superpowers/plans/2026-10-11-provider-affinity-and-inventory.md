# Provider Session Affinity and Inventory Trim Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make requests that belong to no recorded conversation carry a real session id, so the
OpenCode Go gateway's required `x-opencode-session` header is never empty; retire the
`opencode-zen` and `agnes` shipped defs.

**Architecture:** `runtime.ProcessSessionID()` lazily generates one UUID v4 per process — the port
of upstream `provider_process_session_id()` (`src/providers/provider_config.c:237`). Every place
that expands the `{session_id}` placeholder (`Provider.ConfiguredHeaders`, and therefore
`Headers`/`MetadataHeaders`) substitutes it when the caller has no conversation id, and
`internal/provider/client` resolves the same value once per request so the prompt cache key and
the headers agree on one affinity id.

**Tech Stack:** Go standard library (`crypto/rand`, `encoding/hex`, `sync`), the existing provider
runtime and client.

**Spec:** upstream resolves the fallback at its HTTP layer
(`src/providers/http_provider.c:388` for requests, `:596`/`:602` for metadata and usage) because
the OpenCode gateway pins a conversation to one upstream by that header and rejects requests
without one (`src/providers/registry.c:26`). gax resolves it where it expands the placeholder.

## Constraints

- No third-party modules; the id is generated with `crypto/rand` and hex-encoded.
- A conversation's session id always wins; the fallback is stable within a process and never
  empty, so no provider sees `x-opencode-session:` with no value.
- The cache key and the headers of one request must come from the same resolved id.
- `--no-session`, `GAX_NO_SESSION`, and the dev-provider `auto` mode keep working: recording stays
  off, and nothing is written to the state directory.
- Retired ids fail with the ordinary `unknown provider '<id>' (supported: ...)` diagnostic. No
  compatibility shim, no hidden config-defined stub, no re-add of the removed `/usage` hook.
- `opencode-go` keeps its definition, its catalog id, and its `/usage` backend unchanged.

## Tasks

### 1. Fall back to a process session id

- [x] Write failing runtime tests for `ConfiguredHeaders("")`, `MetadataHeaders("")`, and for a
      conversation id overriding the fallback.
- [x] Add `ProcessSessionID()` and substitute it when `ConfiguredHeaders` gets no session id.
- [x] Verify `internal/provider/runtime` tests.

### 2. Keep the client's affinity id consistent

- [x] Write a failing client test for `Prepare` without a conversation: the OpenCode Go session
      header and the OpenRouter `prompt_cache_key`.
- [x] Resolve the id once per request (`affinityID`) and pass it to `RequestSettings` and the
      headers.
- [x] Verify `internal/provider/client` tests.

### 3. Retire the OpenCode Zen and Agnes AI defs

- [x] Write failing registry tests: the shipped order without `opencode-zen` and `agnes`, and a
      lookup for each retired id.
- [x] Delete both defs, keeping the shared OpenCode header helper (with upstream's why-comment)
      for `opencode-go`.
- [x] Re-point the generic model-listing test at `openai-compatible` so the "listed IDs keep
      unknown capabilities and no invented pricing" coverage survives.
- [x] Update the `unknown provider` diagnostic in the selection test.

### 4. Document the change

- [x] Update the README provider inventory and table, the changelog, and the built-in
      `customize-gax` skill's credential list.
- [x] Run `make lint test-race`.

## Verification

- [x] Run `make lint`.
- [x] Run `make test-race` (51 packages).
- [x] Confirm `--no-session` one-shot runs against the mock provider still record nothing under
      `$XDG_STATE_HOME`/`$HOME`.

## Record

- `ProcessSessionID()` is a `sync.Once` UUID v4: the fallback is per process, exactly like
  upstream's static buffer, so consecutive requests of one run share one affinity id and a later
  run gets a fresh one.
- The fallback lives at the expansion boundary rather than at each caller, so model listing and
  `/usage` requests (which pass `""` on purpose) get the same non-empty header as model requests.
- Retiring `agnes` also drops its catalog-less listing behavior; that path is still covered by the
  `openai-compatible` test, which asserts unknown capabilities and unpriced metadata.
- Upstream still ships both defs; gax diverges deliberately because the Zen endpoint no longer
  serves and the Agnes AI endpoint is unusable. `docs/superpowers/plans/2026-10-10-*.md` keep
  their historical task lines and carry a dated note pointing here.

## Upstream divergence

`src/providers/registry.c` ships `opencode-zen` (pay-as-you-go) and `agnes` (Agnes AI, with an
`agnes_parse_model` hook gax never ported). gax ships neither: the Zen gateway no longer works, and
the Agnes AI hub is not usable. Everything else in the inventory — including `opencode-go` and its
usage windows — stays aligned with the baseline.

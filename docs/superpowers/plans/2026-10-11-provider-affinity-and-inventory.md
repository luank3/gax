# Provider Session Affinity and Inventory Trim Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make requests that belong to no recorded conversation carry a real session id, so the
OpenCode Go gateway's required `x-opencode-session` header is never empty; give Codex's streamed
requests the identity headers the endpoints expect; retire the `opencode-zen` and `agnes` shipped defs.

**Architecture:** `runtime.ProcessSessionID()` lazily generates one UUID v4 per process — the port
of `provider_process_session_id()`. Every place
that expands the `{session_id}` placeholder (`Provider.ConfiguredHeaders`, and therefore
`Headers`/`MetadataHeaders`) substitutes it when the caller has no conversation id, and
`internal/provider/client` resolves the same value once per request so the prompt cache key and
the headers agree on one affinity id.

**Tech Stack:** Go standard library (`crypto/rand`, `encoding/hex`, `sync`), the existing provider
runtime and client.

**Spec:** the fallback belongs where a request is built, because the OpenCode gateway pins a
conversation to one backend by that header and rejects requests without one. gax resolves it where
it expands the placeholder.

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
- The Codex identity headers stay scoped to Codex: no other provider's request grows a session or
  beta header, and the metadata/usage paths keep their own Accept handling.

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
- [x] Delete both defs, keeping the shared OpenCode header helper (with its why-comment)
      for `opencode-go`.
- [x] Re-point the generic model-listing test at `openai-compatible` so the "listed IDs keep
      unknown capabilities and no invented pricing" coverage survives.
- [x] Update the `unknown provider` diagnostic in the selection test.

### 4. Port the Codex session headers

- [x] Write failing header assertions: the streamed Responses request carries `session-id`,
      `x-client-request-id`, and `OpenAI-Beta: responses=experimental`; the non-streamed metadata
      request carries `Accept: application/json` and none of the three.
- [x] Add both branches to `Provider.Headers`, resolving the session id when the caller has none.
- [x] Verify `internal/provider/runtime` tests.

### 5. Document the change

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
  the same static buffer, so consecutive requests of one run share one affinity id and a later
  run gets a fresh one.
- The fallback lives at the expansion boundary rather than at each caller, so model listing and
  `/usage` requests (which pass `""` on purpose) get the same non-empty header as model requests.
- `Provider.Headers` resolves the id too, because the Codex branch needs it: the
  `codex_auth_headers` (`src/providers/codex_auth.c:275`) keys its session identity and beta flag
  off the *streaming* flag, which gax no longer passes on the request path — the SSE Accept and
  Content-Type moved to the transport, so the Responses wire marks the streamed case instead. The
  non-streamed branch sends `Accept: application/json`, which the Codex `/models`
  request previously went without.
- Retiring `agnes` also drops its catalog-less listing behavior; that path is still covered by the
  `openai-compatible` test, which asserts unknown capabilities and unpriced metadata.
- The reference still ships both defs; gax diverges deliberately because the Zen endpoint no longer
  serves and the Agnes AI endpoint is unusable. `docs/superpowers/plans/2026-10-10-*.md` keep
  their historical task lines and carry a dated note pointing here.

## Divergence from the ported behavior

`src/providers/registry.c` ships `opencode-zen` (pay-as-you-go) and `agnes` (Agnes AI, with an
`agnes_parse_model` hook gax never ported). gax ships neither: the Zen gateway no longer works, and
the Agnes AI hub is not usable. Everything else in the inventory — including `opencode-go` and its
usage windows — stays aligned with the ported behavior.

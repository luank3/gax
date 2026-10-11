# Provider Request Policy Plan

**Goal:** Derive Chat, Responses, and Anthropic body/stream options from provider definitions, scoped config, and catalog metadata.

**Architecture:** Keep config interpretation in `provider/runtime.RequestSettings`; translate that protocol-neutral policy into wire-specific options in `provider/client`. Callers can use defaults or supply explicit wire options.

**Tech:** Go 1.25 standard library; no new dependencies.

**Sources:** `/tmp/luank3-hax-upstream/src/providers/http_provider.c`, `chat_body.c`, and `catalog.c` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`; per-item reasoning replay from `/home/dat/dev/hax` commit `e040365`.

## Task 1: Resolve provider and model request policy

**Files:** `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Test cache modes, session cache keys, reasoning format/field defaults, request-cost flags, model output caps, thinking modes/budgets, and warning behavior.
- [x] Run the focused runtime tests and confirm the `RequestSettings` API is absent.
- [x] Resolve these settings from provider config, definition defaults, and catalog metadata.

## Task 2: Apply defaults through request preparation and streaming

**Files:** `internal/provider/client/client.go` and `client_test.go`.

- [x] Test default Chat and Anthropic bodies end to end, including cache markers, prompt cache keys, usage accounting, reasoning replay, thinking, and output caps.
- [x] Keep explicit caller-supplied wire options available through `PrepareWithOptions` and `StreamOptions.Body`.
- [x] Run package tests and the repository checks.

## Task 3: Preserve Chat Completions reasoning-field provenance

**Files:** `internal/conversation`, `internal/provider/openai`, `internal/provider/runtime`, and `internal/provider/client`.

- [x] Record the streamed reasoning member on conversation items and persist it through session JSON.
- [x] Resolve replay in configured, catalog, provider-default, recorded-field order; explicit `off` remains pinned.
- [x] Add regression tests for parser precedence, model catalog overrides, session decoding, and prepared request bodies.
- [x] Verify the runtime, conversation, OpenAI wire, and client packages.

## Review focus

- Chat cache `auto` follows whether writes replace input; explicit on/off and TTL settings take precedence.
- One-hour cache usage classification is enabled only when the model has a known one-hour write rate and markers are sent.
- Catalog reasoning-field hints apply only when `reasoning_roundtrip` is not pinned.
- With no configured, catalog, or provider-default field, recorded Chat Completions reasoning replays under the member it arrived in; `auto` and `on` request this default resolution.
- User `max_tokens` is capped at the catalog model output limit; otherwise the model limit wins over the 32k fallback.
- Anthropic thinking follows known effort metadata, selected effort, and the configured mode in upstream order.

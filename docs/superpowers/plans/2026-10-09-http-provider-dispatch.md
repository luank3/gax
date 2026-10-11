# HTTP Provider Dispatch Plan

**Goal:** Connect resolved provider settings to the Chat Completions, Responses, and Anthropic Messages body builders and SSE stream parsers.

**Architecture:** Add `internal/provider/client` as a small adapter over `provider/runtime`, the existing wire packages, and shared HTTP transport. It prepares a model-specific request and delegates streaming to the existing retry-aware protocol handlers.

**Tech:** Go 1.25 standard library; no new dependencies.

**Sources:** `/tmp/luank3-hax-upstream/src/providers/http_provider.c`, `wire.c`, and the three provider wire/body/stream modules at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

## Task 1: Prepare model-specific HTTP requests

**Files:** create `internal/provider/client/client.go` and `client_test.go`.

- [x] Test endpoint/path selection, per-model wire routing, auth and custom headers, body serialization, and provider extra-body merge.
- [x] Run the focused package test and confirm the API is absent.
- [x] Implement request preparation without duplicating runtime or wire policy.

## Task 2: Dispatch streams to wire parsers

**Files:** extend `internal/provider/client/client.go` and `client_test.go`.

- [x] Test all three wire formats against `httptest` servers and verify translated text and completion events.
- [x] Route responses through the existing retry and idle-timeout controls.
- [x] Run package tests and the repository checks.

## Review focus

- Per-model wire rules select the request path, body serializer, and event parser together.
- Provider headers and credentials come from the resolved runtime object; streaming headers are added once.
- The provider's filtered/merged `extra_body` reaches the final request body.
- Retry and incomplete-stream behavior remain owned by the shared transport layer.

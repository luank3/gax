# Superseded: DeepSeek Provider Parity Implementation Plan

> Superseded on 2026-10-11. This plan followed the fork snapshot `23988f9`, not the porting baseline `ff458337f8ba7366d71fad4da0210a53b658678a`, which has no DeepSeek provider or `reasoning_required` setting; the implementation was removed from the Go port at the time. DeepSeek and its required-reasoning replay were later restored as a deliberate divergence from the baseline.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Match upstream's shipped DeepSeek provider definition and required Chat Completions reasoning replay in Go.

**Architecture:** Register DeepSeek in the same priority position as upstream, resolve its API key and model catalog identity through the existing provider runtime, and carry the endpoint's required reasoning member through request settings into the Chat Completions body builder. DeepSeek model parsing and account-balance `/usage` were implemented in follow-up plans.

**Tech Stack:** Go provider registry, provider config/runtime, OpenAI-compatible Chat Completions body builder, Go tests.

**Spec:** hax `src/providers/registry.c`, `src/providers/deepseek.c`, `src/providers/http_provider.c`, and `src/providers/chat_body.c`.

## Global Constraints

- Preserve upstream's shipped provider order: hosted providers precede local servers, and generic compatible endpoints remain last.
- Keep `reasoning_required` scoped to Chat Completions provider settings.
- If a reasoning field is required, include an empty field on assistant messages that otherwise carry text, tool calls, or typed reasoning details; never add a message that contains only the empty field.
- Provider credential values must remain redacted from diagnostics.

## Review Focus

- Missing `DEEPSEEK_API_KEY` reports the missing variable and does not make the provider ready; cover in runtime resolution tests.
- A reasoning-less tool call includes `reasoning_content: ""`; cover in Chat Completions body tests.
- Recorded plain reasoning is retained, and typed reasoning details remain alongside the required empty member; cover in body tests.
- An explicit `reasoning_roundtrip: off` warns when the provider requires replay; cover in runtime tests.
- Provider ordering keeps DeepSeek before local servers and removes the non-upstream Openference entry; cover in registry tests.

### Task 1: Match the shipped DeepSeek definition

**Files:**
- Modify: `internal/provider/registry/registry.go`
- Test: `internal/provider/registry/registry_test.go`
- Modify: `internal/provider/runtime/runtime.go`
- Test: `internal/provider/runtime/runtime_test.go`

**Interfaces:**
- Produces: a visible `deepseek` definition using `https://api.deepseek.com`, `DEEPSEEK_API_KEY`, catalog id `deepseek`, default reasoning field `reasoning_content`, and a `ReasoningRequired` flag.
- Keeps: the existing resolver and metadata APIs.

- [x] **Step 1: Write and run the failing provider-order and resolution tests**
- [x] **Step 2: Register DeepSeek at upstream priority and remove Openference**
- [x] **Step 3: Verify the tests pass**

### Task 2: Preserve required reasoning on replay

**Files:**
- Modify: `internal/provider/config/config.go`
- Test: `internal/provider/config/config_test.go`
- Modify: `internal/provider/runtime/runtime.go`
- Test: `internal/provider/runtime/runtime_test.go`
- Modify: `internal/provider/openai/chat_body.go`
- Test: `internal/provider/openai/chat_body_test.go`
- Modify: `internal/provider/client/client.go`

**Interfaces:**
- Consumes: the `ReasoningRequired` definition flag and existing resolved reasoning field.
- Produces: `RequestSettings.ReasoningRequired` and `openai.BodyOptions.ReasoningRequired`.

- [x] **Step 1: Write and run failing tests for the config field and empty reasoning replay**
- [x] **Step 2: Carry the required flag into chat request construction and diagnostics**
- [x] **Step 3: Verify the tests pass**

### Verification

- [x] Run `go test -race -count=1 ./...`
- [x] Run `go vet ./...`
- [x] Build Linux and macOS targets

### Deferred upstream behavior

DeepSeek's custom parser and `/user/balance` usage report are covered in [the follow-up hooks plan](2026-10-10-deepseek-provider-hooks.md). General `/models` fetching is covered in [the model listing plan](2026-10-10-provider-model-listing.md).

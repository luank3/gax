# Provider Model Listing Plan

> Upstream-parity correction (2026-10-11): generic OpenAI-compatible providers fetch `/models` when the picker opens, not as a background metadata probe. Openference parses rich metadata from that explicit listing but has no selected-model probe; Agnes keeps listed IDs with unknown capabilities and pricing.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add authenticated model listing and cache provider model IDs from both explicit listings and background metadata probes.

**Architecture:** A runtime method fetches `<base_url>/models` with the provider's resolved metadata headers, validates the `data` array, and retains usable model IDs. OpenRouter and Openference listings use OpenRouter's parser; Agnes keeps generic IDs with unknown metadata. Background probes parse selected metadata and listing IDs independently; IDs use a generation guard so an older response cannot replace a newer picker result.

**Tech Stack:** Go provider runtime, OpenRouter metadata parser, shared bounded HTTP transport.

**Spec:** Official `github.com/luank3/hax` master commit `ff458337f8ba7366d71fad4da0210a53b658678a`, especially `src/providers/openai_models.c` and `src/providers/registry.c`.

## Constraints

- Use a 10-second timeout and a bounded response body.
- Redact credentials from all errors; use the existing `transport.FormatModelListError` for HTTP failures.
- Reject malformed list envelopes and lists with entries but no usable `id` strings.
- Return an empty success for `data:null` and for an empty data array.
- Preserve model IDs exactly; parser fields refine metadata but do not rewrite IDs.
- OpenRouter probes fetch the full listing; Codex probes omit hidden models from completion IDs.
- Listing-only probes do not delay request turns or overwrite a newer explicit list.

## Tasks

- [x] Add failing tests for Openference metadata listing, `data:null`, and malformed responses.
- [x] Implement `Provider.ListModels` for the OpenAI metadata dialect and OpenRouter/Openference parser dispatch.
- [x] Preserve generic Agnes model IDs without adding unreported capabilities or pricing.
- [x] Add Anthropic cursor-paginated model listing and capability metadata parsing.
- [x] Cache ordered IDs from provider-specific background probes and explicit listings; generic OpenAI-compatible providers list models only when the picker opens.
- [x] Reconcile llama.cpp's configured model with `/models`, canonicalize aliases, and keep server-discovered models run-scoped.
- [x] Verify runtime/client and provider tests.
- [x] Run race tests, vet, and Linux/macOS builds.

## Upstream interaction boundary

Upstream provides rich model metadata through selected-provider probes and explicit `/models` listings. Generic OpenAI-compatible providers list models only when their picker opens; Openference parses metadata from that listing and has no selected-model probe.

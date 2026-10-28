# Keyless Search Canary Plan

**Goal:** Allow maintainers to manually check whether You.com's keyless MCP profile and the DuckDuckGo HTML crawl still work through gax's search client.

**Architecture:** Build-tagged live tests invoke the real `web.Client` with explicit backends, no credentials, a fixed public query, and bounded timeouts. The canary rejects fallback responses and unusable links; offline HTTP fixtures run in the normal suite and exercise success and failure cases. A read-only, manually dispatched GitHub Actions workflow runs both through `make test`.

**Tech Stack:** Go standard library, GitHub Actions.

**Reference:** `docs/superpowers/plans/2026-10-12-web-search-providers.md` and `docs/superpowers/specs/2026-10-12-web-tools-spec.md`.

## Constraints

- No third-party modules, credentials, scheduled workflow, issue creation, or provider/model calls.
- Normal tests must not contact live services, even if environment variables are set.
- Each backend must be checked independently; a successful DuckDuckGo fallback must not mask a You.com failure.
- Live endpoints can throttle or fail due to runner network conditions; report diagnostics without automatic retry or escalation.

## Tasks

- [x] Add offline fixtures for keyless You.com and DuckDuckGo success, fallback masking, upstream failure, and malformed or absent results; demonstrate a failing check before implementation.
- [x] Implement the bounded, build-tagged canary and live subtests; run fixture tests and perform a live check.
- [x] Add a manually dispatched workflow and document local invocation and network limitations.
- [x] Validate the workflow and run `make lint test-race`; record the live outcome.

## Verification

- Offline fixtures failed against the initial no-op check and passed with the implementation, including fallback masking and canceled requests.
- The tagged canary returned usable results from both You.com and DuckDuckGo locally on 2026-10-28; this does not guarantee GitHub runners will avoid upstream throttling.
- `make lint test-race` passed. Ruby Psych parsed the workflow and confirmed a manual trigger with no schedule; GitHub-hosted execution remains unverified until published.

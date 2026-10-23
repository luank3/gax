# Keenable Search Provider Implementation Plan

> **For agentic workers:** Execute inline with executing-plans and test-driven-development. No subagents, commits, pushes, or releases without user authorization.

**Goal:** Add `keenable` to `web_search` without changing the default backend or existing selection priority.
**Architecture:** Follow the existing per-backend functions in `internal/web/search.go`, injectable endpoints in `fetch.go`, credential settings in `internal/config`, and tool configuration in `internal/tools/web.go`.
**Tech Stack:** Go standard library only, cgo-free.
**Spec:** `docs/superpowers/specs/2026-10-12-web-tools-spec.md`; upstream contracts at https://docs.keenable.ai/api-reference/search and https://docs.keenable.ai/authentication.

## Constraints and review focus
- POST `/v1/search` with `X-API-Key` when `KEENABLE_API_KEY` / `web.keenable_api_key` is nonempty; otherwise POST `/v1/search/public` with required `X-Keenable-Title: gax`.
- Send `query`, normalized `max_results` (1–10), and `snippet_max_length: 400`; omit mode to retain the API's default. No extra search/filter settings.
- Prefer each result's nonempty snippet, falling back to description; reuse result sanitation, blank-entry handling, snippet bounds, and count limits.
- Append Keenable after TinyFish in keyed auto selection. A fresh install still uses You.com; explicit `keenable` without a key uses the public endpoint.
- Preserve cancellation, timeout/byte limits, and DuckDuckGo availability fallback. Rejected/malformed keys and exhausted credit remain actionable errors; empty listings are not retried. Public access refusal may fall back without claiming a nonexistent key was rejected.
- Never expose credential values in diagnostics or service error bodies. Mark the credential setting secret; do not add dependencies or change unrelated backend behavior.
- Do not reintroduce internal plan/spec links into README.

## Task 1: Backend and tool integration
Files: `internal/web/{search,fetch}.go`, `internal/web/{search,keenable}_test.go`, `internal/config/{settings.go,config_test.go}`, `internal/tools/{web,web_test}.go`.
- [x] Add failing selection/settings tests and observe failure before implementing the backend.
- [x] Cover keyed/public requests and headers, count/default bounds, snippets/description, blank entries, auto priority, errors/fallback/no-fallback, cancellation, and credential privacy with local HTTP fixtures.
- [x] Add `BackendKeenable`, `Keys.Keenable`, endpoint fields/defaults, `searchKeenable`, and credential diagnostics using the existing shared HTTP helpers.
- [x] Register `web.keenable_api_key` / `KEENABLE_API_KEY`, add the provider choice, and wire tool key loading/description.
- [x] Run `make test TEST_PACKAGES='./internal/web ./internal/tools ./internal/config'`.

## Task 2: Documentation and verification
Files: `README.md`, `CHANGELOG.md`, `AGENTS.md`, web-tools spec, this plan.
- [x] Document keyed/public selection and unchanged defaults; add an Unreleased entry.
- [x] Run `make lint test-race`, cgo-free build, and inline diff review.
- [x] Run one isolated public-endpoint smoke test if network access permits; no live keyed request or credit spending. Record exact outcome and limitations.

## Verification record
- RED: `make test TEST_PACKAGES='./internal/web ./internal/tools' TEST_FLAGS='-run=Keenable -count=1'` failed with unknown provider `keenable`, absent credential setting, and absent provider choice before implementation.
- GREEN: `make test TEST_PACKAGES='./internal/web ./internal/tools ./internal/config' TEST_FLAGS='-count=1'` passes. The registry-size assertion was updated from 75 to 76 for the added setting.
- Ruling: Reuse `addResult` exactly as implemented: it retains duplicates and title-only hits, and skips only entries with both title and URL blank. An initial test fixture incorrectly assumed deduplication; corrected the fixture rather than changing unrelated backend behavior.
- `make lint test-race` passes with inherited provider endpoint/credential/trace/transcript overrides removed. All six cgo-free Make builds pass (linux/darwin/windows × amd64/arm64).
- Native Windows ARM64 vet and Make tests for `internal/web`, `internal/tools`, and `internal/config` pass through the isolated deferred-cleanup exec wrapper.
- Live public smoke: built `/tmp/gax-keenable`, used an isolated mock-provider process with no search keys/config/session persistence, explicitly selected `keenable`, and requested two hits for `Go context cancellation`. The real `web_search` returned two Go documentation results with `[2 results · keenable]`, not fallback results. Evidence is in `/tmp/gax-keenable-smoke-eobo9si9/stdout.jsonl`.
- No live keyed query was sent; keyed authentication, refusal handling, and credential privacy are covered by local HTTP fixtures. No account credits were spent. No separate mode/filter/fetch/MCP features were added.
- Inline self-review found no additional patch issues; no subagents were used. The user subsequently authorized committing and pushing the Keenable provider changes. Existing release tags/assets remain unchanged.

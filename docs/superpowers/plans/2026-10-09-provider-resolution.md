# Provider Resolution Plan

**Goal:** Turn a registry definition plus layered configuration into a safe, testable runtime provider description following the generic HTTP-provider resolution rules.

**Architecture:** Add `internal/provider/runtime` as a pure resolution layer over `config.Store`, `provider/config`, and `provider/registry`. It resolves the endpoint and protocol, metadata dialect, credentials, defaults, headers, extra body, and an availability result without doing network I/O. Stream execution, metadata fetching, dynamic auth hooks, and the CLI remain later layers.

**Tech:** Go 1.25 standard library; no new dependencies.

**Sources:** the ported sources, `docs/providers.md`, and `docs/configuration.md`.

## Constraints

- A pinned provider keeps its shipped endpoint and protocol even when its config block specifies overrides.
- Unpinned providers use a non-empty configured endpoint, otherwise their shipped default; `{port}` uses the registered typed value, then the definition port.
- Supported request APIs are case-insensitive: `openai-completions`/`chat`, `openai-responses`/`responses`, `anthropic-messages`, and `catalog` (Chat default with per-model routing added later).
- Static credentials never appear in `String`, `Error`, or warning output. Keyless endpoints can be configured and ready; probe-backed endpoints report that a probe is needed.
- Shipped extra headers are overridden case-insensitively by user headers; empty strings remove defaults. Body objects recursively merge, with user values winning, after reserved user keys are filtered.
- Model routing rules, OAuth/companion hooks, transport streams, model metadata, and trace redaction remain outside this slice.

## Task 1: Resolve endpoint, wire, credentials, and availability

**Files:** create `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Write tests for pinned endpoint/API, aliases and defaults, `{port}` and trailing slash handling, missing endpoint reasons, API-key environment precedence, API-key-free providers, and probe-required providers.
- [x] Run `go test ./internal/provider/runtime` and verify it fails because the API is absent.
- [x] Implement runtime resolution with typed status values and secret-safe errors.
- [x] Run `go test ./internal/provider/runtime` and `go test ./...`.

## Task 2: Resolve metadata, headers, and body defaults

**Files:** extend `internal/provider/runtime/runtime.go` and `runtime_test.go`.

- [x] Write tests for metadata API overrides/fallback, API-family defaults, shipped header parsing, config header overrides/removals, session placeholders, and recursive JSON body merging.
- [x] Run `go test ./internal/provider/runtime` and confirm the new methods/fields are absent before implementation.
- [x] Implement the resolved request settings and safe warning reporting.
- [x] Run `go test ./internal/provider/runtime`, `go test ./...`, `go build ./...`, and `go vet ./...`.

## Review focus

- Configured values cannot redirect a pinned first-party credential.
- Missing keys and unresolved `$ENV` indirection produce deterministic availability without exposing key contents.
- Empty API-key configuration does not accidentally suppress a provider fallback key.
- Header null/non-string values are ignored rather than interpreted as removal markers.
- Configured extra body values recursively override defaults while reserved protocol fields stay filtered.

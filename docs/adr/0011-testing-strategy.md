# ADR-0011: Testing Strategy

## Status
Accepted (Phase 1)

## Context
`gax` v2 must preserve `hax`'s behavioral fidelity across 6 OS/arch targets, enforce strict package layering, prevent regressions in streaming/compaction/MCP/web UI, and enforce size/memory budgets in CI.

## Options Considered
1. **Ad-hoc unit tests only.**
   - *Rejected*: Misses import-direction regressions, cross-platform compile breaks, and CLI/SSE end-to-end behavior.
2. **Layered test pyramid matching the package architecture.**
   - *Selected*: Each behavior is tested at the lowest layer that observes it.

## Decision

1. **Architecture & Policy Gates (`internal/archtest/`)**:
   - `imports_test.go`: Parses all `.go` ASTs via `go/parser`. Fails if:
     - Any package imports a higher or sibling layer (per ADR-0001).
     - Any package outside `internal/platform` imports `syscall`, `golang.org/x/sys`, or references `runtime.GOOS`.
     - `go.mod` contains unapproved `require` lines or `cgo` is referenced.
2. **Unit & Protocol Tests (`*_test.go`)**:
   - Pure state machines (`agent` turn loop, `compact`, `bashcompress`, `markdown`, `editbuffer`, `session` JSONL reader/writer) tested in-memory with zero network/TTY.
   - Providers tested against `httptest.Server` fixtures and the in-process `mock` provider (porting `hax`'s `scripts/mock/*.txt` scenarios).
   - MCP lazy-loading tested against an in-process stdio/HTTP JSON-RPC fake server verifying zero process spawn when unused and schema caching/compaction behavior.
   - `gax serve` API, SSE streaming, constant-time auth, and Origin/CSRF checks tested via `net/http/httptest`.
3. **Race & Cross-Compile Gate**:
   - `CGO_ENABLED=1 go test -race -count=1 ./...` on Linux.
   - `CGO_ENABLED=0` cross-compile check for all 6 targets (`linux`, `darwin`, `windows` × `amd64`, `arm64`).
4. **Budget Gate (CI)**:
   - Measure stripped binary sizes (all 6 targets) against `<= 12.0 MiB` (`amd64`) / `<= 11.0 MiB` (`arm64`), embedded web assets `<= 60 KiB` gzipped, startup time `<= 15 ms`, and mock workload RSS `<= 32 MiB`.
5. **Platform Honesty**:
   - Tests requiring Windows/macOS OS APIs are tagged and run on their respective CI runners (or explicitly reported as "not tested on Windows/macOS" when run locally on Linux).

## Consequences
- Every slice in Phase 3 is gated on `gofmt`, `go vet`, `go test -race`, 6-target `CGO_ENABLED=0` cross-compile, and architecture import checks.

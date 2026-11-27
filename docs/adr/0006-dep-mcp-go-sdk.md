# ADR-0006: Dependency Candidate — `github.com/modelcontextprotocol/go-sdk`

## Status
Approved (User selected Option B at Phase 1 Gate: keep `go-sdk v1.8.0`, raise binary budget to 14.5 MiB amd64 / 13.5 MiB arm64)

## Context
In Phase 0, the user indicated preference for `github.com/modelcontextprotocol/go-sdk` (Decision 3b). Per Hard Constraint 1, this ADR evaluates `go-sdk` with measured binary size, memory delta, transitive dependencies, and alternatives.

## What It Replaces & Why Stdlib Comparison Matters
- **Replaces**: An in-repo MCP JSON-RPC 2.0 client over stdio (`os/exec` + `bufio.Scanner`) and streamable-HTTP (`net/http` + SSE).
- **In-repo cost**: ~400–500 LOC using stdlib `encoding/json`, `net/http`, and `os/exec` (zero external modules).

## Measured Binary Size & Memory Delta
Measured in `scratch/phase1-bench/go-deps` (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, Go `go1.27.3`) comparing `base` (`net/http` + `encoding/json`) against `mcpsdk` (`github.com/modelcontextprotocol/go-sdk v1.8.0` using `mcp.NewClient`, `CommandTransport`, `StreamableClientTransport`):

| Target | `base` (bytes) | `mcpsdk` (bytes) | Delta (bytes) |
| --- | ---: | ---: | ---: |
| `linux/amd64` | 4,440,224 | 7,831,712 | +3,391,488 (+3.23 MiB) |
| `linux/arm64` | 4,128,928 | 7,209,120 | +3,080,192 (+2.94 MiB) |
| `darwin/amd64` | 4,563,056 | 8,052,016 | +3,488,960 (+3.33 MiB) |
| `darwin/arm64` | 4,235,090 | 7,441,650 | +3,206,560 (+3.06 MiB) |
| `windows/amd64` | 4,591,104 | 8,080,896 | +3,489,792 (+3.33 MiB) |
| `windows/arm64` | 4,170,752 | 7,380,480 | +3,209,728 (+3.06 MiB) |

- **Memory delta (`linux/arm64`)**: `maxrss` 10,648 KiB -> 10,776 KiB (+128 KiB).
- **CGO**: `CGO_ENABLED=0` compatible.

## Metadata
- **Module**: `github.com/modelcontextprotocol/go-sdk` (`v1.8.0`)
- **License**: MIT
- **Maintenance health**: Official MCP Go SDK (Anthropic / Go team collaboration), actively maintained.
- **Transitive dependencies (12 modules)**:
  - Direct/indirect `require` lines: `github.com/google/jsonschema-go`, `github.com/segmentio/asm`, `github.com/segmentio/encoding`, `github.com/yosida95/uritemplate/v3`, `golang.org/x/oauth2`, `golang.org/x/sync`, `golang.org/x/sys`, `golang.org/x/time`.
  - Full module graph (`go list -m all`): also pulls `cloud.google.com/go/compute/metadata`, `github.com/golang-jwt/jwt/v5`, `github.com/google/go-cmp`, `golang.org/x/tools`.

## Alternatives Considered
1. **Hand-written in-repo MCP client** (~450 LOC): Implements `initialize`, `notifications/initialized`, `tools/list`, `tools/call` over stdio (newline-delimited JSON-RPC) and Streamable HTTP (`POST` + `Accept: application/json, text/event-stream`). Zero transitive dependencies, ~30–50 KiB binary impact.

## Decision & Budget Impact
**Flagged for User Call**: Adding `go-sdk v1.8.0` adds **+3.23–3.33 MiB** and **12 transitive dependencies**, which pushes the projected `amd64` binary to **~13.0–13.5 MiB**, **exceeding** the Phase 0 `12.0 MiB` budget.
- **Recommendation**: Switch to the **in-repo MCP client** (0 deps, stays under `12.0 MiB` budget), **OR** explicitly approve raising the binary budget to `14.5 MiB` (`amd64`) / `13.5 MiB` (`arm64`) to keep `go-sdk v1.8.0`.

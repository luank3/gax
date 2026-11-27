# Decisions Needed

## Phase 0 Decisions (Recorded)
1. **Session store compatibility**: **1a** — Keep `hax` v1 JSONL format (additive fields only).
2. **Windows terminal & process control**: **2b** — `golang.org/x/sys` (evaluated in [ADR-0003](../adr/0003-dep-golang-x-sys.md)).
3. **MCP client**: **3b** — `github.com/modelcontextprotocol/go-sdk` (evaluated in [ADR-0006](../adr/0006-dep-mcp-go-sdk.md) — **see Phase 1 budget flag below**).
4. **Embedded web assets**: **4b** — Build `dist/` at `make` / release time; do not commit minified assets to git ([ADR-0010](../adr/0010-embedded-assets-build-strategy.md)).
5. **Search backends**: **5a** — Keep all 8 backends, refactored onto a shared JSON-POST helper.

---

## Phase 1 Approvals Needed (Gate)

### 1. Approve `golang.org/x/sys v0.50.0` ([ADR-0003](../adr/0003-dep-golang-x-sys.md))
- **Delta**: `+0 to +55 KiB` across the 6 targets, `+128 KiB` RSS, **0 transitive dependencies**, BSD-3-Clause.
- **Recommendation**: **Approve** for use exclusively inside `internal/platform`.

### 2. Re-confirm MCP Client vs Binary Budget ([ADR-0006](../adr/0006-dep-mcp-go-sdk.md))
Measured `github.com/modelcontextprotocol/go-sdk v1.8.0` adds **+3.23 MiB** (`linux/amd64`) to **+3.33 MiB** (`darwin/amd64`, `windows/amd64`) and **12 transitive modules** (`cloud.google.com/go/compute/metadata`, `golang-jwt/jwt/v5`, `segmentio/asm`, `segmentio/encoding`, `google/jsonschema-go`, `golang.org/x/oauth2`, `golang.org/x/tools`, etc.).
This pushes the projected `amd64` binary to **~13.0–13.5 MiB**, exceeding the Phase 0 `12.0 MiB` budget.
- **Option A (Recommended)**: Switch to the **in-repo MCP JSON-RPC 2.0 client** (~450 LOC, 0 transitive deps, ~40 KiB, stays under `12.0 MiB` budget).
- **Option B**: Keep `github.com/modelcontextprotocol/go-sdk v1.8.0` and raise the binary budget to `14.5 MiB` (`amd64`) / `13.5 MiB` (`arm64`).

### 3. Approve JS Build Dependencies ([ADR-0009](../adr/0009-css-framework-choice.md))
- Runtime: `preact`
- Build-time only: `vite`, `@preact/preset-vite`, `tailwindcss`, `@tailwindcss/vite`
- Measured prototype: `2.68 KiB` gzipped CSS + `6.68 KiB` gzipped JS (`9.4 KiB` total gzipped, well under the `60 KiB` budget).
- **Recommendation**: **Approve**.

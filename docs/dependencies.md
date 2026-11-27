# Approved Dependencies

All Go builds use `CGO_ENABLED=0`. No third-party Go module may be added to `go.mod` without an approved ADR in `docs/adr/`.

## Go Modules

| Module | Pinned Version | License | Scope | Reason | ADR |
| --- | --- | --- | --- | --- | --- |
| `golang.org/x/sys` | `v0.50.0` | BSD-3-Clause | `internal/platform` only | Typed POSIX `termios`/`ioctl`/`flock` and Win32 Console/Job Object/LockFileEx APIs; 0 transitive deps, +0–55 KiB | [ADR-0003](adr/0003-dep-golang-x-sys.md) |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | MIT | `internal/mcp` only | Official MCP Go SDK for stdio and streamable-HTTP transports, JSON-RPC 2.0, and schema handling (budget raised to 14.5 MiB amd64 / 13.5 MiB arm64) | [ADR-0006](adr/0006-dep-mcp-go-sdk.md) |

## JS Packages (`web/`, build-time only)

| Package | Role | Scope | Reason | ADR |
| --- | --- | --- | --- | --- |
| `preact` | Runtime UI library (~4 KiB gz) | `web/` | Required by spec for `gax serve` SPA | [ADR-0008](adr/0008-web-ui-structure.md) |
| `vite` | Build-time bundler | `web/` dev | Produces static assets for `//go:embed` | [ADR-0010](adr/0010-embedded-assets-build-strategy.md) |
| `@preact/preset-vite` | Vite Preact JSX transform | `web/` dev | Build-time JSX compilation | [ADR-0008](adr/0008-web-ui-structure.md) |
| `tailwindcss` | Utility CSS framework | `web/` dev | CSS-first `@theme` tokens + automatic purge (2.68 KiB gz in prototype) | [ADR-0009](adr/0009-css-framework-choice.md) |
| `@tailwindcss/vite` | Tailwind v4 Vite plugin | `web/` dev | Build-time CSS generation | [ADR-0009](adr/0009-css-framework-choice.md) |

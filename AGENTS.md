# AGENTS.md

## Architecture & Constraints
- **Strict layering**: `core` (0) → `platform` (1) → `config`/`session`/`catalog`/`skill`/`subagent`/`transcript`/`transport`/`wiretrace`/`bashcompress`/`web` (2) → `provider`/`tools`/`mcp` (3) → `agent` (4) → `tui`/`oneshot`/`jsonstream`/`serve` (5) → `cmd/gax` (6). Enforced by [internal/archtest/imports_test.go](internal/archtest/imports_test.go).
- **Platform isolation**: Only `internal/platform` may import `syscall`, `golang.org/x/sys`, or reference `runtime.GOOS`.
- **Dependencies**: Zero unapproved Go or JS dependencies. Approved dependencies are pinned in [docs/dependencies.md](docs/dependencies.md) with ADRs in [docs/adr/](docs/adr/).
- **CGO_ENABLED=0**: All shipped binaries are static and cgo-free.

## Commands
```sh
make web          # build Preact + Tailwind v4 SPA into internal/serve/ui/dist
make build        # build ./bin/gax (-tags webui, CGO_ENABLED=0)
make lint         # gofmt check + go vet
make test-race    # CGO_ENABLED=1 go test -race -count=1 ./...
make budget       # 6-target cross-compile + size/startup/RSS budget check
```

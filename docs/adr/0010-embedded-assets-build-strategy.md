# ADR-0010: Embedded Web Assets Build Strategy

## Status
Accepted (Phase 1 — User selected Decision 4b)

## Context
`gax serve` serves the Preact web UI from static files embedded via Go's `//go:embed`. In Phase 0, the user selected **Option 4b** (do not commit built `dist/` artifacts to git; build frontend assets at `make` / release time).

## Options Considered
1. **Commit built `internal/serve/ui/dist/` to git (Option 4a).**
   - *Pros*: `go install` works without Node.
   - *Cons*: Pollutes git diffs with minified JS/CSS blobs.
2. **Build `dist/` at `make` / CI release time; keep `dist/` gitignored (Option 4b, selected).**
   - *Pros*: Clean git history; no generated minified blobs in PRs.
   - *Cons*: Plain `go build` / `go install` without running `make web` first would fail `//go:embed ui/dist/*` if `ui/dist/` is empty.

## Decision
Keep `internal/serve/ui/dist/` **gitignored** and build it via `make web` (invoked automatically by `make build` and GoReleaser `before.hooks`):
1. **Build pipeline**: `make web` runs `npm ci --prefix web && npm run build --prefix web`, outputting to `internal/serve/ui/dist/`.
2. **Plain `go test ./...` / `go build` without `make web`**: Keep a tiny tracked `internal/serve/ui/dist/.keep` (or `embed` behind a `//go:build !no_webui` / placeholder `index.html` fallback only if `dist/index.html` is absent? No — `//go:embed` fails if a pattern matches nothing, so a tracked `internal/serve/ui/placeholder/index.html` or `embed_webui.go` build tag would be needed if `go test ./...` runs before `make web`).
   - Specifically: use `//go:build webui` for `embed` of `ui/dist`, enabled by default in `make build` / `make test` / GoReleaser (`-tags webui`), and a fallback stub without `-tags webui` that prints `"web UI assets not built; run make web"` on `gax serve`!
   - This guarantees `go test ./...` and `go vet ./...` pass out-of-the-box on a fresh clone even without Node, while `make build` and all release binaries embed the full Vite-built UI.
3. **CI enforcement**: CI runs `make web` + `make build -tags webui` + verifies total gzipped `internal/serve/ui/dist/` size `<= 60 KiB`.

## Consequences
- Git history stays 100% free of minified JS/CSS artifacts.
- `go test ./...` works without Node; `gax serve` full UI requires `make build` (or Node at build time).
- Release archives (Linux, macOS, Windows) always include the embedded web UI.

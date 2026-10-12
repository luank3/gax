# AGENTS.md

## Project overview

gax is a Go reimplementation of [hax](https://github.com/OleksandrChekhovskyi/hax), a minimalist
terminal-native coding agent originally written in C. The port targets behavioral parity with the
hax sources it was ported from; `internal/version.Value` pins the baseline commit (`ff45833`)
cited by every plan in `docs/superpowers/plans`.

Standard library only: `go.mod` has no `require` block and must keep none. Features are ported
from the C source rather than invented; when behavior is ambiguous, read the upstream hax
checkout before deciding.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/gax` | Process entry point; hands `os.Args[1:]` and stdio to `app.Run`. |
| `internal/app` | REPL, one-shot and JSON modes, slash-command dispatch, pickers, `/usage` rendering. |
| `internal/agent` | Turn loop, compaction, usage accounting, system-prompt assembly, session state. |
| `internal/provider/registry` | Shipped provider definitions, hook flags, config-only providers. |
| `internal/provider/runtime` | Resolves definitions plus config into endpoints, credentials, wire rules, request settings, model listing. |
| `internal/provider/{openai,anthropic}` | Request bodies and stream parsers for the three wires. |
| `internal/provider/{codex,deepseek,openrouter,opencodego,mock}` | Provider hooks: auth, model metadata, account usage. |
| `internal/provider/{client,config}` | Wire dispatch and the `providers.<name>` configuration vocabulary. |
| `internal/tools` | Built-in tools (`read`, `edit`, `write`, `bash`, `task_wait`, `web_fetch`, `web_search`, `skill`) and background tasks. |
| `internal/web` | HTTP fetching, HTML-to-text conversion, and the search backends behind the web tools (you.com first, then brave, octen, exa, firecrawl, tinyfish, with the DuckDuckGo crawl as the fallback; a gax addition; upstream has none). |
| `internal/bashcompress` | Compresses recognized shell commands' output for the model (`ls`, `cat`, `grep`, `git`, test runners, linters, `docker ps`) — a gax addition; upstream has none. |
| `internal/config` | Setting registry, layered resolution, persistence, presets. |
| `internal/session` | Session logs, resume, fork, prune, prompt history. |
| `internal/terminal/*` | Input editor, live renderer, Markdown engine, pickers, width, theme, clipboard. |
| `internal/transport`, `internal/wiretrace` | HTTP/SSE transport, retries, traces, secret redaction. |
| `internal/catalog` | models.dev catalog cache used for model metadata and wire routing. |
| `docs/superpowers` | Porting plans (`plans/`) and design notes (`specs/`), one file per subsystem. |

## Commands

```sh
make build       # ./bin/gax
make test        # go test ./...
make test-race   # go test -race -count=1 ./...
make vet         # go vet ./...
make fmt-check   # gofmt -l must be empty
make fmt         # gofmt -w
make lint        # fmt-check + vet
make cover       # coverage summary
make snapshot    # local goreleaser snapshot (requires goreleaser)
```

Focused runs are fine while working (`go test ./internal/provider/deepseek/...`), but finish
with `make lint test-race`.

**Always run tests through the Make targets.** They unset `GAX_PROVIDER`, `GAX_MODEL`,
`GAX_EFFORT`, and `GAX_PRESET` first. Several `internal/app` tests assert exact stdout/stderr
for one-shot runs and fail under an ambient provider/model/effort selection, which makes a
plain `go test ./...` in a configured shell look broken when it is not.

Cross-compilation is verified for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and
`darwin/arm64` with `CGO_ENABLED=0`; keep it that way (cgo is an optional locale optimization
behind build tags).

## Conventions

- **No third-party modules.** Only the standard library. Adding a dependency needs an explicit
  decision from the maintainer, not a convenience argument.
- **Match the port, not personal taste.** Keep function names, error strings, and user-visible
  text aligned with the Go-side precedents; when porting new behavior, mirror upstream.
- **Comments explain why.** The existing code documents non-obvious constraints (wire quirks,
  ordering requirements, endpoint bugs) and omits narration. Follow that.
- **Errors are user-facing.** Messages read as terminal diagnostics (`failed to fetch usage from
  <url>`, `provider 'deepseek': reasoning_required needs the reasoning field's name ...`) and
  must never include credential values. Register secrets with `wiretrace` when they cross the
  transport.
- **Environment variables are `GAX_`-prefixed.** Provider credentials are the exception
  (`OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, …). Settings are declared in
  `internal/config/settings.go`; add one there instead of reading `os.Getenv` directly.
- **XDG paths belong to gax** (`~/.config/gax/config.json`,
  `~/.local/state/gax/sessions/...`). Tests pin these locations, so change them only
  deliberately.
- **Tests are table-driven and live beside the code** (`foo.go` / `foo_test.go`), mostly in
  external `_test` packages for the runtime-facing ones. New behavior comes with tests; the
  porting plans require it.
- **User-visible text changes ripple.** The help screen, banner, slash-command registry, JSON
  result records, and provider diagnostics are asserted verbatim by tests — update them
  together.

## Workflow

1. Read the matching plan in `docs/superpowers/plans` before touching a subsystem; each records
   goal, constraints, tasks, and verification.
2. Change code and tests together; prefer the smallest correct change that fits the surrounding
   style.
3. Run `make lint test-race`.
4. Update the plan (tick tasks, or add a dated plan for new work). Plans are the porting record —
   a behavior change with no plan entry is incomplete.
5. Commit with an imperative subject; never commit generated binaries (`bin/`, `dist/` are
   ignored).

## Gotchas

- `internal/version.Value` is injected at release time via
  `-ldflags -X github.com/luank3/gax/internal/version.Value=<version>`; its default pins the
  upstream commit, and session headers record it as `gax_version`.
- The system prompt tells the model that `GAX_SUBAGENT_DEPTH` limits nested `gax -p` runs.
  Subagent recursion, provider inheritance, and the child environment are built in
  `internal/tools/bash.go` and `internal/subagent`.
- Provider hooks are dispatched by definition ID in `internal/provider/runtime`; adding a
  provider means touching the registry, the hook switch, and usually a new provider package.
- `golangci-lint` reports pre-existing findings in this repository; CI gates on gofmt, `go vet`,
  and the test suite, so do not treat a red `golangci-lint run` as your regression.

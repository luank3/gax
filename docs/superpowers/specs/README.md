# Smallest-spec index

One reviewable specification per leaf subsystem, written from the sources and tests at
`internal/version.Value` (`ff45833`). Each spec carries **Rule / Source / Assertion / Rejected**
entries; read the spec against its cited lines rather than trusting a summary of them.

## Reading order for a review

1. `2026-10-12-*-provider-*` + `config-layers` + `catalog-core`: what gax sends, where it sends it,
   and which setting wins.
2. `agent-core`, `agent-prompt`, `agent-compact-usage`, `tools-*`: what the model sees and may call.
3. `app-*`, `terminal-*`, `small-foundation`: what the user sees.
4. `transport-http`, `session-core`, `web-tools`, `bashcompress`, `system-runtime`: the I/O edges.

## Specs

| Spec | Lines | Rules | Packages |
| --- | --- | --- | --- |
| `2026-10-12-app-core-spec.md` | 139 | 10 | `app` (Run, REPL, output, signals) |
| `2026-10-12-app-commands-config-spec.md` | 140 | 8 | `app` (`/provider`, `/model`, `/effort`, `/config`, `/preset`) |
| `2026-10-12-app-commands-session-spec.md` | 140 | 9 | `app` (`/undo`, `/fork`, `/resume`, `/session`, `/copy`, `/tasks`) |
| `2026-10-12-app-commands-slash-spec.md` | 177 | 14 | `slash`, `app` (completion, pickers) |
| `2026-10-12-agent-core-spec.md` | 138 | 8 | `agent` (session, loop), `conversation` |
| `2026-10-12-agent-prompt-spec.md` | 139 | 9 | `agent/prompt`, `skill` |
| `2026-10-12-agent-compact-usage-spec.md` | 139 | 8 | `agent` (compact, stats, usage) |
| `2026-10-12-provider-runtime-spec.md` | 138 | 9 | `provider/runtime`, `provider/registry`, `provider/config` |
| `2026-10-12-provider-wire-spec.md` | 134 | 10 | `provider/openai`, `provider/anthropic` |
| `2026-10-12-provider-hooks-spec.md` | 139 | 8 | `provider/{deepseek,openrouter,opencodego,mock,modelsort,usagerender}` |
| `2026-10-12-provider-codex-spec.md` | 140 | 9 | `provider/codex` |
| `2026-10-12-config-layers-spec.md` | 129 | 7 | `config` |
| `2026-10-12-catalog-core-spec.md` | 138 | 8 | `catalog` |
| `2026-10-12-transport-http-spec.md` | 139 | 8 | `transport`, `provider/client` |
| `2026-10-12-tools-registry-spec.md` | 140 | 10 | `tools` (registry, read/edit/write, task_wait) |
| `2026-10-12-tools-bash-spec.md` | 140 | 10 | `tools` (bash, classifier, preprocess) |
| `2026-10-12-bashcompress-spec.md` | 140 | 9 | `bashcompress` |
| `2026-10-12-web-tools-spec.md` | 226 | 13 | `web`, `tools/web.go` |
| `2026-10-12-terminal-input-spec.md` | 138 | 7 | `terminal/input` |
| `2026-10-12-terminal-live-spec.md` | 140 | 9 | `terminal/live` |
| `2026-10-12-terminal-markdown-spec.md` | 139 | 9 | `terminal/markdown` |
| `2026-10-12-terminal-width-spec.md` | 139 | 8 | `terminal/width`, `terminal/picker`, `terminal/vtresolve`, `terminal/theme` |
| `2026-10-12-session-core-spec.md` | 139 | 9 | `session` |
| `2026-10-12-system-runtime-spec.md` | 138 | 9 | `system/*` |
| `2026-10-12-small-foundation-spec.md` | 171 | 12 | `cli`, `subagent`, `version`, `diag`, `wiretrace`, `transcript` |

## Triage

Findings that need a decision, not just a test. Everything else lives in each spec's `Gaps` and
`Review` sections; the mechanical check below covers citation integrity, not correctness of judgment.

### Recorded decisions and corrections

- `/provider`, `/model`, and `/effort` accept and apply an argument in gax
  (`TestRunREPLSelectsSelectionByArgument`, `internal/app/app_test.go:1162`). **Resolved
  2026-10-12:** this is upstream parity, not a divergence. Upstream master `2834c2c` declares
  `usage = "[id]"` (and `"[level]"` for `/effort`) with an `argument_choices` hook for all three in
  `src/slash.c`; the fork commit `ff45833` predates upstream's argument-taking commits. The
  2026-10-11 "accepts no arguments" notes were wrong; `2026-10-10-repl-provider-selection.md` and
  `2026-10-10-slash-command-front-end.md` now carry a correction, as does
  `2026-10-10-command-entrypoint.md:22`.
- `docs/sessions.md:76` said `/undo` truncates the log; `session.Log.Undo`
  (`internal/session/log.go:321`) appends an `undo` record and rewinds the write cursor. The doc now
  says that.
- `docs/superpowers/plans/2026-10-11-web-tools.md:36` requires an error when a search returns
  nothing usable. **Resolved 2026-10-12:** the plan governs; every backend now rejects an empty or
  entry-less listing in `finishSearch` (`internal/web/search.go:560`), and
  `TestSearchReportsBackendFailures` pins the shapes. The same day replaced the DuckDuckGo HTML
  backend this entry was written against with six search APIs, you.com leading them, and kept the
  crawl as the fallback for a backend that cannot answer
  (`docs/superpowers/plans/2026-10-12-web-search-providers.md`).
- `docs/superpowers/plans/2026-10-09-config-provider-registry.md:46` counts "all 64 ordered upstream
  setting entries" while the registry has a different row count; `config-layers` records the delta.
- `catalog.merge` had no callers. **Resolved 2026-10-12:** deleted instead of wired
  (`internal/catalog/catalog.go`); the fallback order it encoded never ran.
- The terminal line editor (keys, paste, resize, interrupt, history file) has **no** plan entry. That
  is the largest behavior area in the tree whose only record is the code plus these specs.

### Behavior worth a second look

- `internal/terminal/width/display.go:43`: the 20-column floor of `DisplayWidth` and the physical
  clamp of `ContentColumns` disagree by design, so `DisplayWidth(19, "auto", 0)` is 20 while
  `ContentColumns(19, "auto", 0)` is 19. Confirm no renderer mixes the two.
- `internal/terminal/markdown/stream.go:107`: a table attempt is emitted only after
  `FlushTable`/`Close`, so a table up to 32 columns and 2048 rows is buffered with no early output.
  A long table therefore shows nothing until it ends. Documented, not verified as intended.
- `internal/catalog/catalog.go:398`: `merge` has no callers; the fallback order it encodes is
  therefore unasserted and untested. Either wire it or delete it.
- `internal/catalog/catalog.go:456`: the tier comparison `inputTokens <= tier.ContextThreshold` is
  exclusive at the boundary. Confirm which side of the threshold owns the tier.
- `internal/config/config.go:613` and `:620`: `parseScaled` overflow branches are unreachable in
  tests; a huge configured number may silently fall back instead of warning.
- `internal/provider/usagerender/text.go:72`: the fit decision measures `usageBarColumn` and the bar
  only, while `displayWidth` is a physical limit; on a narrow terminal the row can still exceed it.
- `internal/provider/deepseek/usage.go:55`: availability accepts any spelling except bare `false`,
  so `"false"` or `0` read as available.
- Anthropic input tokens sum base, cache-read, and cache-write tokens
  (`internal/provider/anthropic/events.go:224`). `catalog.Price` subtracts those back out only when
  `cacheWrite >= input` rate (`internal/catalog/catalog.go:503`); verify the rate pairs in the
  catalog make that true for every Anthropic entry.
- `internal/terminal/live/render.go:361`: a visible reasoning delta sets `attemptRendered` while a
  hidden one returns early, so a retry after hidden reasoning takes the unmarked path. Confirm that
  is the intended display difference.
- `internal/terminal/input/editor.go:644`: a partial bracketed-paste marker is dropped silently, the
  one place paste bytes vanish without a diagnostic.

### Untested, high-traffic paths

- `internal/agent/loop.go:307`: the `error: no tool executor is configured` fallback, reached only
  when both the tool call and the registry are absent.
- `internal/transport`: `retry.go`, `sse.go`, `api_error.go`, and `ca_roots.go` have no test file of
  their own; jittered backoff, `Finalize`, `Retry-After` parsing, and `FormatAPIError` are unasserted.
- `internal/tools/registry.go:534`: the nil-registry branch, and `files.go:104`'s line-count
  truncation marker, both have no asserting test.
- `internal/app/repl.go:58` and `:62`: the non-TTY prompt gate and the `130` exit for a canceled
  context are unasserted.

## Verification

The mechanical check that every citation resolves to a real line and every cited test name exists at
the cited line (script: `docs/superpowers/specs/tools/verify_citations.py`):

```sh
python3 docs/superpowers/specs/tools/verify_citations.py docs/superpowers/specs/2026-10-12-*-spec.md
```

Every cited test also has to exist; `check_test_names.py` reports names a spec mentions that no
`*_test.go` defines. Its four current hits are the gaps themselves, not typos: three plan-named
tests in `app-commands-slash` and `TestStreamSegmentBoundaryAfterHeadingKnownGap` in
`terminal-markdown`.

```sh
python3 docs/superpowers/specs/tools/check_test_names.py docs/superpowers/specs/2026-10-12-*-spec.md
```

Behavioral verification for the whole set is the port's own gate:

```sh
make lint test-race
```

Each spec's `Assertion` lines name the test that pins its rules, so a focused review can run a single
package: `go test ./internal/<package>/` through the Make environment (see `CONTRIBUTING.md` for why
the Make targets matter).

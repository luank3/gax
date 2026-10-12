# Bash Output Compression Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compress the model-facing output of common shell commands so an agent reads a compact
form of `ls`, `cat`, `grep`, `git`, test-runner, linter, and container output instead of raw
terminal text — the `rtk` idea (Rust Token Killer, `rtk-ai/rtk`) applied inside the bash tool.

**Architecture:** A new `internal/bashcompress` package parses the command line, matches it against
a filter per command family, and returns a compact form of the captured output plus whether it
dropped lines the raw output still holds. `internal/tools/bash.go` applies the filter to the
captured bytes before the existing output shaping, so caps, saved-path recovery, and display
behavior are unchanged; the full output is preserved to a tracked temp file whenever a filter drops
anything, and the result carries `[full output: <path>]`.

**Tech Stack:** Go standard library only (`strings`, `strconv`, `sort`, `encoding/json`, `regexp`
where a shape genuinely needs it).

**Spec:** upstream hax has no output compressor, so this is a deliberate gax addition. The filter
set, caps, and output shapes follow `rtk-ai/rtk` (Apache-2.0) as read at commit `master` on
2026-10-12: `src/cmds/system/ls.rs` (`compact_ls`), `src/cmds/system/tree.rs`, `src/cmds/system/
read.rs` + `src/core/filter.rs` (`MinimalFilter`), `src/cmds/system/search.rs`, `src/cmds/system/
ast_grep_cmd.rs`, `src/cmds/git/git_cmd.rs` (`compact_diff`, `format_status_output`,
`parse_commit_output`), `src/cmds/rust/cargo_cmd.rs` (`filter_cargo_test`), `src/cmds/go/go_cmd.rs`
(`filter_go_test_json`), `src/cmds/python/pytest_cmd.rs`, `src/cmds/python/ruff_cmd.rs`,
`src/cmds/python/sqlfluff_cmd.rs`, `src/cmds/cloud/container.rs`, `src/cmds/js/npm_cmd.rs`, and the
shared caps in `src/core/truncate.rs`.

## Constraints

- No third-party modules. No new `require` block entry.
- **Never worse than raw:** a filter result replaces the raw output only when it is strictly
  smaller. rtk's `never_worse` guard, expressed in bytes rather than estimated tokens.
- **Nothing disappears silently:** any filter that drops lines (caps, collapsed test output,
  truncated hunks, trimmed comment lines) sets `Omitted`, and the bash tool then preserves the full
  output and appends `[full output: <path>]`. Reformatting that keeps every line (git status, docker
  ps, ls) does not set it.
- **The command must be one plain command.** A command containing a pipe, redirection, `;`, `&&`,
  `||`, background `&`, command substitution, or a newline is never compressed: the captured bytes
  are then the output of something else. Quoted strings may contain those bytes.
- **Failures survive.** Filters for test runners and linters keep failures first; filters for
  content (`cat`, `head`, `tail`) only apply when the command exited 0, so an error message is never
  filtered. Non-zero exits for structured commands (`git`, tests, linters, `docker`) are filtered,
  because those filters preserve the failure text by construction.
- **Only the model-facing text changes.** The live terminal display keeps streaming raw bytes;
  `OutputSummarizesDisplay`, `HiddenTail`, `FallbackOutput`, and JSON records are untouched.
- Disabled by a setting (`bash.compress`, default on) and by `GAX_BASH_COMPRESS=off`; when disabled
  the captured bytes reach the shaper exactly as before.
- Windows/POSIX: the parser handles `\`-escapes and single/double quotes only; no shell expansion.

## Tasks

### 1. Command parsing

- [x] Write failing tests for `Parse`: plain commands, quoted arguments with spaces, env
  assignments, `sudo`/`command` prefixes, and every rejection (pipe, redirect, `;`, `&&`, `||`,
  `&`, backtick, `$(`, newline, unbalanced quote).
- [x] Implement `Parse(command string) (Invocation, bool)` with a small POSIX-ish tokenizer.
- [x] Verify `internal/bashcompress` tests.

### 2. Entry point, caps, and helpers

- [x] Write failing tests for `Compress`: no filter, unknown program, filter output not smaller than
  raw, disabled by the caller, and the `Omitted` flag.
- [x] Implement `Compress(command, output string, exitCode int) (Result, bool)` and the shared caps
  (`CAP_ERRORS` 20, `CAP_WARNINGS` 10, `CAP_LIST` 20, `CAP_INVENTORY` 50) plus `truncateLine`,
  `compactPath`, blank-run collapsing, and comment stripping.
- [x] Verify `internal/bashcompress` tests.

### 3. File and search filters

- [x] `ls`: compact long-format listing (dirs first, octal with `-l`, sizes), noise-directory
  filtering unless `-a`, inventory cap with `... (N more, M filtered)`.
- [x] `tree`: noise-directory subtrees dropped, body capped, counts line kept as a header.
- [x] `cat`/`head`/`tail`/`bat`: comment and blank-run stripping for known source extensions above
  a size threshold, capped, exit-0 only.
- [x] `grep`/`rg`: long lines truncated, matches grouped by file past the caps (`200` total, `25`
  per file) with `+N more in <file>` notes.
- [x] `ast-grep`/`sg`: grouped by file, `5` per file and `50` total, rtk's overflow notes.
- [x] Verify with table-driven fixtures for each shape.

### 4. Git filters

- [x] `git status`: `* <branch>` header, porcelain-style `XY path` lines grouped by state, clean
  message, hint lines dropped, in-progress rebase/merge/cherry-pick state surfaced.
- [x] `git diff`/`git show --patch`: port rtk's `compact_diff` (file headers, `  +N -M` per file,
  hunk headers kept, ≤3 leading context lines, 100 lines per hunk, 500 lines per diff).
- [x] `git log`: `hash subject (relative date) <author>` plus up to 3 body lines, 10 commits by
  default, honoring `-n`/`--max-count`.
- [x] `git add`/`commit`/`push`/`pull`/`fetch`/`stash`: one confirmation line (`ok`, `ok abc1234`,
  `ok main`, `ok N files +A -D`), failure text kept verbatim.
- [x] Verify with fixtures captured from real repositories.

### 5. Test-runner and linter filters

- [x] `cargo test`: rtk's shape (`cargo test: N passed (...)`, else `FAILURES (n):` + summaries).
- [x] `go test`: NDJSON parsed when the output is JSON, else the text form; `Go test: N passed[, M
  failed] in K packages` with per-package failure blocks.
- [x] `pytest`: `Pytest: N passed, M failed ...` plus `Failures:` with `[FAIL] <test>` and up to 3
  relevant lines each.
- [x] `npm`/`pnpm`/`yarn`/`bun test`, `jest`, `vitest`: npm boilerplate stripped, `Tests`/`Test
  Files` summary kept, failure blocks kept.
- [x] `ruff check` and `sqlfluff lint`: `Top rules` / `Top files` / `Violations` grouping parsed
  from the default text output.
- [x] Verify with fixtures for each runner.

### 6. Container filter

- [x] `docker ps`: `[docker] N containers:` plus `  <id> <name> (<image>) <status>[ ports]`, list
  cap.
- [x] Verify with a fixture of a default `docker ps` table.

### 7. Wire into the bash tool

- [x] Write failing `internal/tools` tests: compression applies to a synchronous `ls -la`/`git
  status`, is skipped when disabled, is skipped for piped commands, appends `[full output: <path>]`
  when a filter dropped lines, and never changes the display sink bytes.
- [x] Add `commandCapture.preserveFull` so a filter that dropped content always has a recovery path,
  even when the capture never spilled.
- [x] Apply the filter in `formatCommandResult` and `collectTaskOutput`.
- [x] Verify `internal/tools` tests.

### 8. Configuration and documentation

- [x] Register `bash.compress` (`GAX_BASH_COMPRESS`, default on, live-editable) and read it in the
  bash tool.
- [x] Update `README.md` (a section on the compressor and its setting) and `CHANGELOG.md`.
- [x] Run `make lint test-race` and a mock-provider smoke run over `--json` to confirm the
  compressed text is what the model receives.

## Verification

- [x] `make lint`.
- [x] `make test-race`.
- [x] One-shot mock run: `git status`, `ls -la`, `grep -rn`, `go test ./...` against the real
  repository, confirming the compact text and the `[full output: ...]` note when content is dropped.
- [x] `GAX_BASH_COMPRESS=off` run of the same commands, confirming byte-identical raw output.

## Record

- **Filters live in `internal/bashcompress`, not in a command wrapper.** rtk rewrites the command
  line (`git status` → `rtk git status`) and re-runs it with flags it needs (`git status --porcelain
  -b`, `git log --pretty=format:%h %s (%ar) <%an>`, `docker ps --format ...`, `ruff check
  --output-format json`, `go test -json`). gax sees only the command the model ran and the bytes it
  produced, so every filter parses the *natural* output of that command. The compact shapes follow
  rtk; the parsers are gax's.
- **`never_worse` compares bytes.** rtk compares `bytes / 4` token estimates and keeps the filtered
  form on a tie. gax keeps the compact form only when it is strictly smaller, which is stricter and
  needs no tokenizer.
- **Recovery is the bash tool's existing saved-output file.** rtk stores dropped output in a tee
  directory or SQLite and prints `rtk recall <slug>`. gax stores it with `commandCapture.preserveFull`
  and prints `[full output saved to <path>]`; the file is tracked by the registry and removed when
  the conversation's tasks reset, like every other bash spool. When no file can be written the
  filter's result is discarded and the raw output stays.
- **`cat`/`head`/`tail` are filtered above a threshold, not behind a flag.** rtk's smart reading is
  `rtk read -l minimal`, which the hook does not add: a plain `cat` passes through. gax cannot add
  flags either, so it applies the same minimal filter (comment-only lines, comment blocks, blank
  runs, Python docstrings and `/** */` blocks kept) only to dumps over 200 lines or 8 KiB, caps the
  result at 200 lines, and names the recovery file. A non-zero exit is never filtered, so an error
  message is never trimmed.
- **`tree` keeps its counts line.** rtk's tree filter removes the trailing `N directories, M files`
  summary; gax moves it to the front, because it is the one line that survives a body cap. Noise
  subtrees (rtk's `NOISE_DIRS`, which rtk passes to `tree -I`) are dropped by the filter instead,
  with a count.
- **`ls` drops `.` and `..`.** rtk lists them when the user passed `-a`; gax treats the parent links
  as never the answer. Noise directories are still shown when `-a`/`-A` was passed, as rtk does.
- **`git add` stays silent.** rtk counts staged files by re-running `git diff --cached --shortstat`
  and prints `ok N files changed, ...`. gax keeps the empty output (nothing is smaller than nothing)
  and only replaces the progress output of `commit`, `push`, `pull`, `fetch`, and `stash`.
- **Test-runner and linter detail caps are gax's.** rtk's caps (`CAP_ERRORS` 20, `CAP_WARNINGS` 10,
  `CAP_LIST` 20, `CAP_INVENTORY` 50) are kept, but ruff and sqlfluff show at most 10 individual
  violations: rtk compares against its own injected JSON baseline, while gax's baseline is the
  concise text, so a 50-line detail list would lose the `never_worse` guard. grep/rg keep rtk's 200
  total and 25 per file; ast-grep keeps 5 per file and 50 total.
- **Not ported:** command rewriting and the hook/agent integrations, `rtk run/err/test/summary`
  wrappers, the tee/SQLite retriever and `rtk recall`, `rtk gain` analytics and telemetry, the
  ~100 other filters (jest/vitest/bun-specific reports beyond the shared summary, `gh`, `aws`,
  `kubectl`, maven/gradle, `pnpm`/`uv`/`pip` package managers), `-u/--ultra-compact`, and
  per-project TOML filters.
- The filter set is closed: `internal/bashcompress` has no configuration beyond on/off, and a
  command whose output shape a filter does not recognize always keeps its raw bytes.

## Upstream divergence

hax has no output compressor: the bash tool returns captured bytes shaped only by the output cap and
line-width rules. Everything here is a gax addition, and it is off-switchable (`bash.compress`).

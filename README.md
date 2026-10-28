<div align="center">

# gax

**A minimalist coding agent in your terminal. One native binary, no third-party Go dependencies.**

[![CI](https://github.com/luank3/gax/actions/workflows/ci.yml/badge.svg)](https://github.com/luank3/gax/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

gax is a terminal-native coding agent written in Go. It began as a port of the C agent
[hax](https://github.com/OleksandrChekhovskyi/hax) and has grown past it since: the web tools, the
bash output compression, and the search backends are gax's own.

## Key features

- **One cgo-free binary** — no third-party Go modules. Windows command execution uses Git Bash.
  Starts instantly and keeps a small footprint, leaving RAM for local models.
- **Local models are first-class** — point gax at a running `llama-server` or `ollama serve` and
  it discovers the served models; llama.cpp also reports its loaded context, output, image, and
  effort capabilities, so no custom provider block is needed for the default setup.
- **Respects your terminal** — streaming Markdown and live tool output, reflowed for the
  current width. Only the streaming region is redrawn, so native scrollback and copy/paste
  survive.
- **Inspectable** — review exactly what was sent and received with the Ctrl-T transcript view,
  or capture a wire-level HTTP/SSE trace with `GAX_TRACE`.
- **Broad provider support** — OpenAI (+ compatible), Anthropic (+ compatible), Codex (ChatGPT
  subscription), OpenRouter, OpenCode Go, DeepSeek, llama.cpp, and Ollama.
- **Well-behaved Unix tool** — XDG config/state paths, clean stdout in `-p` one-shot mode with
  resume hints on stderr, plain-text JSON config and session files, scriptable `--json` output.
- **Compact tool output** — recognized shell commands (`ls`, `cat`, `grep`, `git`, test runners,
  linters, `docker ps`) reach the model as a compressed form, with the full output kept in a temp
  file the result names. Disable with `bash.compress`.

## Install

With a Go toolchain (1.25 or newer):

```sh
go install github.com/luank3/gax/cmd/gax@latest
```

Release targets include Linux, macOS, and Windows (amd64, arm64). Archives are attached to
[releases](https://github.com/luank3/gax/releases); Windows archives use ZIP. Unpack `gax`
(or `gax.exe`) into any directory on your `PATH`.

From source:

```sh
git clone https://github.com/luank3/gax.git
cd gax
make build          # the binary is now at ./bin/gax
make install        # go install into GOPATH/bin
```

## Windows

Use Windows 10/11 with Windows Terminal and [Git for Windows](https://gitforwindows.org/).
Run `gax.exe` from PowerShell, cmd, or Git Bash; the agent's `bash` tool always executes
Bash commands, not PowerShell syntax. Bash is discovered beside `git.exe`, on `PATH`, or
in standard Git installation directories. Set `bash.shell` (`GAX_BASH_SHELL`) for a custom path.
Use forward-slash paths inside Bash commands (for example, `'C:/work/my project'`);
file tools accept native Windows paths. MinTTY-style pipe terminals use plain line input;
use Windows Terminal for the full interactive editor.

Build from PowerShell without Make:

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -o bin/gax.exe ./cmd/gax
```

Configuration and sessions keep the layout below, using `%USERPROFILE%` when `HOME` is
unset. XDG overrides are honored. Private files inherit Windows directory ACLs; keep
config/state/temp directories in your user profile or another access-restricted location.
Clipboard text/images use Windows PowerShell's built-in clipboard APIs. Optional `fzf`
completion still requires `fzf`; external editors must be invocable from Git Bash.
Windows has no SIGUSR1 pause signal; use Escape interactively instead.

## Connect a provider

The easiest first run is interactive: start `gax`, then use `/provider` to see the shipped
providers and pick a model. Interactive selections are remembered.

| Provider | Setup |
| --- | --- |
| `codex` | Run `/login` inside gax (ChatGPT subscription). |
| `openai` | Set `OPENAI_API_KEY`. |
| `anthropic` | Set `ANTHROPIC_API_KEY`. |
| `openrouter` | Set `OPENROUTER_API_KEY`. |
| `opencode-go` | Set `OPENCODE_API_KEY`. |
| `deepseek` | Set `DEEPSEEK_API_KEY`. |
| `llama.cpp` | Run `llama-server`, then `gax --provider llama.cpp`. |
| `ollama` | Run `ollama serve`, then `gax --provider ollama`. |
| `openai-compatible` | Set `GAX_OPENAI_BASE_URL` (+ `GAX_OPENAI_API_KEY`). |
| `anthropic-compatible` | Set `GAX_ANTHROPIC_BASE_URL` (+ `GAX_ANTHROPIC_API_KEY`). |

Any other endpoint can be declared as a custom provider under `providers.<name>` in the config
file, then selected with `--provider=<name>`.

## Quick start

Run gax from the project directory you want it to work in:

```sh
gax                          # interactive REPL
gax -p "list the TODOs"      # run one prompt, print the final answer
printf 'explain this\n' | gax -p   # read the prompt from stdin
gax -c                       # continue the latest session for this directory
gax --resume                 # pick a past session for this directory
gax --resume=ID -p "next"    # resume a specific session in one-shot mode
gax review -p "check this"   # apply the preset named "review" for this run
```

CLI options: `-p/--print`, `--json`, `-c/--continue`, `--resume[=ID]`, `--no-session`, `--raw`,
`--bare`, `--provider`, `--model`, `--effort`, `--preset`, `-h/--help`, `-v/--version`. Run
`gax --help` for the full descriptions.

## Slash commands

Typed at the start of a REPL line; `/help` lists them in this order.

| Command | Purpose |
| --- | --- |
| `/new [preset]` (`/clear`) | Start a fresh conversation. |
| `/resume` | Resume a past conversation. |
| `/undo [turns back]` | Revert the conversation to before an earlier message. |
| `/fork [turns back]` | Branch a new session before an earlier message. |
| `/provider [id]`, `/model [id]`, `/effort [level]` | Switch provider, then model and effort. |
| `/preset [name]`, `/preset-save <name> [tint]` | Switch to, or save, a config-defined preset. |
| `/config [key [value]]` | View or change settings. |
| `/compact [focus]` | Summarize the conversation to free up context. |
| `/copy` | Copy the last response to the clipboard. |
| `/tasks [kill <id>... \| kill all]` | List and manage background tasks. |
| `/session` | Show this session's info and usage totals. |
| `/usage` | Show provider account usage (Codex, DeepSeek, OpenRouter, OpenCode Go). |
| `/login [provider]`, `/logout [provider]` | Manage a gax-owned provider login. |
| `/help` | Show help. |

Without an argument, `/provider`, `/model`, and `/effort` open pickers that continue to the
settings that depend on them; with an argument they switch directly. Tab completes command names
and the values a command's first argument enumerates — provider ids, effort levels a model takes,
preset names, and the model ids from the provider's last model listing, so open the `/model` picker
once where the listing is unavailable.

Useful keys: Ctrl-R recalls earlier prompts for this directory, Ctrl-T opens the transcript
view, Ctrl-C interrupts the running turn, Ctrl-D exits.

## Configuration

Configuration is optional. The [stability policy](docs/sessions.md#stability-policy) covers
documented configuration, session logs, and `--json` output. Files follow the XDG layout:

| Path | Purpose |
| --- | --- |
| `$XDG_CONFIG_HOME/gax/config.json` (`~/.config/gax/config.json`) | Settings and presets. |
| `$XDG_CONFIG_HOME/gax/AGENTS.md` | Global instructions added to the system prompt. |
| `$XDG_STATE_HOME/gax/state.json` (`~/.local/state/gax/state.json`) | Remembered selections. |
| `$XDG_STATE_HOME/gax/sessions/<cwd>.<hash>/` | Session logs and prompt history for that directory. |
| `$HOME/.agents/skills/` | Shared skills; project skills live in `<repo>/.agents/skills/`. |

Scalar settings resolve in this order: the current run (`--provider`, `--model`, `--effort`,
`--preset`), a resumed conversation's recorded selection, the environment, `state.json`,
`config.json`, and finally the built-in default. The sentinel value `(default)` stops the lookup
at that tier and restores the built-in default.

Every setting has a `GAX_`-prefixed environment variable (`GAX_PROVIDER`, `GAX_MODEL`,
`GAX_EFFORT`, `GAX_PRESET`, `GAX_THEME`, `GAX_SHOW_REASONING`, `GAX_NO_SESSION`, `GAX_TRACE`,
`GAX_TRANSCRIPT`, `GAX_BASH_TIMEOUT`, `GAX_BASH_COMPRESS`, `GAX_COMPACT_AUTO`, `GAX_CATALOG_URL`, …). Run `/config`
in the REPL for the complete, ordered list; provider and search keys are unprefixed
(`OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, `BRAVE_API_KEY`, `EXA_API_KEY`, …). Credential values are redacted from
diagnostics and wire traces.

Example `config.json`:

```json
{
  "provider": "deepseek",
  "model": "deepseek-chat",
  "theme": "dark",
  "show_reasoning": "on",
  "providers": {
    "openai-compatible": {
      "base_url": "https://example.com/v1",
      "api_key": "$MY_ENDPOINT_KEY"
    }
  },
  "presets": {
    "review": {
      "provider": "anthropic",
      "model": "claude-sonnet-4-6",
      "effort": "high",
      "description": "Careful code review"
    }
  }
}
```

`"$ENV_NAME"` reads a value from the environment; `"$$literal"` escapes it. Presets accept
`description`, `tint`, `provider`, `model`, `effort`, `system_prompt`, and
`system_prompt_append`, and can be selected with the leading shorthand (`gax review`) or
`--preset=review`.

gax ships two built-in skills of its own: `code-intelligence` (which guides the model on
structural search via optional external tools such as `ast-grep`, `ctags`, and `rg`, plus git history
without full-file reads) and `customize-gax` (which tells the model how to change settings, providers,
and presets). They are listed with the discovered skills; loading them resolves the documents from the
binary rather than the filesystem (`gax:skills/<name>/SKILL.md`, which `read` also accepts). A
discovered skill of the same name shadows the built-in.

## Sessions and output

Sessions are recorded per working directory, so `-c` and `--resume` stay scoped to the project
you ran from. Resumed conversations restore their provider, model, effort, and preset unless a
CLI flag overrides them; `--no-session` records nothing. `--raw` sends only your prompt text
(no system prompt, context, or tools) and `--bare` keeps the prompt and tools but drops
project context, skills, and the subagents section.

`--json` implies `-p` and writes newline-delimited JSON to stdout: a `session` header record
with the provider/model/effort/git state, then the conversation items, then a final `result`
record (`outcome`, `turns`, `elapsed_ms`, `text`, `context_tokens`, `cost`, `session_id`).

The agent ships these built-in tools: `read`, `edit`, `write`, `bash`, `task_wait`, `web_fetch`,
`web_search`, and `skill`. `bash` commands that outlive their timeout detach into background tasks, and
`gax -p "<task>"` can be run through `bash` to delegate to a subagent with clean context. Besides
files, `read` accepts the `gax:skills/<name>/SKILL.md` paths of the skills built into the binary.

`skill` loads a skill by name with a `[skill: <name> (<source>)]` header, so the system prompt
lists each skill with its description but without the path of its document: the model loads the one
whose description matches instead of composing a path, and reuses a skill already present in the
active conversation without reloading its full body unless its content changes or leaves active
context after compaction or `/undo`. `no_skills` (or `--bare`) drops both the listing and the tool,
and a discovered skill shadows a built-in of the same name.

`web_fetch` retrieves a URL and converts an HTML response to plain text unless the model passes
`strip: false` for the raw body; `web_search` returns ranked titles, URLs, and snippets. You.com is
the default backend: with `YDC_API_KEY` it answers through the Web Search API, without one through
its keyless free MCP profile (100 queries a day). Brave (`BRAVE_API_KEY`), Octen (`OCTEN_API_KEY`),
Exa (`EXA_API_KEY`), Firecrawl (`FIRECRAWL_API_KEY`), TinyFish (`TINYFISH_API_KEY`), and Keenable
(`KEENABLE_API_KEY`) answer when their key is set. `web.search_provider` pins one of them (or
`duckduckgo`) outright; selecting `keenable` without a key uses its rate-limited public endpoint.
An unavailable backend — rate-limited, broken, or returning an unusable response — falls back
to the keyless DuckDuckGo crawl, which is also the last resort of a fresh install. Both tools
follow `web.timeout` and `web.max_bytes`, and `GAX_NO_WEB=on` (or `no_web` in `config.json`)
drops them from the toolset.

### Bash output compression

`bash` compacts the output of the commands whose shape it recognizes before the model reads it —
the idea behind [rtk](https://github.com/rtk-ai/rtk), applied inside the tool instead of as a
command wrapper. Only a single simple command is eligible: a pipeline, a redirect, a second
command, or a command substitution keeps the raw bytes, because the output then belongs to
something else.

| Command | Compact form |
| --- | --- |
| `ls` (long listings) | One line per entry, directories first, octal mode and size, noise directories filtered |
| `tree` | Counts line first, noise subtrees dropped, body capped |
| `cat` / `head` / `tail` | Large dumps lose comment-only lines and blank runs, capped with a line count |
| `grep` / `rg` | Long lines truncated, matches grouped by file once the caps bite |
| `ast-grep` | Grouped by file, per-file and total caps |
| `git status` | `* branch` header and `XY path` lines grouped by state, in-progress rebase/merge reported |
| `git diff` | File headers and per-file `+N -M` counts, bounded leading context and hunk sizes |
| `git log` | `hash subject (age) <author>` plus up to three body lines |
| `git add` / `commit` / `push` / `pull` / `fetch` / `stash` | One confirmation line (`ok abc1234`, `ok main`, …) |
| `cargo test`, `go test`, `pytest`, `npm test` / `jest` / `vitest` | Failures first, passing tests collapsed to a count |
| `ruff check`, `sqlfluff lint` | Grouped by rule and file, violations capped |
| `docker ps` | `id name (image) status [ports]` |

A filter is used only when its result is smaller than the raw output. **Nothing is dropped
silently:** a compact form that leaves lines out says so (`… +12 more`, `[3 long lines truncated]`,
`… (4 more lines)`, `... (2 deletions truncated)`, `… (28 older commits not shown; pass -n to see
more)`) and the full output is stored in a temp file the result names
(`[full output saved to /tmp/gax-…/bash-1.log]`), which is removed when the conversation's tasks
are reset. If that file cannot be written, the filter's result is discarded and the raw output is
returned instead.

Three escape hatches, for when the compact form is not enough:

- `raw: true` on a `bash` call returns that command's output verbatim, now and when a detached
  task is later collected.
- The named `[full output saved to …]` file holds the complete output, including everything a cap
  or a summary left out.
- `GAX_BASH_COMPRESS=off` (or `bash.compress` in `config.json`) turns the whole thing off.

Test and compiler output is treated as the case that must survive intact. Failing tests keep their
`file:line` references, stack frames, panic locations, and assertion text; passing tests collapse
to a count. `go build`, `cargo build`, and a `go test` or `cargo test` run that fails to *compile*
are never filtered at all — the compiler's diagnostics reach the model byte for byte. The live
terminal display always streams raw bytes too; only what the model reads is compacted.

## Development

```sh
make build       # build ./bin/gax
make test        # go test ./...
make test-race   # go test -race -count=1 ./...
make vet         # go vet ./...
make fmt-check   # fail if gofmt would change anything
make lint        # fmt-check + vet
make cover       # coverage summary
```

The Make targets unset `GAX_PROVIDER`, `GAX_MODEL`, `GAX_EFFORT`, and `GAX_PRESET` before
running tests, because several app tests assert exact one-shot output and would otherwise
inherit an ambient selection. Run the suite through `make test` (or unset those variables
yourself) rather than calling `go test` from a configured shell.

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Coding agents should start
with [AGENTS.md](AGENTS.md) for the repository map and conventions.

## Documentation

- [`docs/sessions.md`](docs/sessions.md) — session log and `--json` record formats.

## License

MIT — see [LICENSE](LICENSE). gax was ported from hax, which is MIT-licensed as well; its
notice is preserved in the license file.

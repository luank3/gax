<div align="center">

# gax

**A minimalist coding agent in your terminal — the Go reimplementation of [hax](https://github.com/OleksandrChekhovskyi/hax).**

[![CI](https://github.com/luank3/gax/actions/workflows/ci.yml/badge.svg)](https://github.com/luank3/gax/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

gax is the Go reimplementation of [hax](https://github.com/OleksandrChekhovskyi/hax), the
minimalist terminal-native coding agent written in C. It targets behavioral parity with the hax
sources it was ported from — baseline `ff45833`, recorded in `internal/version.Value` — and the
porting trail lives in [`docs/superpowers/plans`](docs/superpowers/plans).

## Key features

- **One static binary** — no runtime dependencies and no third-party Go modules. Starts
  instantly and keeps a small footprint, leaving RAM for local models.
- **Local models are first-class** — point gax at a running `llama-server` or `ollama serve` and
  it discovers the served models; llama.cpp also reports its loaded context, output, image, and
  effort capabilities, so no custom provider block is needed for the default setup.
- **Respects your terminal** — streaming Markdown and live tool output, reflowed for the
  current width. Only the streaming region is redrawn, so native scrollback and copy/paste
  survive.
- **Inspectable** — review exactly what was sent and received with the Ctrl-T transcript view,
  or capture a wire-level HTTP/SSE trace with `GAX_TRACE`.
- **Broad provider support** — OpenAI (+ compatible), Anthropic (+ compatible), Codex (ChatGPT
  subscription), OpenRouter, OpenCode Zen/Go, DeepSeek, Agnes AI, llama.cpp, and Ollama.
- **Well-behaved Unix tool** — XDG config/state paths, clean stdout in `-p` one-shot mode with
  resume hints on stderr, plain-text JSON config and session files, scriptable `--json` output.

## Install

With a Go toolchain (1.25 or newer):

```sh
go install github.com/luank3/gax/cmd/gax@latest
```

Prebuilt archives for Linux and macOS (amd64, arm64) are attached to
[releases](https://github.com/luank3/gax/releases). Unpack the `gax` binary into any directory
on your `PATH`.

From source:

```sh
git clone https://github.com/luank3/gax.git
cd gax
make build          # the binary is now at ./bin/gax
make install        # go install into GOPATH/bin
```

## Connect a provider

The easiest first run is interactive: start `gax`, then use `/provider` to see the shipped
providers and pick a model. Interactive selections are remembered.

| Provider | Setup |
| --- | --- |
| `codex` | Run `/login` inside gax (ChatGPT subscription). |
| `openai` | Set `OPENAI_API_KEY`. |
| `anthropic` | Set `ANTHROPIC_API_KEY`. |
| `openrouter` | Set `OPENROUTER_API_KEY`. |
| `opencode-zen` / `opencode-go` | Set `OPENCODE_API_KEY`. |
| `deepseek` | Set `DEEPSEEK_API_KEY`. |
| `agnes` | Set `AGNES_API_KEY`. |
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
| `/provider`, `/model`, `/effort` | Switch provider, then model and effort. |
| `/preset [name]`, `/preset-save <name> [tint]` | Switch to, or save, a config-defined preset. |
| `/config [key [value]]` | View or change settings. |
| `/compact [focus]` | Summarize the conversation to free up context. |
| `/copy` | Copy the last response to the clipboard. |
| `/tasks [kill <id>... \| kill all]` | List and manage background tasks. |
| `/session` | Show this session's info and usage totals. |
| `/usage` | Show provider account usage (Codex, DeepSeek, OpenRouter, OpenCode Go). |
| `/login [provider]`, `/logout [provider]` | Manage a gax-owned provider login. |
| `/help` | Show help. |

Useful keys: Ctrl-R recalls earlier prompts for this directory, Ctrl-T opens the transcript
view, Ctrl-C interrupts the running turn, Ctrl-D exits.

## Configuration

Configuration is optional. Files follow the XDG layout:

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
`GAX_TRANSCRIPT`, `GAX_BASH_TIMEOUT`, `GAX_COMPACT_AUTO`, `GAX_CATALOG_URL`, …). Run `/config`
in the REPL for the complete, ordered list; provider keys are unprefixed (`OPENAI_API_KEY`,
`DEEPSEEK_API_KEY`, …). Credential values are redacted from diagnostics and wire traces.

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

gax ships one skill of its own: `customize-gax`, which tells the model how to change these settings,
providers, and presets. It is listed with the discovered skills and read through the `gax:` path
`gax:skills/customize-gax/SKILL.md`, which `read` resolves from the binary instead of the filesystem. A
discovered skill named `customize-gax` shadows it.

## Sessions and output

Sessions are recorded per working directory, so `-c` and `--resume` stay scoped to the project
you ran from. Resumed conversations restore their provider, model, effort, and preset unless a
CLI flag overrides them; `--no-session` records nothing. `--raw` sends only your prompt text
(no system prompt, context, or tools) and `--bare` keeps the prompt and tools but drops
project context, skills, and the subagents section.

`--json` implies `-p` and writes newline-delimited JSON to stdout: a `session` header record
with the provider/model/effort/git state, then the conversation items, then a final `result`
record (`outcome`, `turns`, `elapsed_ms`, `text`, `context_tokens`, `cost`, `session_id`).

The agent ships these built-in tools: `read`, `edit`, `write`, `bash`, and `task_wait`.
`bash` commands that outlive their timeout detach into background tasks, and `gax -p "<task>"`
can be run through `bash` to delegate to a subagent with clean context. Besides files, `read`
accepts the `gax:skills/<name>/SKILL.md` paths of the skills built into the binary.

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
- [`docs/superpowers/plans`](docs/superpowers/plans) — the per-subsystem porting plans and
  their acceptance criteria.
- [`docs/superpowers/specs`](docs/superpowers/specs) — design notes for larger subsystems.

## License

MIT — see [LICENSE](LICENSE). hax, the C implementation this project ports, is MIT-licensed
as well.

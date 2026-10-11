---
name: customize-gax
description: Change gax's own settings, providers, models, or presets — where config.json lives, which keys exist, what applies live, and what needs a restart.
---

# Customizing gax

gax is the program running you. Its configuration is optional and lives in plain JSON under the
XDG paths. Read this when the user asks to change a setting, model, provider, theme, or preset.

## What you can and cannot do

- You cannot type slash commands: they are REPL input. Ask the user to run one, or change the file.
- You can read and edit the config file with the read/edit/write tools.
- gax reads `config.json` once at startup, so an edit you make applies to the next gax start, not to
  the session you are in. For a key marked **live** below the user can apply it immediately with
  `/config <key> <value>` — that changes the current run only and does not write the file.
- Keep the file valid JSON. A malformed `config.json` is ignored as a whole (gax warns
  `ignoring malformed config at <path>`), so every other setting in it falls back to its default.
- Never echo credential values into the conversation; reference environment variables instead.

## Files

| Path | Purpose |
| --- | --- |
| `~/.config/gax/config.json` | Settings, `providers.<name>` blocks, `presets.<name>` blocks. |
| `~/.config/gax/AGENTS.md` | Instructions gax adds to the system prompt in every directory. |
| `~/.config/gax/skills/<name>/SKILL.md` | Skills only gax reads. |
| `~/.local/state/gax/state.json` | The remembered provider/model/effort/preset selection. |
| `~/.local/state/gax/sessions/<cwd>.<hash>/` | Session logs and prompt history for that directory. |
| `<repo>/AGENTS.md`, `<dir>/.agents/skills/` | Project instructions and skills; the nearest directory wins. |
| `~/.agents/skills/` | Skills shared with other agents; gax's own and project skills win over them. |

`$XDG_CONFIG_HOME` and `$XDG_STATE_HOME` replace `~/.config` and `~/.local/state` when set.

Scalar settings resolve in this order: the current run (`--provider`, `--model`, `--effort`,
`--preset`), a resumed conversation's recorded selection, the environment, `state.json`,
`config.json`, and finally the built-in default. The value `(default)` stops the lookup at that tier
and restores the built-in default.

Provider credentials are unprefixed (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`,
`OPENROUTER_API_KEY`, `OPENCODE_API_KEY`); every other variable is `GAX_`-prefixed
and named in the table below. An `api_key` or `extra_headers` value may be `$VAR`, read from the
environment at startup, with `$$` escaping a literal `$`.

## Settings

`/config` in the REPL lists these in registry order, and `/config <key>` prints the current value
with where it came from (`default`, `config`, `state`, `env`, `conversation`, `run`). **Changeable**
says whether `/config <key> <value>` works on the running instance; `restart` keys only take effect
when gax starts again. `(default)` is accepted as a value anywhere.

<!-- settings-table -->

## Providers

`provider` selects a shipped provider by id, and `/provider` lists them with availability. An object
under `providers.<name>` in `config.json` defines a new provider — that is how a private gateway, a
proxy, or a second endpoint of the same kind is added. Select it with `--provider=<name>` or
`"provider": "<name>"`.

```json
{
  "provider": "gateway",
  "providers": {
    "gateway": {
      "display_name": "Gateway",
      "base_url": "https://gateway.example/v1",
      "api_key": "$GATEWAY_API_KEY",
      "api": "chat",
      "model_apis": {"claude-*": "anthropic-messages"}
    }
  }
}
```

Fields: `base_url`, `api_key` (`$VAR` allowed), `api_key_env`, `display_name`, `api` (`chat` for
Chat Completions, `responses` for the Responses API), `model_apis` (model glob → dialect
`openai-completions`, `openai-responses`, or `anthropic-messages`), `metadata_api`, `catalog_id`,
`sort_models`, `port` (llama.cpp), `extra_body` (JSON merged into every request body; reserved keys
such as `model`, `messages`, and `tools` are rejected), `extra_headers`, and the request-dialect
fields `cache`, `cache_ttl`, `send_cache_key`, `request_cost`, `reasoning_format`,
`reasoning_roundtrip`, `reasoning_required`, `max_tokens`, `thinking_mode`, `thinking_budget`, and
`version`. A field the provider's wire cannot use, and any unknown field, is reported as a warning
and ignored. Keep credentials in the environment rather than in the file.

## Presets

A preset is a named selection applied in one step: `/preset <name>` in the REPL, `gax <name>`, or
`--preset=<name>`. Define one under `presets.<name>` in `config.json`; `/preset-save <name> [tint]`
writes the current selection there.

```json
{
  "presets": {
    "review": {
      "provider": "anthropic",
      "model": "claude-sonnet-4-6",
      "effort": "high",
      "tint": "violet",
      "description": "Careful code review"
    }
  }
}
```

The only presettable keys are `provider` (required), `model`, `effort`, `system_prompt`,
`system_prompt_append` (`@path` reads a file), `description`, and `tint`. Endpoint settings belong in
a `providers.<name>` block; context and recording switches belong on the command line (`--raw`,
`--bare`, `--no-session`).

## Prompt, skills, and sessions

- `system_prompt` replaces the built-in base prompt and `system_prompt_append` adds text after it;
  `(none)` sends no system message at all. Both accept `@path`.
- `no_env`, `no_agents_md`, `no_subagents`, and `no_tasks` drop whole prompt sections; `no_skills`
  drops the `skill` tool, so no skill can be listed or loaded.
- New skills go in `~/.config/gax/skills/<name>/SKILL.md` (gax only) or `<repo>/.agents/skills/`
  (project, shared with other agents). The frontmatter `description` is what the `skill` tool lists
  and the body is loaded on demand. A discovered skill with the same name shadows a built-in one, so this
  skill is overridden by putting a `customize-gax` skill in any discovered directory.
- Sessions, `-c`/`--resume`, `/undo`, and `/fork` are covered in gax's README.

Every setting has the `GAX_`-prefixed variable named in the table above; those variables are read
after the run flags and a resumed selection, and before `state.json`. Setting one for a single run is
often easier than editing the file:

```sh
GAX_THEME=dark GAX_SHOW_REASONING=on gax
gax --provider=openai-compatible --model=some-model -p "summarize the diff"
```

# Session and JSON record formats

gax writes two newline-delimited JSON streams: the **session log** it records on disk, and the
**`--json` output** it streams on stdout in one-shot runs.

## Session logs

Sessions are stored per working directory:

```text
$XDG_STATE_HOME/gax/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl
```

`<encoded-cwd>` is the project directory path with `/` replaced by `-` (truncated to 79 bytes)
plus a `.` and a 16-digit hash of the full path. `<timestamp>` is `2006-01-02T15-04-05Z` UTC and
`<uuid>` identifies the session for `--resume=<id>`. The prompt history for that directory lives
beside the logs in `history`.

Each line is one JSON object. The first line is always the session header; the remaining lines
are appended in the order the run produced them.

### Header record

```json
{"type":"session","version":1,"gax_version":"ff45833","id":"…","timestamp":"2026-10-11T11:26:17Z","cwd":"/home/you/project","provider":"deepseek","model":"deepseek-chat","effort":"high","git_branch":"main","git_commit":"ec77f8d","git_subject":"Initial Go reimplementation of hax"}
```

| Field | Meaning |
| --- | --- |
| `type` | Always `session`. |
| `version` | Format version; currently `1`. |
| `gax_version` | Build version of gax that recorded the session. |
| `id`, `timestamp`, `cwd` | Session identity and origin. |
| `provider`, `model`, `model_label`, `effort`, `preset` | Selection the session started with; `model_label` appears only when the provider labels the model differently from its id. |
| `git_branch`, `git_commit`, `git_subject` | Repository state at the start, when the directory is a Git worktree. |
| `forked_from` | Session id this one was branched from with `/fork`. |

### Selection and turn records

Selection changes (`/provider`, `/model`, `/effort`, `/preset`, `--resume` with a new selection)
append a selection record, and every completed user turn appends its mapping latency:

```json
{"type":"selection","provider":"anthropic","model":"claude-sonnet-4-6","effort":"high"}
{"type":"user_turn","elapsed_ms":4213}
```

### Conversation items

Every other line is a conversation item. `kind` determines which fields are meaningful:

| `kind` | Fields |
| --- | --- |
| `user` | `text`, `images` |
| `assistant` | `text`, `origin` |
| `tool_call` | `call_id`, `tool_name`, `arguments` |
| `tool_result` | `call_id`, `output`, `output_hidden_tail`, `images` |
| `reasoning` | `reasoning_text`, `reasoning_json`, `reasoning_field`, `provider`, `model` |
| `turn_boundary` | Separates turns for compaction and undo. |
| `turn_usage` | `usage`, `provider`, `model`, `origin`, `inherited` |

Items may also carry `provider`, `model`, `origin`, and `usage`. `origin` marks special
provenance — `compact_seed`, `continuation`, `interrupted`, `skipped`, `refused`, `summarized`,
`task_note`, or `compaction`. `output_hidden_tail` counts bytes the tool output cap dropped, and
`inherited` marks usage carried over from a resumed conversation.

The `usage` object holds the provider's accounting for one response; omitted members were not
reported, and reported zeros are preserved:

```json
{"input":1200,"output":340,"cached":900,"cache_write":0,"cache_write_1h":0,"cost":0.0042,"elapsed_ms":8100,"in_tokens":300,"cost_in":0.0011,"cost_cache_read":0.0002,"cost_cache_write":0,"cost_out":0.0029,"cost_total":0.0042,"cost_estimated":false,"provider_label":"deepseek","model_label":"DeepSeek V4","effort":"high","served_model":"deepseek-chat","route":"chat","response_id":"…"}
```

### Undo, fork, and compaction

`/undo` and `/fork` truncate or copy the log at an item boundary, so a session file always
contains a contiguous history. `/compact` appends a `turn_boundary` item, a `user` item with
`origin: "compact_seed"` holding the summary, and a `turn_usage` item with
`origin: "compaction"`. The transcript view (Ctrl-T) renders the same stream for inspection.

## `--json` output

`--json` implies `-p` and streams to stdout instead of the plain answer: the session header
record, then conversation items as they are produced, then one final `result` record. The
conversation is still recorded unless `--no-session` (or an internal provider such as `mock`)
disables recording, and its id is reported as `session_id`. Fields marked optional are omitted
when unset.

```json
{"type":"result","outcome":"complete","text":"final answer","turns":3,"elapsed_ms":12980,"context_tokens":18422,"cost":0.0131,"cost_estimated":false,"session_id":"…"}
```

| Field | Meaning |
| --- | --- |
| `type` | Always `result`. |
| `outcome` | `complete`, `error`, `interrupted`, `paused`, or `max_turns`. |
| `text` | Final assistant message; present only for a completed run with a non-empty answer. |
| `error` | Diagnostic for a failed run. |
| `turns` | Model round-trips performed. |
| `elapsed_ms` | Wall-clock duration of the run. |
| `context_tokens` | Context size reported by the last response. |
| `cost`, `cost_estimated` | Spend for the run and whether it was derived from catalog rates rather than provider-reported cost. |
| `session_id` | Session id, so a later `--resume=<id>` can continue the conversation. |

Exit status follows the run: `0` for `complete`, `130` for `interrupted`, and `1` for the other
outcomes (a paused run also prints `gax: paused at a turn boundary` on stderr). Diagnostics stay
on stderr, so stdout remains machine-readable.

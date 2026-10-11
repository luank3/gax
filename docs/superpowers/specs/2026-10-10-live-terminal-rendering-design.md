# Live terminal rendering design

## Goal

Port Hax's live interactive terminal presentation to Go. In a TTY REPL, assistant text and
reasoning follow the provider stream, retries and stream boundaries update the display correctly,
and tool calls/results appear while tools run. The session history and model-facing tool results
remain canonical and independent of presentation.

The renderer is enabled for the interactive TTY REPL. One-shot mode keeps its final-answer output,
and JSONL and other non-TTY modes keep their existing output contracts.

## Current state

`agent.RunTurn` forwards provider-independent `conversation.Event` values through
`LoopOptions.Observe`, and `RunLoop` exposes turn, tool-call, and checkpoint hooks. The app does not
currently set `Observe` for the interactive REPL. It waits for a completed loop, gathers assistant
text from session items, and renders the full answer with `terminalmarkdown.Render`.

The built-in registry executes tools without a display sink or per-tool presentation metadata.
Upstream passes a synchronous display callback into tool execution and uses tool metadata to select
argument formatting, preview mode, diff rendering, and collapsed exploration rows. Bash, task-wait,
and write can emit content while executing; the final model-facing result can include annotations
that should not be repeated in the user-facing display.

## Design

Add a stateful renderer under `internal/terminal` that owns the active display mode and consumes
stream events. Its Markdown engine is a Go port of the upstream streaming renderer, not an adapter
that only preserves the current Go renderer's smaller feature set. It retains upstream behavior
for inline emphasis and code, headings, fences, lists, thematic breaks, blockquotes, soft and hard
breaks, em-dash spacing, bare links, GFM tables (including alignment, reflow, and bounded
collection), terminal-width wrapping, ANSI style transitions, and ambiguous syntax split across
feeds. The existing `Render` function uses the same engine so static and live output cannot drift.
The live renderer tracks visible content, closes or transitions blocks at reasoning/tool
boundaries, and resets Markdown state when a retry abandons a partial attempt. A partial attempt
that was already visible is closed with the upstream-style unexpected end marker before the retry
begins. Reasoning display follows the existing configuration setting. TTY sizing, ANSI styles,
and delayed status labels remain renderer concerns; the app supplies the terminal writer and
configuration.

Extend tool execution with a display-only byte sink and presentation metadata. Metadata covers the
display argument and optional suffix, plain versus unified-diff output, head/head-tail/collapsed
preview mode, per-call preview selection, and collapsed argument formatting. Execution continues
to return the complete canonical `conversation.Item` for provider history. A tool can report that
its returned text summarizes bytes already streamed to the display, allowing the renderer to avoid
duplicating those bytes. Registry methods that do not receive a display sink retain their current
behavior.

Wire the renderer in `internal/app` only when the REPL has a TTY pair and JSON mode is off. The
observer handles streamed assistant events. The existing tool-call hook renders the complete call
after argument assembly and routes tool execution through the registry's display-aware path. Tool
rows, clustered exploration calls, streamed output, final previews, and status footers use the
same renderer state so they do not interleave or repeat output. One-shot, JSON, and line-oriented
non-TTY paths continue to use their current end-of-turn behavior.

## Error and lifecycle behavior

Provider retries reset only the visible attempt renderer; conversation assembly and retry usage
continue to follow the existing agent-loop rules. Provider errors, cancellation, and pauses close
the active display mode without fabricating a final answer. Tool errors remain ordinary tool
results and render through the same preview policy as successful results. Display writer errors
are latched outside conversation history and reported through the app's existing output-error
path; they never become model-facing tool text. A streaming adapter checks a latched error at the
next stream tick or loop checkpoint so the app can stop before launching another turn.

The renderer is per app run and is not persisted in session files. Streamed presentation never
changes event assembly, tool-result contents, JSONL records, or session history.

## Verification

Unit tests port the upstream Markdown regression behavior into Go and cover arbitrary chunk
partitions (including split UTF-8 and inline markers), immediate prose emission, table buffering,
block boundaries, wrapping and width edge cases, ANSI style state, retry resets, and writer errors.
Renderer tests cover event boundaries, retry markers and resets, reasoning visibility, tool preview
selection, streamed-versus-returned output, unified-diff output, and collapsed tool clusters. App
integration tests use a pseudo-terminal and a local streaming provider to verify text appears before
stream completion, retries do not duplicate the final attempt, and live tool output is not printed
again from the tool result. Writer-error tests verify diagnostics stay out of conversation history.
Existing one-shot, JSONL, non-TTY, session, and tool-history tests remain regression coverage.

## Scope boundary

This design covers the interactive renderer and its tool-display integration. Session picker,
slash-command completeness, compaction policy, and usage/cost parity remain separate open work in
the command-entrypoint plan.

## Upstream reference

hax `src/agent.c`, `render/render_ctx.c`, `agent_dispatch.c`, `agent_tool.c`,
`tool.h`, and tool implementations at upstream commit
`ff458337f8ba7366d71fad4da0210a53b658678a`.

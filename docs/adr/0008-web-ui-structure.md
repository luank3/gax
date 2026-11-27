# ADR-0008: Web UI Structure & Design System

## Status
Accepted (Phase 1)

## Context
`gax serve` serves a static Preact single-page app embedded in the Go binary. Requirements:
- Dense, utilitarian, terminal-like aesthetic; no gradients, glassmorphism, emoji decoration, or stock dashboard cards.
- Design tokens (color, type, spacing) defined first and styled from them.
- Keyboard-first, usable at mobile width, respects `prefers-color-scheme`.
- Minimal JS dependencies: `preact` only at runtime (no router or state management library).

## Options Considered
1. **Single-view Preact app with URL hash state (`#project=<id>&session=<id>`) and `useReducer`/`useState`.**
   - *Pros*: Zero router or state-store dependencies; ~6.7 KiB gzipped JS including Preact; works seamlessly from `embed.FS`.
   - *Cons*: Manual hash sync (~25 LOC).
2. **Add `preact-router` + `@preact/signals` / `zustand`.**
   - *Rejected*: Unnecessary bundle weight and dependency surface for a 2-pane workspace (sidebar + conversation).

## Decision
Build a compact Preact app under `web/src/`:
- **Design tokens (`web/src/tokens.css`)**:
  - Colors mapped to CSS custom properties with `@media (prefers-color-scheme: light)` and dark default: `--bg-base`, `--bg-surface`, `--bg-elevated`, `--border`, `--text-primary`, `--text-muted`, `--accent` (teal), `--danger`, `--warn`.
  - Typography: monospace-first (`ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`), `13px` base, `1.45` line-height, `tabular-nums` for token counts and timings.
  - Spacing: compact `4px` base unit (`4px`, `8px`, `12px`).
- **Layout**:
  - Desktop (`>= 768px`): collapsible `220px` project/session rail on the left + full-height streaming conversation pane on the right (header with cwd/model/tokens/cost + scrollable event log + sticky prompt input).
  - Mobile (`< 768px`): single-column view with a toggleable project/session drawer.
- **Keyboard shortcuts**:
  - `Ctrl+K` / `/`: focus project/session switcher
  - `Enter`: send prompt (`Shift+Enter` for newline)
  - `Escape`: pause running turn (`Escape` twice: abort)
  - `Ctrl+L`: clear local scrollback filter / focus prompt

## Consequences
- Total JS (Preact + app) stays ~7–15 KiB gzipped, leaving plenty of headroom under the `60 KiB` total gzipped asset budget.
- No runtime router or state library required.

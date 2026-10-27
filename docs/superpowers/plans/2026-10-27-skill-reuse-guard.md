# Skill Reuse Policy and Active-Context Guard Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent redundant skill reloads across `continue` prompts and multi-turn workflows by clarifying skill reuse semantics in the system prompt and `skill` tool description, wrapping loaded skill content with an explicit `[skill: <name> (<source>)]` header, and guarding `skill(name)` against reloading an unchanged document already present in active conversation context.

**Architecture:**
- `internal/agent/prompt.go` prepends the skill reuse policy (`skillsPromptPreamble`) to the `# Skills` section before the `- <name>: <description>` catalogue.
- `internal/tools/skill.go` updates `skillDefinition()` with reuse guidance, formats full skill documents with a `[skill: <name> (<source>)]` header line, and checks active conversation items (supplied by `Registry.SetActiveItems`) before returning a duplicate full body.
- `internal/agent/loop.go` wires `options.Session.Context().Items` into `options.ToolRegistry.SetActiveItems` so the active floor tracks `conversation.ContextFloor` across compaction, `/undo`, `/fork`, and `/new`.

**Tech Stack:** Go standard library (`internal/agent`, `internal/tools`, `internal/conversation`).

## Constraints

- Standard library only: `go.mod` has no `require` block and must keep none.
- Do not advertise a `reload` tool parameter unless the tool supports it.
- Only a complete, successful skill load in the active conversation (`Session.Context().Items`) satisfies the runtime reuse guard; catalogue listings (`skill({})`), errors, and compaction summaries mentioning a skill name do not.
- If a skill's source or content on disk changes, or if its full document leaves active context after compaction, `/undo`, or session reset, the next `skill(name)` call must return the full document again.
- Ordinary `read` calls are unaffected.

## Tasks

- [x] Update `buildSkills` in `internal/agent/prompt.go` to include the active-conversation reuse and continuation policy.
- [x] Update `skillDefinition` in `internal/tools/skill.go` to instruct the model to check active conversation before calling `skill` again, and wrap loaded skill bodies with `[skill: <name> (<source>)]`.
- [x] Add `SetActiveItems` to `internal/tools.Registry`, wire it in `internal/agent.RunLoop` to `Session.Context().Items`, and return a short reuse notice from `runSkill` when the exact full skill document is already present in active context.
- [x] Update and add unit tests in `internal/agent/prompt_test.go`, `internal/tools/skill_test.go`, and `internal/agent/loop_test.go`.
- [x] Update `README.md`, `CHANGELOG.md`, and `docs/superpowers/specs/2026-10-12-agent-prompt-spec.md`.
- [x] Verify with `make lint test-race`.

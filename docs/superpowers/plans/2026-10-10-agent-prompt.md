# Agent system prompt assembly

## Goal

Port upstream base-prompt selection and the dynamic suffix containing environment details, project `AGENTS.md` instructions, and skill descriptions.

## Tasks

- [x] Add tests for raw/none modes, configured prompt files, fallback behavior, suffix ordering, project instruction precedence, and nearest skill shadowing.
- [x] Implement prompt assembly and expose it for agent session setup.
- [x] Run focused and repository-wide tests, race checks, build, vet, and whitespace checks.
- [x] Port preset role listings and exact platform/environment display details.

## Source

`/tmp/luank3-hax-upstream/src/agent_core.c`, `agent_env.c`, `config.c`, and `text/frontmatter.c` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

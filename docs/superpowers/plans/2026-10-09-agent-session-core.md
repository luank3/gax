# Agent session core

## Goal

Port the provider-independent agent conversation state from upstream `agent_core.c`, including context floors, continuation and interrupt repair, retired history, usage bookkeeping, and turn absorption.

## Boundaries

This slice owns session state and transformations only. Provider setup, tools, rendering, persistence orchestration, and the outer request loop remain separate consumers.

## Tasks

- [x] Add behavior tests for context selection, turn boundaries, retirement, interrupt/resume state, usage, and turn absorption.
- [x] Implement the session state and transformations using existing `conversation` and `provider` types.
- [x] Run focused, repository-wide, race, build, and vet checks.

## Source

`/tmp/luank3-hax-upstream/src/agent_core.c` and `agent_core.h` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

# Agent session core

## Goal

Port the provider-independent agent conversation state, including context floors, continuation and interrupt repair, retired history, usage bookkeeping, and turn absorption.

## Boundaries

This slice owns session state and transformations only. Provider setup, tools, rendering, persistence orchestration, and the outer request loop remain separate consumers.

## Tasks

- [x] Add behavior tests for context selection, turn boundaries, retirement, interrupt/resume state, usage, and turn absorption.
- [x] Implement the session state and transformations using existing `conversation` and `provider` types.
- [x] Run focused, repository-wide, race, build, and vet checks.


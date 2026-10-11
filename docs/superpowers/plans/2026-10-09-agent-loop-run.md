# Agent loop continuation engine

## Goal

Port upstream `agent_loop_run` behavior around streamed turns: tool batches, refusal and skip results, pause/abort checkpoints, per-attempt usage, continuation boundaries, and the compaction seam.

## Tasks

- [x] Test tool refusal and execution, max-turn stopping, pause and abort behavior, compaction seams, and final context usage.
- [x] Implement the continuation loop and provider metadata integration.
- [x] Run focused, repository-wide, race, build, and vet checks.

## Source

`/tmp/luank3-hax-upstream/src/agent_loop.c` and `agent_loop.h` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

# Agent loop continuation engine

## Goal

Port upstream `agent_loop_run` behavior around streamed turns: tool batches, refusal and skip results, pause/abort checkpoints, per-attempt usage, continuation boundaries, and the compaction seam.

## Tasks

- [x] Test tool refusal and execution, max-turn stopping, pause and abort behavior, compaction seams, and final context usage.
- [x] Implement the continuation loop and provider metadata integration.
- [x] Run focused, repository-wide, race, build, and vet checks.
- [x] Observe every streamed tool call through `tool_seen` before dispatching any of them, and observe the repair range of a failed or cancelled turn, matching upstream `loop_observe_tools` at all three call sites (`agent_loop.c:332`, `:359`, `:385`). The port previously observed calls inside the dispatch loop and skipped the two abort paths, so a turn that failed mid-stream never reported its calls to the frontend.

## Source

hax `src/agent_loop.c` and `agent_loop.h` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

# Agent loop continuation engine

## Goal

Port the streamed-turn behavior: tool batches, refusal and skip results, pause/abort checkpoints, per-attempt usage, continuation boundaries, and the compaction seam. tool batches, refusal and skip results, pause/abort checkpoints, per-attempt usage, continuation boundaries, and the compaction seam.

## Tasks

- [x] Test tool refusal and execution, max-turn stopping, pause and abort behavior, compaction seams, and final context usage.
- [x] Implement the continuation loop and provider metadata integration.
- [x] Run focused, repository-wide, race, build, and vet checks.
- [x] Observe every streamed tool call through `tool_seen` before dispatching any of them, and observe the repair range of a failed or cancelled turn, at all three call sites (dispatch, refusal, and abort). The port previously observed calls inside the dispatch loop and skipped the two abort paths, so a turn that failed mid-stream never reported its calls to the frontend.


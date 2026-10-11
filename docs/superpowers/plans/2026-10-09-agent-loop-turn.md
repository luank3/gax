# Agent loop turn and abort repair

## Goal

Port one provider round-trip and its abort repair from upstream `agent_loop.c`, preserving retry usage, assembled output, and replay-safe tool-call history.

## Tasks

- [x] Test event observation, retry reset/accounting, provider errors, user-cancel repair, and usage detection.
- [x] Implement the provider stream turn wrapper and abort repair.
- [x] Run focused, repository-wide, race, build, and vet checks.

## Source

hax `src/agent_loop.c` and `agent_loop.h` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

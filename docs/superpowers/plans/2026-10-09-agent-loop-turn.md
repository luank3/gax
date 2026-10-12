# Agent loop turn and abort repair

## Goal

Port one provider round-trip and its abort repair, preserving retry usage, assembled output, and replay-safe tool-call history.

## Tasks

- [x] Test event observation, retry reset/accounting, provider errors, user-cancel repair, and usage detection.
- [x] Implement the provider stream turn wrapper and abort repair.
- [x] Run focused, repository-wide, race, build, and vet checks.


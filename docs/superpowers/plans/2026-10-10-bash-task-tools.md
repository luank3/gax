# Bash and task tools

## Goal

Port the `bash` and `task_wait` execution behavior: shell selection, bounded combined output, timeouts that detach into tasks, named background tasks, task collection and cancellation, process-group cleanup, and credential-safe child environments.

## Tasks

- [x] Add behavior tests for schemas, command output and status, background launch/collection, timeout detachment, task cancellation and cross-task completion, validation, environment scrubbing, and orphan cleanup.
- [x] Implement the shell runner and task manager in the built-in tool registry.
- [x] Run focused and repository-wide tests, race checks, build, vet, and whitespace checks.
- [x] Match synchronous Bash and task-wait truncation summaries, retaining each saved full-output log until registry shutdown.
- [x] Strip a leading `cd … &&` only when its target is proven to be the current working directory.
- [x] Port remaining display hooks.
- [x] Match output-drain thresholds and task-report wording, including turn notes, conversation reset, and shutdown.


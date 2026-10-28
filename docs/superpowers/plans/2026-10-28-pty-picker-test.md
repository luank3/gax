# PTY Picker Test Synchronization Plan

**Goal:** Prevent the provider-picker PTY test from using stale output and racing terminal cleanup on failure.

**Reference:** `docs/superpowers/plans/2026-10-12-darwin-ci-fixes.md` and `docs/superpowers/specs/2026-10-12-app-core-spec.md`.

## Tasks

- [x] Exercise cleanup against a running REPL and blocked capture reader; confirm the initial cleanup implementation fails.
- [x] Close the master, wake the blocked capture read, wait for the REPL to exit, then close the slave; test the cleanup under the race detector.
- [x] Wait for editor repaint after provider selection and after `/session`, and require a new session-result occurrence before Ctrl-D.
- [x] Run focused repeated race tests and `make lint test-race`; record limitations.

## Verification

- A master-close-only diagnostic timed out waiting for the active REPL; waking the capture reader allowed it to stop. The cleanup regression test passed 20 times under `-race`.
- The picker test passed 10 times under `-race` pinned to one CPU, and `make lint test-race` passed.
- The original intermittent failure occurred once during a single-CPU PTY run; this change addresses the observed stale-output wait and cleanup race, not the maintenance review's unreproduced provider-event timeouts.

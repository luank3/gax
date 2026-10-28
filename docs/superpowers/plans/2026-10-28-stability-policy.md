# Stability Policy Documentation Plan

**Goal:** Record the approved pre-1.0 compatibility policy for configuration, session logs, and JSON output without changing runtime behavior.

**Reference:** `docs/superpowers/specs/2026-10-12-session-core-spec.md` and `docs/superpowers/plans/2026-10-09-config-provider-registry.md`.

## Tasks

- [x] Add the approved policy to `docs/sessions.md`, distinguishing documented expectations from implemented migration or version rejection.
- [x] Link the policy from README configuration guidance and note it in the unreleased changelog.
- [x] Run `make lint test-race` and verify the documentation diff.

`make lint test-race` and `git diff --check` passed on Linux. No runtime formats or migration behavior changed.

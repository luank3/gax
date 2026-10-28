# Maintenance Intake and Dependency Guard Plan

**Goal:** Enforce the existing no-third-party-module policy and improve public issue intake without changing GitHub repository settings.

**Architecture:** A small POSIX shell check reads `go mod edit -json` and uses `jq` to reject require and tool directives; a shell regression test exercises real temporary module files. CI runs the regression test and policy check on Ubuntu. GitHub issue forms collect reproduction details and warn that traces may contain private data; Dependabot updates Actions only.

**Tech Stack:** Go toolchain, POSIX shell, jq, GitHub Actions and issue forms.

**Reference:** Maintenance review of commit `2d6d96e`; `docs/superpowers/plans/2026-10-23-windows-support.md` records the existing CI contract.

## Constraints

- No third-party Go modules, GitHub settings changes, commits, or automatic provider calls.
- Fail closed when `go mod edit` fails; reject both grouped and single require/tool directives.
- Never require a public trace; warn that traces can contain prompts and other sensitive content.
- Keep the Windows and macOS test jobs unchanged.

## Tasks

- [x] Add a failing shell regression test for clean, commented, grouped, indirect, tool, and malformed module files; run it before implementing the guard.
- [x] Implement the guard and wire its tests and policy check into Ubuntu CI; rerun the regression tests.
- [x] Add bug, provider, and feature issue forms and weekly GitHub Actions Dependabot configuration.
- [x] Validate configuration and run `make lint test-race`; review the diff and record verification.

## Verification

- The regression test first failed because the guard was absent, then passed with the implemented guard; the repository policy check also passed.
- Ruby Psych parsed the issue forms, Dependabot configuration, and CI workflow as YAML. GitHub's hosted form and workflow validation remains unverified until the changes are published.
- `make lint test-race` passed on Linux.

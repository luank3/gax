# Windows Support Implementation Plan

> **For agentic workers:** Use executing-plans to implement this plan task-by-task. No commits or subagent dispatch without user authorization.

**Goal:** Ship native Windows gax using Git Bash without regressing Unix behavior.
**Architecture:** Platform-specific filesystem, process, path and console backends; reuse existing agent/tool/editor logic.
**Tech Stack:** Go standard library, Win32 through syscall, cgo disabled.
**Spec:** docs/superpowers/specs/2026-10-23-windows-support-design.md

## Global Constraints
- No third-party modules; keep Linux/macOS behavior and security guarantees.
- Git Bash shell contract, not PowerShell command syntax.
- Preserve unrelated untracked codegraph documents.
- No commits, branches, or destructive git commands.

## Review Focus
- Cancellation must kill grandchildren even when the shell exits first.
- Active session files and auth updates must be protected across processes.
- Reparse points must not allow session file truncation or unsafe pruning.
- Windows paths containing spaces, drive letters, UNC roots and Unicode must work.
- Raw console mode must restore on errors, cancellation and modal tools.

### Task 1: Native filesystem and session safety
Files: internal/system/fs/{native_unix,native_windows}.go; internal/session/{load,label,log,fork,prune}.go; internal/provider/codex/store_lock_*.go.
- [x] Add Windows regression tests for no-follow opens, lock contention/release and timestamp updates; observe failure.
- [x] Introduce OpenNoFollow, Lock/Unlock, TouchFile and HasLinks primitives, with Unix implementations preserving existing flags.
- [x] Route session/auth operations through primitives; preserve all file identity checks.
- [x] Run Make tests and Windows compile/test verification.

### Task 2: Shell, paths and command tree lifecycle
Files: internal/system/{path,shell}; internal/tools/bash_process_*.go and bash.go; internal/app/signals*.go; editor/file-picker integrations.
- [x] Add tests for Windows paths/home fallback/shell discovery and whole-tree termination; observe failures.
- [x] Add platform path/shell behavior and Windows Job Object lifecycle with suspended start.
- [x] Replace Unix signal dispatch with platform functions without changing Unix escalation.
- [x] Run targeted Make tests, Windows smoke tests and cross-build.

### Task 3: Console and clipboard
Files: internal/system/console/*_windows.go; internal/terminal/{input,picker,width,paste}/*windows.go.
- [x] Add tests for input decoding, raw mode restoration, pipe rejection and terminal size.
- [x] Implement Win32 console VT/raw IO and reuse editor/picker logic; implement interrupt watcher stop/join.
- [x] Add native clipboard text/image capture and text copy.
- [x] Compile native tests and run available Windows smoke tests; explicitly record any interactive validation still needed.

### Task 4: CI, releases and verification
Files: .github/workflows/ci.yml; .goreleaser.yaml; Makefile; README.md.
- [x] Add Windows native CI and amd64/arm64 cross-builds, .exe output and ZIP packaging.
- [x] Adapt platform-sensitive tests with native replacements where functionality exists.
- [x] Run make lint test-race, all cross-builds, native Windows checks where available.
- [x] Review diff, update this plan with outcomes and limitations.

## Execution record
- Initial Windows amd64 build fails on Unix-only session and bash syscalls.
- WSL Windows PowerShell interop is available; native tests run as Windows executables with fixtures copied into temporary Windows directories.
- Implemented filesystem handle safety, out-of-band Windows byte-range locks, auth-store locks, suspended-start Job Objects, Git Bash discovery, native path/home/temp handling, console input/VT output, picker/interrupt integration, clipboard helpers, browser launching, CI and ZIP releases.
- Native tests exposed Windows ENOTDIR aliasing ERROR_PATH_NOT_FOUND, POSIX-only temp defaults, and directory fsync failures; fixed these at their platform boundaries rather than suppressing diagnostics.
- Self-review found and fixed missing-home tilde expansion, truncated UTF-8 session bucket names, and VT mode being restored too early between prompts and model output. Regression tests cover all three, including nested/emergency console restoration.
- `make lint test-race` passes on Linux with inherited provider endpoint/credential/trace/transcript overrides removed. An initial baseline failure in `TestRunREPLPresetUsesProviderDefaultAndDisplaysPresetErrors` was caused by ambient GAX_OPENAI_BASE_URL; no unrelated production/test change was made for it.
- Full Windows amd64 suite passes via `GOOS=windows make test TEST_FLAGS='-exec=/tmp/gax-windows-exec -count=1 -timeout=120s'`. The wrapper is local verification infrastructure, not a shipped dependency. Native console injection verifies Unicode input, editor/modal mode handoff, Escape pause/abort and stop/join; Job Object tests verify grandchild termination on kill and handle close.
- Cgo-free Make builds pass for linux/darwin/windows × amd64/arm64; Windows vet passes. `bin/gax.exe` is the Windows amd64 artifact. Its native `--version` and isolated mock one-shot invocation both succeed.
- Windows-native CI and GoReleaser ZIP configuration were added in the initial implementation; release packaging has not been published. The first remote CI result and its formatting follow-up are recorded below.

## Verification limits and deliberate platform differences
- Windows arm64 is cross-compiled, not runtime-tested here. A visual Windows Terminal smoke check remains recommended before release.
- Symlink tests skip only when Windows denies the required symlink privilege; POSIX script fixtures are skipped on Windows. Go-subprocess compression fixtures skip locally when no Windows Go executable is available (the native CI runner installs Go).
- Native clipboard helpers use built-in Windows PowerShell in STA mode. Automated Windows tests avoid modifying the host clipboard. An initial legacy clipboard fixture accidentally wrote `latest answer` to the host clipboard before being isolated; this was disclosed during implementation.
- Windows private-file access relies on inherited profile/directory ACLs rather than Unix mode bits. Keep custom config/state/temp roots access-restricted. Directory flushing is not available through Windows directory handles; replacement file contents are flushed before rename.
- Git Bash commands retain POSIX quoting and use forward-slash paths. No PowerShell/cmd tool syntax or SIGUSR1 emulation is introduced. Full interactive editing requires a native Windows console/Windows Terminal; MinTTY-style pipes fall back to plain line input.
- Review was performed inline without subagents; no commits or branches were created during the initial implementation, and the pre-existing untracked codegraph documents were left untouched. The user subsequently authorized commit/push as `e8c3181`.

## CI follow-up: preserve Go source line endings

The first remote run for `e8c3181`, https://github.com/luank3/gax/actions/runs/39828577659,
passed Linux/macOS tests and all six cross-builds, but Windows stopped at `fmt-check`
before running tests. The committed source is gofmt-clean; a checkout with
`core.autocrlf=true` reproduces the failure by converting all 346 Go files to CRLF.

- [x] Reproduce the Windows checkout formatting failure in a temporary export using `git -c core.autocrlf=true checkout-index`, then run the existing Make `fmt-check` target against it.
- [x] Add `.gitattributes` with `*.go text eol=lf`; do not change application source or global Git configuration.
- [x] Repeat the checkout simulation: zero Go files contain CRLF and `make fmt-check` passes, both in the export and the working tree.
- [x] Run `make lint test-race` with the same isolated environment as the initial verification; all checks pass.

Run `f7a77e2` (https://github.com/luank3/gax/actions/runs/39830489256) confirmed the LF
rule fixes `fmt-check` and Windows vet. It then exposed the test failures addressed below.

## CI follow-up: embedded assets and portable test fixtures

- [x] Reproduce embedded skill failures from a `core.autocrlf=true` export: Markdown is embedded verbatim, so CRLF breaks frontmatter and exact tool-output assertions.
- [x] Extend `.gitattributes` with `*.md text eol=lf`; keep embedded documents identical across checkouts rather than adding runtime normalization.
- [x] Reproduce both app-output failures with `TERM=xterm-256color`, `COLORTERM=truecolor`, and automatic theme selection after a preceding app run.
- [x] Pin and restore the palette for the Markdown-output unit test; pin `GAX_THEME=off` and restore the previous palette for the recorded banner-spacing test. Keep production auto-theme behavior unchanged.
- [x] Add regular-file and symlink subcases to the write-permission test, reproduce the unsupported `0750` assertion on native Windows without requiring symlink privilege, then compare against the mode reported after chmod. Verify updated contents as well as supported permissions.
- [x] Verify the original Windows permission failure now passes for regular files; symlink coverage passes on Linux and remains capability-dependent on native Windows.
- [x] Repeat the simulated Windows checkout: Go/Markdown files remain LF, formatting passes, and embedded skill/read/tool tests pass.
- [x] Run the complete native Windows suite and vet against that export with `TERM=xterm-256color`, `COLORTERM=truecolor`, and `GAX_THEME` unset (default auto detection); all checks pass.
- [x] Run `make lint test-race` with inherited provider endpoint/credential/trace/transcript overrides removed; all checks pass.
- [x] Run Linux and Windows amd64 cgo-free Make builds; both pass.

The local Windows wrapper defers temporary executable cleanup until after the suite so
transient Windows image locks cannot turn passing tests into harness failures. A first
expanded simulation explicitly set `GAX_THEME=auto`, which incorrectly changed a config
source-tier assertion from `default` to `env`; the final simulation leaves it unset,
matching the CI runner. No production change was made for these harness differences.

No application code or production theme behavior is changed in this follow-up. Remote CI
confirmation requires a subsequent commit/push.

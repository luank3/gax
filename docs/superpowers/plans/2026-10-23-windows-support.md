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

## Windows Terminal color and native scrollback regressions

Reported after `v0.2.0-rc.1`: native Windows Terminal shells lose all palette colors and
wheel/trackpad input does not scroll, although dragging the terminal scrollbar works.

Investigation confirmed a native shell environment with `WT_SESSION` but no `TERM` or
`COLORTERM`; the auto theme therefore selected `off`. The raw-mode mask also retained
`ENABLE_MOUSE_INPUT` while disabling Quick Edit. Microsoft's console host translates
that combination into mouse reporting (`SetConsoleInputModeImpl`,
https://github.com/microsoft/terminal/blob/main/src/host/getset.cpp), while gax only
handles keyboard records and discards mouse records.

- [x] Trace theme detection, shared Windows raw mode, and native console mouse reporting.
- [x] Add failing theme cases for Windows Terminal without TERM, NO_COLOR, TERM=dumb, explicit TERM, and light-background hints.
- [x] Add native regression checks that raw prompt and interrupt-watcher modes disable mouse reporting, preserve Ctrl+C policy, and restore original flags.
- [x] Recognize WT_SESSION when TERM is absent, without overriding explicit theme/TERM/NO_COLOR choices.
- [x] Disable mouse reporting in shared Windows raw mode rather than consuming or translating wheel events in gax.
- [x] Update the Windows design record and Unreleased changelog fixes.
- [x] Run native Windows tests/vet, `make lint test-race`, and Windows amd64/arm64 builds.

Verification record:
- Before the fixes, `TestAutoThemeRecognizesWindowsTerminal` failed the native/light cases
  with `off`; native ARM64 raw-mode checks failed with mouse reporting still enabled
  (`0x1b0` / `0x1b1`). Both regressions pass after the changes.
- Linux `make lint test-race` passes with inherited provider endpoint/credential/trace/
  transcript overrides removed, as in the earlier verification.
- Native Windows amd64 and ARM64 vet pass. The full ARM64 suite passes; the full amd64
  suite passes with `-p=1`. Both use `make test` with the deferred-cleanup Windows exec
  wrapper, `-count=1 -timeout=120s`, `WT_SESSION` set, and `TERM`, `COLORTERM`, `NO_COLOR`,
  and `GAX_THEME` unset. Focused ARM64 console/theme/editor/picker tests also pass,
  including isolated-console prompt, modal, interrupt and exact-restoration checks.
- The first parallel amd64 suite hit the pre-existing 250 ms wall-clock assertion in
  `TestBashKillsOrphanedChildrenWhenShellExits`. A repeat on an unchanged `git archive HEAD`
  export reproduced nine failures in twenty runs (250–311 ms), independently of these
  fixes. No unrelated process code or timing assertions were changed.
- Cgo-free Windows amd64/ARM64 Make builds pass and both executables run `--version`
  natively as `v0.2.0-rc.1-dirty`. Local artifacts are `bin/gax.exe` (amd64) and
  `bin/gax-windows-arm64.exe`; copies for manual Windows Terminal checks are in
  `C:\Users\Admin\AppData\Local\Temp\gax-terminal-fix-LfXG5e6p` as `gax-amd64.exe`
  and `gax-arm64.exe`. These are unpublished local builds, not replacement RC assets.
- Inline self-review found no additional issues in this patch; no subagents, commits,
  pushes, tags, or releases were created. The untracked codegraph documents remain untouched.

The user confirmed that colors and scrolling work in Windows Terminal with the patched
build. This is manual confirmation, separate from the automated console-mode checks;
individual mouse/trackpad devices were not separately enumerated. The published RC
tag/assets remain unchanged.

## Release follow-up: v0.2.0-rc.2

The user authorized committing/pushing these fixes and chose a new `v0.2.0-rc.2` tag
rather than replacing `v0.2.0-rc.1`. The first RC and its published assets stay immutable.

- Finalize the RC2 changelog with both Windows Terminal fixes and backfill the omitted RC1
  entry from its pinned commit range and published release metadata.
- Fresh pre-commit `make lint test-race` passes with the same isolated environment.
- Push the reviewed fix commit, require CI success, then push a signed annotated RC2 tag.
- Verify the release workflow, prerelease status, six platform archives, checksums, and
  packaged executable versions; keep `v0.1.0` as the latest stable release.

## Windows multiline paste regression

Reported after RC2: only the first line of a multiline Windows Terminal paste reaches the
prompt. gax enables bracketed paste on output but explicitly disables
`ENABLE_VIRTUAL_TERMINAL_INPUT`. ConPTY strips paste markers in that mode, leaving a bare
carriage return that the editor treats as submission. This behavior is documented in
https://github.com/microsoft/terminal/issues/18094 and the `Generic` CSI dispatch in
`src/terminal/parser/InputStateMachineEngine.cpp`.

- [x] Add failing native mode assertions for VT input in prompt and interrupt modes.
- [x] Cover native-console multiline paste, newline normalization, Unicode, continued editing,
  explicit submission, and mode restoration without reading/writing the host clipboard.
- [x] Enable VT input in shared raw mode, retaining disabled mouse input and Quick Edit.
- [x] Update the Windows spec/changelog and run `make lint test-race`, native Windows tests/vet,
  and cgo-free Windows amd64/ARM64 builds.
- [x] Provide an unpublished patched executable for manual Windows Terminal paste validation.

Verification:
- Before the fix, native ARM64 `TestRawRestoresConsoleAndReadsNativeKeys` failed for both
  raw modes (`0x1a0` / `0x1a1`, VT input disabled), and the three newline variants in
  `TestWindowsTTYMultilinePaste` failed its active-mode assertion. All pass after the fix.
- The paste cases cover CRLF, LF, CR, empty lines, trailing newlines, CJK and surrogate-pair
  emoji, existing draft text, editing after paste, final Enter, and exact input/output
  restoration. The native test injector now emits UTF-16 pairs rather than truncating
  supplementary characters. Existing modal/interrupt and native Unicode-key checks pass.
- Linux `make lint test-race` passes with inherited provider endpoint/credential/trace/
  transcript overrides removed. Full native Windows amd64 (`-p=1`) and ARM64 tests and
  vet pass with the same environment isolation and deferred-cleanup exec wrapper as RC2.
- Cgo-free Make builds pass for Windows amd64 and ARM64; both run `--version` natively as
  `v0.2.0-rc.2-dirty`. Manual-check copies are `gax-amd64.exe` and `gax-arm64.exe` in
  `C:\Users\Admin\AppData\Local\Temp\gax-paste-fix-YE352kgH`.
- Inline self-review found no additional patch issues. Tests use isolated consoles and
  injected key records, not the user's clipboard or a physical Windows Terminal paste.
  The user subsequently confirmed that the patched Windows Terminal build works for
  multiline paste; this manual confirmation is separate from the automated tests.

The user authorized committing and pushing this follow-up after manual validation.
Release tags/assets remain unchanged; a new release requires separate authorization.

## Windows word-deletion shortcuts

Native console key records carry modifier state separately from the character. The source previously
forwarded Backspace as a single byte and Delete as a CSI sequence, discarding Ctrl's word-editing
intent even though the shared editor already has previous/next-word kill actions.

- [x] Add failing native key-decoding cases for Ctrl+Backspace and Ctrl+Delete, plus assertions that
  unmodified Backspace/Delete retain their existing encodings.
- [x] Map Ctrl+Backspace to the shared backward word-kill action and Ctrl+Delete to the shared
  forward word-kill action; preserve AltGr and unmodified key behavior.
- [x] Verify the emitted backspace sequence decodes to the existing backward word-kill action.
- [x] Run native Windows tests/vet, Linux `make lint test-race`, and cgo-free Windows amd64/arm64
  builds.

Verification:
- Native Windows ARM64 `make vet test` passes across all packages using the isolated-console runner;
  Linux `make lint test-race` passes with provider/trace overrides unset.
- `make build` succeeds for Windows amd64 and ARM64 with `CGO_ENABLED=0`.
- Key tests assert native key-record encodings and shared-editor action decoding; no physical
  Windows Terminal interaction was exercised for this keyboard shortcut change.

## Windows Shift+Enter editing

The shared editor inserts a newline for LF, while Return's native Windows record normally carries CR.
The key decoder previously ignored the Shift modifier and sent CR through the submit path.

- [x] Add a native key-record regression case: Shift+Return with CR must produce the editor's LF
  input, while unmodified Return remains CR.
- [x] Add an isolated-console editor test proving Shift+Enter inserts a newline and does not submit;
  a later plain Enter submits the complete multiline draft.
- [x] Map shifted `VK_RETURN` to LF in the Windows source, preserving plain Return.
- [x] Run native Windows tests/vet, Linux `make lint test-race`, and cgo-free Windows amd64/arm64
  builds.

Verification:
- Before the fix, `TestKeyDecoding/shift-enter` failed with CR instead of the expected LF.
- The native Windows ARM64 full suite/vet, Linux `make lint test-race`, and both cgo-free Windows
  builds pass. The editor regression uses an isolated console and injected key records; no physical
  Windows Terminal interaction was done.

## Shared-terminal word-deletion sequences

Terminal decoders can expose Ctrl+Delete and Ctrl+Backspace as distinct escape sequences, even when
ordinary raw Backspace bytes are ambiguous.

- [x] Add failing shared-decoder tests for Ctrl+Delete (`CSI 3;5~`) and Ctrl+Backspace
  (`CSI 127;5u`, `CSI 27;5;127~`), while preserving plain Delete (`CSI 3~`).
- [x] Map only those distinct sequences to the existing forward/backward word-deletion actions; do
  not enable a terminal keyboard protocol or reinterpret raw Backspace bytes.
- [x] Update the terminal-input spec and Unreleased changelog to document the mappings and raw-byte
  limitation.
- [x] Run `make lint test-race` and cgo-free Linux/macOS builds.

Verification:
- New decoder cases failed before implementation (`CSI 3;5~` returned `ActionDeleteForward`; the two
  distinct Backspace sequences returned `ActionNone`), then passed after the exact-sequence mappings
  were added. The complete `internal/terminal/input` package passes.
- Baseline `make test` initially failed because `GAX_OPENAI_BASE_URL` was set in the shell; rerunning
  as `env -u GAX_OPENAI_BASE_URL make test` passes.
- The first full race run timed out in `TestRunTTYSlashProviderPickerChainsModelAndEffortSelection`
  and reported a cleanup race. That test passed in isolation, and a full retry of
  `env -u GAX_OPENAI_BASE_URL make lint test-race` passed.
- Cgo-free Linux/amd64 and macOS/arm64 `make build` commands pass.
- This follow-up uses a user-approved isolated worktree branch; no commit or push was created.

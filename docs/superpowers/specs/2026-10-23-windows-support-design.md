# Native Windows support

## Scope
Native Windows 10/11 amd64 and arm64 binaries, with Git for Windows Bash as the command shell. PowerShell/cmd command syntax is not a new tool contract. Preserve Linux/macOS behavior and keep standard-library-only, cgo-free builds.

## Design
- Isolate native filesystem operations behind platform files: no-follow opens, shared/exclusive cross-process locks, handle timestamps and link counts. Windows locks use an out-of-band byte range so normal session IO is not blocked. Pruning must retain active sessions; symlinks/reparse points must not be followed by session opens.
- Isolate command tree lifecycle. Windows uses a kill-on-close Job Object, with the command created suspended, assigned before execution, then resumed; cancellation attempts a console break before terminating the job. Close job handles after collection and on every start failure.
- Resolve Git Bash via configured shell, PATH and standard Git installation directories. Use Windows PATH/PATHEXT rules; keep existing POSIX resolution unchanged. Editor/file-picker subprocesses use the selected shell rather than /bin/sh.
- Make path utilities platform-specific without normalizing Unix dot-component semantics. Honor XDG overrides; use the Windows user profile when HOME is absent, retaining gax's documented relative config/state layout.
- Add a shared Windows console backend for raw modes, UTF-8 key input with bounded waits, terminal size and restoration. Enable ANSI/VT output for the entire app run, not just prompt input; unwind nested output/raw scopes in reverse order on normal return or immediate termination. Reuse the existing editor and picker state machines. Interrupt watching has an explicit stop/join lifecycle.
- Auto themes recognize `WT_SESSION` when native Windows Terminal shells omit `TERM`, while respecting `NO_COLOR`, explicit `TERM` values (including `dumb`), and manual theme choices. Raw keyboard input disables `ENABLE_MOUSE_INPUT` as well as Quick Edit so the console host retains native wheel/trackpad scrollback; both prompt and running-turn modes restore the exact original flags.
- Raw input enables `ENABLE_VIRTUAL_TERMINAL_INPUT`: without it, ConPTY strips bracketed-paste markers and pasted carriage returns submit the first line. Keep the native key-record reader, preserve Unicode/escape-sequence decoding, and let the shared editor normalize CRLF/CR/LF paste bodies without submitting until a separate Enter. Restore original input/output flags on return and modal handoff.
- Add native clipboard text/image helpers through built-in Windows PowerShell in STA mode, auth-store cross-process locks, tests, release ZIPs and Windows CI. Windows files inherit profile/directory ACLs; Unix permission bits are not a Windows access-control guarantee. Document Git Bash dependency and platform verification limitations.

## Verification
Run existing Make lint/race targets on Linux, cgo-free cross-builds for every shipped target, and Windows tests on a native runner. Where WSL interop is available, run Windows smoke tests directly. Console interaction requires Windows Terminal validation; never infer it from cross-compilation.

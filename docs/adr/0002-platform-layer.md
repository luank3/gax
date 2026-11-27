# ADR-0002: Platform Layer (OS Isolation, Windows Terminal & Process Control)

## Status
Accepted (Phase 1)

## Context
In current `gax`, `runtime.GOOS` and `syscall` appear in 40+ non-test call sites across `internal/app`, `internal/provider/codex`, `internal/session`, `internal/tools`, `internal/terminal/*`, and `internal/transport`. Hard Constraint 3 requires:
- Native support for `linux`, `darwin`, `windows` × `amd64`, `arm64` with `CGO_ENABLED=0`.
- All platform-specific code isolated in `internal/platform` behind build tags, with one interface per concern.
- Zero imports of `syscall` or checks of `runtime.GOOS` outside `internal/platform`.

## Options Considered
1. **Keep stdlib `syscall` + `syscall.NewLazyDLL` inside `internal/platform`.**
   - *Pros*: Zero third-party Go modules; already proven in `gax` v0.2.0 (`console_windows.go`, `bash_process_windows.go`, `clipboard_windows.go`).
   - *Cons*: Manual struct layouts and magic constants (`0x2000` for `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, `0x4` for `CREATE_SUSPENDED`, raw `ioctl` numbers on Unix).
2. **Use `golang.org/x/sys` (`unix` + `windows`) inside `internal/platform`.**
   - *Pros*: Typed, maintained constants and wrappers for Win32 Console (`GetConsoleMode`, `SetConsoleMode`, `GetConsoleScreenBufferInfo`), Job Objects (`CreateJobObject`, `SetInformationJobObject`, `AssignProcessToJobObject`, `TerminateJobObject`), `flock`, `termios`, and `TIOCGWINSZ`. Zero transitive dependencies.
   - *Cons*: Adds one third-party module to `go.mod` (requires ADR-0003 approval).

## Decision
Isolate all OS-specific code in `internal/platform` with one focused interface per concern:

1. **Terminal (`platform.Terminal`)**: raw mode (`ENABLE_VIRTUAL_TERMINAL_INPUT`, `ENABLE_VIRTUAL_TERMINAL_PROCESSING`, mouse reporting off for native scrollback), window size (`GetConsoleScreenBufferInfo` / `TIOCGWINSZ`), input flush, restore-all on exit/panic.
2. **Shell (`platform.Shell`)**: resolves `bash` (Git for Windows beside `git.exe`, `PATH`, standard dirs) with fallback to `pwsh`/`powershell` or `cmd` on Windows, and `bash`/`sh` on POSIX; formats shell command flags (`-c` vs `-NoProfile -NonInteractive -Command` vs `/d /s /c`).
3. **Process tree (`platform.ProcessGroup`)**:
   - Windows: Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, `CREATE_NEW_PROCESS_GROUP | CREATE_SUSPENDED`, assign before resume (`NtResumeProcess` via `LazyDLL("ntdll.dll")` where `x/sys/windows` lacks it), `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)` for graceful stop, `TerminateJobObject` for hard kill.
   - POSIX: `Setpgid: true` (+ `Pdeathsig: SIGKILL` on Linux), `kill(-pgid, SIGTERM)` -> grace period -> `kill(-pgid, SIGKILL)`.
4. **Clipboard (`platform.Clipboard`)**: Win32 `user32`/`kernel32` (`CF_UNICODETEXT`, `PNG`, `CF_DIB`/`CF_DIBV5`) on Windows; `pbpaste`/`pbcopy`/`osascript` on macOS; `wl-paste`/`wl-copy`/`xclip` on Linux.
5. **Dirs (`platform.Dirs`)**: XDG Base Directory resolution (`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`) with `HOME` / `%USERPROFILE%` fallback and symlink-canonical project bucket paths.
6. **File locking (`platform.FileLock`)**: `LockFileEx` on Windows; `flock` on POSIX (for `auth.json` and session writes).
7. **Keep-awake & Browser (`platform.KeepAwake`, `platform.OpenBrowser`)**: `caffeinate` (macOS), `systemd-inhibit` (Linux), `SetThreadExecutionState` (Windows).

Using `golang.org/x/sys` (pending ADR-0003 approval) for typed Win32/POSIX primitives, with `LazyDLL` only for `ntdll.NtResumeProcess` and `user32`/`gdi32` clipboard calls not wrapped by `x/sys/windows`.

## Consequences
- `internal/archtest/imports_test.go` fails the build if any package outside `internal/platform` imports `syscall`, `golang.org/x/sys`, or references `runtime.GOOS`.
- Windows terminal, process-tree, and clipboard behavior can be tested on Windows CI without touching core packages.

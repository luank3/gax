# ADR-0003: Dependency Candidate — `golang.org/x/sys`

## Status
Approved (User approved Phase 1 Gate)

## Context
`internal/platform` needs low-level OS primitives for terminal raw mode & size (`termios`, `TIOCGWINSZ`, Win32 `GetConsoleMode`/`SetConsoleMode`), process-tree termination (`kill(-pgid)`, `Pdeathsig`, Win32 Job Objects), and advisory file locking (`flock`, `LockFileEx`).

The user selected Option 2b in Phase 0 (`golang.org/x/sys` over raw `syscall` + `LazyDLL`). Per Hard Constraint 1, this ADR documents the measurements and details required before adding it to `go.mod`.

## What It Replaces & Why Stdlib Is Insufficient
- **Replaces**: Unmaintained `syscall` package (frozen in Go stdlib; lacks `IoctlGetWinsize`, `Flock` constants across BSD/macOS/Linux, and typed Win32 Job Object / Console APIs) and manual `unsafe` struct layouts + magic hex constants in `LazyDLL` wrappers.
- **Why stdlib is insufficient**: Go's `syscall` package is frozen; `syscall.SyscallN` + hand-typed Win32 structs (`jobExtendedLimits`, `screenInfo`) are brittle across `amd64`/`arm64` alignment and duplicate constants `golang.org/x/sys` already maintains and tests across all 6 target platforms.

## Measured Binary Size & Memory Delta
Measured in `scratch/phase1-bench/go-deps` (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, Go `go1.27.3`) comparing a minimal `net/http` + `encoding/json` binary (`base`) against `xsys` (`golang.org/x/sys v0.50.0` calling `unix.IoctlGetWinsize`/`Kill` on `!windows` and `windows.GetConsoleMode`/`CreateJobObject` on `windows`):

| Target | `base` (bytes) | `xsys` (bytes) | Delta (bytes) |
| --- | ---: | ---: | ---: |
| `linux/amd64` | 4,440,224 | 4,444,320 | +4,096 (+4.0 KiB) |
| `linux/arm64` | 4,128,928 | 4,128,928 | 0 (0 B) |
| `darwin/amd64` | 4,563,056 | 4,563,184 | +128 (+0.1 KiB) |
| `darwin/arm64` | 4,235,090 | 4,235,202 | +112 (+0.1 KiB) |
| `windows/amd64` | 4,591,104 | 4,647,424 | +56,320 (+55.0 KiB) |
| `windows/arm64` | 4,170,752 | 4,226,048 | +55,296 (+54.0 KiB) |

- **Memory delta (`linux/arm64`)**: `maxrss` 10,648 KiB -> 10,776 KiB (+128 KiB / +0.12 MiB).
- **CGO**: `CGO_ENABLED=0` compatible on all 6 targets.

## Metadata
- **Module**: `golang.org/x/sys` (`v0.50.0`)
- **License**: BSD-3-Clause
- **Maintenance health**: Official Go sub-repository, actively maintained by the Go team, updated monthly.
- **Transitive dependencies**: **None** (`go.mod` has only `require golang.org/x/sys v0.50.0`).

## Alternatives Considered
1. **In-repo `syscall` + `LazyDLL` wrappers** (used in `gax` v1): 0 B external module cost, but requires ~400 LOC of manual Win32 struct definitions, magic constants, and `unsafe.Pointer` calls, plus per-OS `ioctl` constants for `linux`/`darwin`.

## Decision
Approve `golang.org/x/sys v0.50.0` for use **exclusively inside `internal/platform`**, enforced by `internal/archtest/imports_test.go`. Pin in `go.mod` and record in `docs/dependencies.md` upon user approval.

## Consequences
- `platform` gains typed, cross-platform Win32/POSIX syscalls with +0–55 KiB binary impact and zero transitive dependencies.
- Non-platform packages remain 100% free of `syscall` and `golang.org/x/sys`.

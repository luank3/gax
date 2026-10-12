# Darwin CI Fixes Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the published repository's CI green on macOS, which it never was: `make vet` failed
there before any test ran, and once it compiled, five `internal/app` tests, one `internal/tools`
test, and one `internal/session` test failed on darwin only.

**Architecture:** No new packages. `internal/session/prune.go` loses its path-string exclusion in
favour of `os.FileInfo` identity; `internal/session/path.go` resolves symlinks before hashing a
working directory; two test helpers move or are corrected.

**Spec:** `golang/go#56259` — on macOS `TMPDIR` is `/var/folders/...` while `/var` is a symlink to
`/private/var`, so `t.TempDir()` returns a path that is not canonical whereas `os.Getwd()` and
`filepath.EvalSymlinks` return the resolved one. Every darwin failure below is that aliasing.

## Constraints

- Standard library only; no new dependencies.
- One project keeps one session bucket, whichever way its path is spelled.
- A resumed session stays out of the sweep when the sweep walks a root named relative to the
  working directory; the watch must not depend on how a path is spelled.
- Linux behaviour is unchanged: `EvalSymlinks` returns the same string for an already-resolved
  path, and the sweep's identity comparison matches what the string comparison matched.

## Tasks

### 1. macOS could not compile

- [x] Move `prepareTTYAppEnv` out of `internal/app/live_tty_linux_test.go` into a file without a
      build tag: `app_test.go` calls it, so `go vet` failed on darwin with `undefined:
      prepareTTYAppEnv`.

### 2. Resumed sessions were swept

- [x] Reproduce: CI failed `TestPruneBeforeSweepsAllProjectBucketsAndRespectsLocksAndExclusion` at
      `assertPruneTestExists(excludedPath)` on both runners.
- [x] Diagnose with an A/B diagnostic push: the sweep recorded `equal=false` for the protected file
      because `systempath.Join(projectRoot.Name(), name)` is relative to the `*os.Root` while the
      caller passes an absolute path.
- [x] Resolve the excluded path once with `Lstat` (`excludedFileInfo`) and compare `os.SameFile`
      before opening the file and again before the unlink.
- [x] Pin it with `TestPruneTreeKeepsExcludedSessionUnderRelativeRoot`, which sweeps through a root
      named relative to the cwd while the resumed path is absolute.

### 3. One project, two buckets

- [x] Resolve symlinks in `EncodeCWD` (via `canonicalCWD`) so `/var/...` and `/private/var/...`
      share a bucket; fall back to the given path when it does not exist.
- [x] Pin it with `TestEncodeCWDResolvesSymlinkedWorkingDirectory`.
- [x] Correct `TestSkillToolListsAndLoadsSkills`: the registry lists skills from the resolved
      working directory, so the expectation is built from `bashWorkingDirectory()`.

### 4. Verification

- [x] `make lint` and `make test-race` locally.
- [x] CI green on `ubuntu-latest` and `macos-latest` with the four cross-builds
      (`38240131727`).

## Record

- `TestPruneTreeKeepsExcludedSessionUnderRelativeRoot` has to close both sessions before sweeping.
  A session still held open keeps the writer's shared lock, and the sweep's `LOCK_EX|LOCK_NB` skips
  it, which hides the exclusion bug: the A/B run of that test passed until the lock was released.
- `EncodeCWD` is the only keying function; `Directory` and `PromptHistoryPath` both call it, so a
  single canonicalization covers session logs, prompt history, and the sessions root listing.
- The macOS test job had never reached the test suite: `make vet` stopped it. These darwin failures
  were latent from the start rather than a regression, and fixing `vet` is what exposed them.
- One flake is still open: `TestRunTTYREPLNotifiesWhenTurnCompletes` waits three seconds for the
  REPL to exit after Ctrl-D. It passed 25 of 25 runs in isolation but failed during a fully parallel
  `-race` run, where the failure path also closes the PTY while the reader goroutine is in
  `queryTerminalSize`. Both occurrences were under load, so it is unverified rather than fixed.

# System helpers spec

## Scope

- Inputs: filesystem paths, a configured shell name, argv slices with byte and time bounds, a
  working directory, and the process environment (`GIT_DIR`, `PATH`, `TMPDIR`, `LC_ALL`,
  `LC_CTYPE`).
- Outputs: bounded byte slices, resolved executable paths, a `(branch, commit, subject)` triple of
  `*string`, POSIX path strings, a tracked temp file, a keep-awake release function, a host
  description, and `locale.State`. Non-goal: terminal rendering, which the terminal specs own.

## Invariants

### File reads are capped and never block on special files

- **Rule:** `ReadFileCapped` returns at most `limit` bytes with truncation detected by a one-byte
  over-read, and `OpenRegular` refuses non-regular files before reading.
- **Source:** `internal/system/fs/fs.go:12` (`ReadFileCapped`), `internal/system/fs/fs.go:47`
  (`OpenRegular`; `O_NONBLOCK` open in `internal/system/fs/open_regular_unix.go:11`, plain open in
  `internal/system/fs/open_regular_other.go:11`).
- **Assertion:** `TestReadFileCappedReportsTruncation` in `internal/system/fs/fs_test.go:9`; the
  no-block half is `TestOpenRegularRejectsFIFOWithoutBlocking` in `internal/system/fs/fs_linux_test.go:12`.
- **Rejected:** `io.ReadAll` over an `os.Open` handle; an exactly-`limit` file would read as
  truncated, and a FIFO open would block before any size check ran.

---
### Git state is collected only inside a repository

- **Rule:** `State` spawns no Git process and returns three `nil`s unless the directory has a
  `.git` marker or `GIT_DIR` is set.
- **Source:** `internal/system/git/git.go:46` (`stateWithRunner`; environment read at `internal/system/git/git.go:41`).
- **Assertion:** `TestStateSkipsGitOutsideARepository` in `internal/system/git/git_test.go:64`.
- **Rejected:** Running the probes unconditionally; the test counts runner calls and requires
  zero, so a session outside a repository would pay two failing execs for no metadata.

---
### Captures return bounded output or nothing

- **Rule:** `CaptureStdoutContextInDir` returns nonempty stdout only for a zero exit before the
  deadline, and kills a child that closes stdout and keeps running.
- **Source:** `internal/system/spawn/spawn.go:108` (`CaptureStdoutContextInDir`; post-EOF wait at
  `internal/system/spawn/spawn.go:160`).
- **Assertion:** `TestCaptureStdoutBoundsRunningAndPostEOFHelpers` in
  `internal/system/spawn/spawn_test.go:90`.
- **Rejected:** Waiting on the process after the stdout reader hits EOF without a second deadline
  check; the test's EOF-then-hang helper would hold the probe for its full sleep.

---
### Shell lookup follows POSIX PATH rules

- **Rule:** `Find` resolves a name only to an executable regular file, treats a name containing
  `/` as a literal path, and skips empty or non-absolute `PATH` entries.
- **Source:** `internal/system/shell/shell.go:22` (`Find`).
- **Assertion:** `TestFindUsesOnlyAbsolutePATHEntries` in `internal/system/shell/shell_test.go:10`.
- **Rejected:** `exec.LookPath` or a bare `os.Stat`; the test puts a matching `fixture-shell` in a
  relative entry and requires no match, and a directory must not resolve either.

---
### Cleanup owns only tracked files and retries

- **Rule:** `Create` tracks each new file, `Untrack` removes it from that ownership, and `Cleanup`
  deletes tracked files but leaves a nonempty directory for a later call.
- **Source:** `internal/system/tempfiles/tempfiles.go:80` (`Cleanup`; `Untrack` at
  `internal/system/tempfiles/tempfiles.go:65`).
- **Assertion:** `TestUntrackLeavesFileAndCleanupRetriesNonemptyDirectory` in
  `internal/system/tempfiles/tempfiles_test.go:64`.
- **Rejected:** `os.RemoveAll` on the private directory; the file the test deliberately untracks
  would be destroyed by the first cleanup.

---
### Paths are joined literally, without normalization

- **Rule:** `Join` trims boundary slashes only, keeps inner components such as
  `missing/../target` intact, and yields `/foo` for an empty base.
- **Source:** `internal/system/path/path.go:11` (`Join`).
- **Assertion:** `TestJoinMatchesUpstreamPathRules` in `internal/system/path/path_test.go:9`.
- **Rejected:** `filepath.Join`; it collapses `missing/../target`, returns `foo` rather than `/foo`
  for an empty base, and drops the trailing slash the table expects for `Join("/tmp", "")`.

---
### Keep-awake never gates the turn

- **Rule:** `Run` calls the turn even when inhibition is disabled, `acquire` is nil, or the acquire
  function returns a nil release, and defers only a non-nil release.
- **Source:** `internal/system/keepawake/keepawake.go:9` (`Run`).
- **Assertion:** `TestRunStillRunsWhenInhibitorCannotStart` in
  `internal/system/keepawake/keepawake_test.go:30`.
- **Rejected:** Returning early when no inhibitor is available; a host without `systemd-inhibit`
  or `caffeinate` would never run the turn.

---
### Release names come from os-release fields with faithful quoting

- **Rule:** `ReleaseName` prefers `PRETTY_NAME`, otherwise joins `NAME` and `VERSION` with one
  space (empty when `NAME` is absent), and unescapes backslashes only inside double quotes.
- **Source:** `internal/system/osinfo/osinfo.go:18` (`ReleaseName`; decoder in
  `decodeReleaseValue` at `internal/system/osinfo/osinfo.go:58`).
- **Assertion:** `TestReleaseNameReadsPrettyNameAndFallsBackToNameAndVersion` in
  `internal/system/osinfo/osinfo_test.go:12`.
- **Rejected:** Stripping quotes and calling `strconv.Unquote`; the single-quoted row must return
  the literal `Test \"Linux\"`, so backslashes inside single quotes survive.

---
### UTF-8 detection probes the effective locale first

- **Rule:** `Detect` probes the supplied environment first and, when that charmap is not exactly
  `UTF-8`, retries the candidates with `LC_ALL` cleared and `LC_CTYPE` set.
- **Source:** `internal/system/locale/locale.go:40` (`Detect`; `isUTF8Charmap` at `internal/system/locale/locale.go:195`).
- **Assertion:** `TestDetectUsesEffectiveLocaleAndCandidateFallback` in
  `internal/system/locale/locale_test.go:19`.
- **Rejected:** Leaving `LC_ALL` in the candidate environment; that subprocess honors it, so a
  pinned `LC_ALL=C` would look UTF-8-free even though the process can select `C.UTF-8`.

## Gaps

- The `limit < 0` branch returning `negative file size limit` (`internal/system/fs/fs.go:14`) has no
  asserting test; the table in `internal/system/fs/fs_test.go:9` uses positive limits only.
- `State`'s `os.LookupEnv("GIT_DIR")` read (`internal/system/git/git.go:41`) is not exercised;
  `internal/system/git/git_test.go:64` injects the flag through `stateWithRunner` instead.
- `Start`'s platform commands, including `--what=idle --mode=block --who=gax --why=gax is running a
  turn` (`internal/system/keepawake/keepawake_linux.go:24`), are not tested; the keepawake tests
  stub the acquire function and never call `Start`.
- The cgo path (`nativeProcessLocale`, `internal/system/locale/locale_native_cgo.go:25`) is tested
  only under `//go:build cgo`, by `TestNativeProcessLocaleMatchesLocaleUtility`
  (`internal/system/locale/locale_native_cgo_test.go:10`).

## Review

- `FindWorktreeRoot` (`internal/system/git/git.go:21`) accepts a `.git` entry of any type via
  `Lstat` and walks to the root unbounded; `internal/system/git/git_test.go:47` fails if a depth cap appears.
- `CaptureStdoutContextInDir` kills the child in the second deadline branch
  (`internal/system/spawn/spawn.go:167`); confirm both test rows still fail fast.
- Behaviors asserted but not ruled above: `ReleaseNameWithFallback`
  (`internal/system/osinfo/osinfo.go:49`) and `releaseFileCap` (`internal/system/osinfo/osinfo.go:14`), plus `OpenURL`/
  `openerCommand` (`internal/system/browser/browser.go:12`), pinned by `internal/system/osinfo/osinfo_test.go:37`,
  `internal/system/osinfo/osinfo_test.go:65`, `internal/system/browser/browser_test.go:10`, and `internal/system/browser/browser_test.go:56`.
- `ChildEnvironment` (`internal/system/locale/locale.go:114`) re-pins five categories when it
  clears `LC_ALL`; the `ChildCType == ""` early return is what `internal/system/locale/locale_test.go:118` pins.

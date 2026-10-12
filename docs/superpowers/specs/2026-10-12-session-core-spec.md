# Session core spec

## Scope

`internal/session` owns the on-disk session format: the append-only JSONL log behind `--resume`, the
`/fork` writer, retention listing and pruning, the picker label reader, and the prompt history path.
Inputs are a cwd, a `Selection`, `[]conversation.Item` plus a loaded count, retention days, and a
cutoff; outputs are paths, JSONL records, `Meta`/`Loaded`, `[]Entry`, and `PickerLabel`. Non-goal:
in-memory conversation state and compaction, which `internal/agent` owns.

## Invariants

### Session directories are keyed by cwd

- **Rule:** `Directory(cwd)` is `<XDG_STATE_HOME>/gax/sessions/<EncodeCWD(cwd)>`; `EncodeCWD` trims
  leading slashes, turns `/` into `-`, truncates to 79 bytes, and appends `.` plus a path hash.
- **Source:** `internal/session/path.go:57`, `internal/session/path.go:37`
- **Assertion:** `TestDirectoryPreservesXDGPathFormatting` in `internal/session/path_test.go:27`
- **Rejected:** path-cleaning the state root; the test pins `/tmp/state//gax/sessions/...`.

---

### Prompt history sits beside the log it belongs to

- **Rule:** `PromptHistoryPath(cwd)` is the `history` file in that cwd's session directory, and an
  empty cwd fails with `cannot resolve prompt history without a working directory`.
- **Source:** `internal/session/path.go:62`
- **Assertion:** `TestPromptHistoryPathSharesCWDSessionDirectory` in
  `internal/session/path_test.go:8`
- **Rejected:** resolving history from the state root, sharing one file across projects.

---

### Nothing reaches disk until the first turn

- **Rule:** `Begin` and `Append` call `materialize` to create the file, and `ResumeHint` returns nil
  while `headerWritten` is false, so an idle log leaves no `*.jsonl` behind.
- **Source:** `internal/session/log.go:196`, `internal/session/log.go:175`
- **Assertion:** `TestRunEmptyREPLEOFDoesNotCreateResumableSession` in
  `internal/app/app_test.go:1377`
- **Rejected:** creating the file in `NewLog`; the test requires zero `*.jsonl` after an empty run.

---

### Resume opens an existing file and never creates one

- **Rule:** `ResumeLog` opens the path with `O_NOFOLLOW` and without `O_CREAT`, so a pruned or
  symlinked path fails instead of being recreated, and it never rewrites the header.
- **Source:** `internal/session/log.go:119`, `internal/session/log.go:394`
- **Assertion:** `TestResumeLogDoesNotRecreatePrunedSession` in `internal/session/prune_test.go:187`
- **Rejected:** opening with `O_CREAT`; a pruner can unlink the file between listing and resume.

---

### Fork branches a prefix under a fresh identity

- **Rule:** `ForkFile` writes a sibling file with a new id, sets `forked_from` to the source id,
  re-stamps the five selection keys, and marks every copied item `Inherited`.
- **Source:** `internal/session/fork.go:17`, `internal/session/fork.go:91`
- **Assertion:** `TestForkFileCopiesOnlyInheritedPrefixWithNewSelection` in
  `internal/session/fork_test.go:10`
- **Rejected:** inheriting unselected fields; the source's `effort: "hi"` must not reach the branch.

---

### Prune spares locked, recent, and excluded sessions

- **Rule:** `PruneBefore` returns nil for a zero cutoff; otherwise it unlinks expired session files
  in every project bucket, sparing files a writer holds `LOCK_EX` and the `excludePath`.
- **Source:** `internal/session/prune.go:59`, `internal/session/prune.go:277`
- **Assertion:** `TestPruneBeforeSweepsAllProjectBucketsAndRespectsLocksAndExclusion` in
  `internal/session/prune_test.go:10`
- **Rejected:** pruning only the caller's bucket; the sweep must reach a second project too.

---

### Pruning is limited to standard-named regular files

- **Rule:** `pruneSessionFile` skips any name `PathIsStandard` rejects and any entry that is not a
  regular file, never descends through a symlinked bucket, and removes a bucket only when empty.
- **Source:** `internal/session/prune.go:277`, `internal/session/prune.go:228`
- **Assertion:** `TestPruneBeforeLeavesUnrelatedFilesAndSymlinksAlone` in
  `internal/session/prune_test.go:52`
- **Rejected:** filtering on the `.jsonl` suffix alone; `backup_...jsonl` survives without an id.

---

### The sweep is throttled by the `.prune` marker

- **Rule:** `StartPrune` runs one sweep per process, and skips the sweep while `.prune` is non-empty
  and younger than `pruneInterval` (24h); only a completed sweep rewrites it with `1`.
- **Source:** `internal/session/prune.go:68`, `internal/session/prune.go:15`
- **Assertion:** `TestStartPruneMarksCompletedSweepAndAppliesDailyThrottle` in
  `internal/session/prune_test.go:154`
- **Rejected:** stamping the marker before the sweep; an aborted run would suppress retries a day.

---

### Picker rows come from the log's opening records

- **Rule:** `ReadPickerLabel` takes provider, model, effort, and preset from the opening `session`
  and `selection` records, preferring `model_label`; takes `git_branch` and `git_subject` from the
  `session` record only; and stops scanning at the first user record.
- **Source:** `internal/session/label.go:28`, `internal/session/label.go:77`
- **Assertion:** `TestReadPickerLabelUsesFirstPromptAndOpeningSelection` in
  `internal/session/label_test.go:9`
- **Rejected:** letting the newest selection win; the test plants `later-provider` after the prompt.

---

## Gaps

- Rule 6's zero-cutoff clause is unasserted: only `List` is pinned by
  `TestListHidesExpiredSessionsUntilRetentionIsDisabled`; `PruneBefore`'s early return is untested.
- Rule 7 leans on `PathIsStandard`'s offsets (63-byte name, `_` at 20, `.jsonl` at 57), which lack
  a direct test; pinned indirectly by `TestPruneBeforeLeavesUnrelatedFilesAndSymlinksAlone`.
- Rule 8's `pruneProcess.done` guard and `ShutdownPrune`'s join are unasserted; only the marker
  throttle is tested.
- `Log.Undo`'s unwritten-header return and `Log.UserTurn`'s negative `elapsedMS` guard have no
  in-package test; the nearest is
  `TestRunREPLUndoRemovesLiveTailAndRetiresItInSessionLog` in `internal/app/app_test.go:2948`.
- `docs/sessions.md:76` says `/undo` truncates the log at an item boundary; `Log.Undo` only appends
  an `undo` record, so `LoadAll`'s `applyUndo` retires the surviving lines at load time.

## Review

- `internal/session/path.go:41` seeds the hash with `1469598103934665603`, matching hax
  `src/session.c:340` rather than the canonical FNV-1a basis; changing it would relocate every
  bucket, and nothing pins the seed.
- `internal/session/log.go:418` checks `locked && !hasLinks(info)` only after a successful
  `syscall.Flock`, so a failed lock skips the link-count check; the symlink half is pinned by
  `TestSessionReadersAndWritersRejectSessionSymlinks`, and `Touch` (`internal/session/prune.go:33`)
  is pinned only by `TestTouchProtectsResumedSessionFromPrune`.
- `internal/session/load.go:242` and `:259` run on every load untested, as does `ReadMeta` (`:37`);
  `internal/session/list.go:44` skips the cutoff for entries whose `idFromPath` is nil.
- `ReadPickerLabel`'s other branches sit outside rule 9: the scan cap and non-blocking open by
  `TestReadPickerLabelRejectsFIFOWithoutBlocking`, the `origin` skip and `(compacted)` fallback by
  `TestReadPickerLabelSkipsTaskNotesAndMarksCompactedHistory`, and truncation by
  `TestReadPickerLabelFlattensAndTruncatesByDisplayCells`.

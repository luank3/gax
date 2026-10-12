# Catalog cache and refresh lifecycle

## Goal

Match the catalog lifecycle behavior: load a bounded provider-shaped cache, refresh it once in the background when stale, keep the last good snapshot on failure, expose stale age once, and support bounded wait, drain, and shutdown.

## Tasks

- [x] Add lifecycle tests for cache loading, fresh-cache short circuit, async refresh, refresh failure, staleness reporting, and wait cancellation.
- [x] Add synchronized snapshot replacement and bounded cache loading.
- [x] Add XDG cache path resolution and atomic cache writes.
- [x] Add one-shot asynchronous refresh and lifecycle controls.
- [x] Run catalog tests, then full Go tests, build, and vet.

## Progress

`Catalog` now loads capped cache files, starts a single refresh when the configured interval has elapsed, validates responses before atomically replacing the cache and in-memory snapshot, and exposes stale-age, wait, drain, and shutdown controls. Focused lifecycle tests pass with the race detector.

Verification passed: `go test -race -count=1 ./...`, `go test -count=1 ./...`, `go build ./...`, and `go vet ./...`.

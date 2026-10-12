# Standalone Identity Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the repository describe and build gax as its own Go agent rather than the tracked
port of a specific hax commit: no baseline commit, no version value borrowed from upstream, and a
cgo-free build everywhere except the race detector.

**Architecture:** No new packages. `internal/version` keeps a `dev` default that release and
`make` builds overwrite with `-ldflags`; the Makefile pins `CGO_ENABLED=0` for the static targets;
the README, AGENTS.md, CONTRIBUTING.md, changelog, and specs index stop claiming a baseline.

**Spec:** the port had been anchored to hax `ff45833` in two places that were load-bearing rather
than descriptive — `internal/version.Value` (reported by `--version` and recorded in every session
header) and the instructions telling contributors and agents to match that checkout. Both made gax
report someone else's commit as its identity, and neither describes what gax ships today.

## Constraints

- Standard library only; no new dependencies.
- `make build`, `make install`, `make vet`, `make test`, and `make cover` run with
  `CGO_ENABLED=0`. `make test-race` keeps `CGO_ENABLED=1`, which `-race` requires — a blanket
  export breaks the race target.
- Release builds keep injecting the tag through `-ldflags -X .../version.Value=<version>`.
- The existing session-header contract (`gax_version`) is unchanged apart from the value it carries.

## Tasks

### 1. A cgo-free build

- [x] Pin `CGO_ENABLED=0` in the Makefile for build, install, vet, test, and cover; leave the race
      target on cgo and expose `CGO=1` as the opt-out for a local cgo build.
- [x] Verify the built binary records `CGO_ENABLED=0`.
- [x] Have CI build and run `./bin/gax --version` on both runners, so a change that silently needs
      cgo fails before cross-compilation does.

### 2. Drop the baseline pin

- [x] `internal/version.Value` defaults to `dev`; its comment describes injection instead of the
      upstream checkout, and its test asserts the default.
- [x] `TestNewHeaderRecordCarriesTheInjectedVersion` pins that a session header records the value
      the binary was built with.
- [x] Remove `ff45833` from the README, changelog, AGENTS.md, the specs index, and the session log
      example.
- [x] Rewrite the README, AGENTS.md, and CONTRIBUTING.md framing: gax was ported from hax and has
      grown past it, with the web tools, bash output compression, and search backends named as its
      own.

### 3. Verification

- [x] `make lint`, `make test`, and `make test-race` locally.
- [x] `gax dev` from a plain `go build`, `gax v0.1.0-dirty` from `make build`.

## Record

- The race detector needs cgo, so "cgo-free by contract" cannot mean a repository-wide export; the
  Makefile scopes it per target instead (`CGO_ENABLED=1` for `test-race` only).
- The dated plans and per-subsystem specs keep their `ff45833` citations: they are the record of
  what was read at the time, and the same provenance rule that keeps the MIT notice in `LICENSE`
  applies to them. What the repository no longer does is point its identity, its version, or its
  contribution rules at that checkout.
- `internal/system/locale` keeps its `//go:build cgo` path. Nothing in a default build compiles it,
  and the race-enabled test run still exercises it.

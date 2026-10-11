# Contributing

Thanks for taking the time to improve gax. This project is a Go port of
[hax](https://github.com/OleksandrChekhovskyi/hax), so the most useful contributions are the ones that make
gax behave the way hax does — or fix a bug in that behavior.

## Prerequisites

- Go 1.25 or newer.
- A Unix-like OS for development (Linux or macOS). Windows works through WSL.
- Optional: a hax checkout to consult when porting behavior.

## Workflow

1. Read the relevant plan in [`docs/superpowers/plans`](docs/superpowers/plans); each subsystem
   has one, with goals, constraints, and acceptance criteria.
2. Make the change with tests in the same commit.
3. Run the checks below.
4. Open a pull request that explains the behavior change and cites the upstream behavior it
   follows (commit hash and file when applicable).

```sh
make lint        # gofmt check + go vet
make test-race   # race-enabled test suite
```

Run the suite through the Make targets: they unset `GAX_PROVIDER`, `GAX_MODEL`, `GAX_EFFORT`,
and `GAX_PRESET`, which otherwise leak into one-shot output asserted by `internal/app` tests.

## Guidelines

- **Standard library only.** gax has no third-party module dependencies, and pull requests that
  add one will not be merged.
- **Keep it minimal.** Prefer the smallest correct change that fits the surrounding code and
  the upstream behavior.
- **Port, don't redesign.** Match upstream naming, diagnostics, and edge-case handling; a
  divergence should be justified in the PR description.
- **Never log credentials.** Provider keys and header values must stay out of diagnostics,
  sessions, and traces; register secrets with `internal/wiretrace` when they travel the wire.
- **Keep the plans current.** Tick off tasks or add a dated plan file when you land new
  behavior; a change without a plan entry is considered incomplete.
- **Commit style.** Imperative subject lines, optionally scoped (`provider: ...`, `app: ...`).
  Do not commit build output (`bin/`, `dist/`).

## Reporting issues

Include the gax version (`gax --version`), your OS, the provider and model in use, and the
smallest reproduction you can manage. For provider-specific problems, a wire trace
(`GAX_TRACE=/tmp/gax-trace.md`) helps enormously — review it before attaching it, since it may
contain your prompts even though credentials are redacted.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).

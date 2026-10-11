# Agent usage accounting

## Goal

Port per-attempt usage footers from upstream `agent_usage.c` and `agent_core.c`, including exact provider charges, catalog estimates, category splits, elapsed time, and stable display provenance.

## Tasks

- [x] Test reported and unknown usage, catalog cost splits, exact-cost precedence, and provenance.
- [x] Expose resolved model metadata and add session usage-footer construction.
- [x] Run focused, repository-wide, race, build, and vet checks.

## Source

hax `src/agent_usage.c`, `agent_core.c`, and `provider.h` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

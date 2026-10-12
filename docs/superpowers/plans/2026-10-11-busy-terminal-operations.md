# Busy terminal operation parity

Port the busy spinner to blocking model, catalog, usage, and Codex login operations. Interactive TTY runs show the same delayed animated status, accept double-Escape cancellation, restore terminal input, erase the status row, and leave an interruption marker. Non-TTY output remains unchanged while request contexts still honor cancellation.

## Verification

- [x] Add renderer coverage for caller-provided status labels.
- [x] Add PTY tests proving status display, double-Escape transport cancellation, pending-input resolution, row cleanup, and the interruption marker.
- [x] Thread busy ticks through model listing, catalog waits, usage requests, and Codex login waits and requests.
- [x] Run the full Go suite, race tests, vet, and Linux/macOS builds.

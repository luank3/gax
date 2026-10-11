# Built-in file tools

## Goal

Port the `read`, `write`, and `edit` model tools with provider schemas, bounded output, image attachments, atomic writes, and exact-string edit safeguards. Make the registry usable by the agent loop.

## Tasks

- [x] Add behavior tests for schemas, slices, binary/image handling, writes, edits, and tool-result history.
- [x] Implement the file-tool registry and connect it to `RunLoop`.
- [x] Run focused and repository-wide tests, race checks, build, and vet.
- [x] Relativize in-working-directory paths for read, write, and edit execution.
- [x] Port remaining tool display hooks and rendering.
- [x] Port the windowed, divide-and-conquer Myers diff with local comparison-budget fallback and repeated-run sliding.

## Source

hax `src/tools/read.c`, `write.c`, `edit.c`, `image_sniff.c`, `output_cap.c`, `path_preprocess.c`, `system/fs.c`, and `text/diff.c` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

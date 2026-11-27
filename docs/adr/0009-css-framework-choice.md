# ADR-0009: CSS Framework Choice (Measured Preact Prototype)

## Status
Accepted (Phase 1)

## Context
We need a CSS framework for the Preact web UI in `gax serve` that fits the `60 KiB` total gzipped embedded-asset budget, supports design tokens and `prefers-color-scheme`, and ships zero unused CSS.

## Measured Prototype
We built the exact same Preact view (responsive project sidebar + streaming conversation log + prompt form) in `scratch/phase1-bench/web-proto` using Vite `6.4.6` + Preact `10.25.0`:

| Candidate | CSS Raw | CSS Gzip | JS Raw | JS Gzip | Total Gzip (incl. HTML) |
| --- | ---: | ---: | ---: | ---: | ---: |
| **Tailwind CSS v4** (`tailwindcss` + `@tailwindcss/vite`) | 8,433 B | **2,680 B** | 16,003 B | 6,685 B | **9,605 B (9.4 KiB)** |
| **UnoCSS** (`unocss` + `@unocss/preset-wind3`) | 5,363 B | **1,388 B** | 16,003 B | 6,685 B | **8,323 B (8.1 KiB)** |

## Options Considered
1. **Tailwind CSS v4 (`tailwindcss` + `@tailwindcss/vite`)**:
   - *Pros*: Native CSS-first `@theme` block maps directly to CSS custom properties (`--color-*`, `--font-*`); automatic content purging (2.68 KiB gzipped CSS); industry-standard documentation and Vite integration.
   - *Cons*: 1.29 KiB larger CSS than UnoCSS in the minimal prototype.
2. **UnoCSS (`@unocss/preset-wind3`)**:
   - *Pros*: 1.39 KiB gzipped CSS (smallest).
   - *Cons*: Virtual CSS module + preset configuration; less standard CSS `@theme` integration.
3. **Hand-authored CSS**:
   - *Rejected*: Prompt specifies "Preact with a fitting CSS framework (decide by measurement in an ADR)".

## Decision
Choose **Tailwind CSS v4** (`tailwindcss` + `@tailwindcss/vite`). At `2.68 KiB` gzipped CSS (`9.4 KiB` total gzipped for the prototype), it is 6× under the `60 KiB` budget while giving clean `@theme` design-token support and automatic dead-CSS elimination.

## Consequences
- `web/package.json` includes only `preact` (runtime) and `vite`, `@preact/preset-vite`, `tailwindcss`, `@tailwindcss/vite` (dev/build-time only).
- CI enforces the `<= 60 KiB` total gzipped asset budget on `internal/serve/ui/dist/`.

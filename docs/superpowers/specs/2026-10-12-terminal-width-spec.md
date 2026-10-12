# terminal width spec

## Scope

The cell arithmetic the terminal UI shares: `Cells`/`RuneCells`, `WrapRows`, `ReflowForDisplay`,
`AutoDisplayWidth`/`DisplayWidth`/`ContentColumns`, `picker.Core`, `LabelCells`, `Resolve`, and
`theme`. Inputs are text, a width, a mode, escape bytes, and a query; outputs are cell counts,
wrapped strings, match indexes, SGR-styled rows. Non-goal: repainting and termios setup.

## Invariants

### Count cells and wrap within the indent budget

- **Rule:** `RuneCells` returns 1 for a control rune (below `0x20`, `0x7f`, or `unicode.IsControl`),
  0 for an `Mn`/`Me` mark or U+200D, 2 inside its wide ranges, and 1 otherwise; `WrapRows` wraps on
  a budget of `columns - indent` floored at 1 and prefixes later rows with `indent` spaces.
- **Source:** `internal/terminal/width/cells.go:20` (`RuneCells`) and `:40` (`WrapRows`).
- **Assertion:** `TestWrapRowsUsesCellsAndKeepsContinuationIndent` in
  `internal/terminal/width/display_test.go:42`; `:43` and `:47` pin the cell and wrap cases.
- **Rejected:** Counting a combining mark as one cell; `TestResolveTerminalRows` in
  `internal/terminal/vtresolve/resolve_test.go:8` erases `xe\u0301` as one cluster at `:46`.

---

### Resolve the display width, then clamp it to the terminal

- **Rule:** A configured width of at least 20 is returned unchanged; otherwise a non-positive
  terminal width becomes 100, `terminal` mode keeps the width, any other mode caps above 110 at
  100, both floor it at 20, and `ContentColumns` clamps the result to the terminal.
- **Source:** `internal/terminal/width/display.go:26` (`DisplayWidth`), clamp in `:43`.
- **Assertion:** `TestDisplayWidthModesAndTerminalClamp` in
  `internal/terminal/width/display_test.go:5` and `:32` (`TestAutoDisplayWidth`).
- **Rejected:** Running a configured width through `AutoDisplayWidth`, which would cut the 120 in
  `internal/terminal/width/display_test.go:20` to 100.

---

### Reserve the suffix on every reflow row and fit the ellipsis inside the budget

- **Rule:** `ReflowForDisplay` reserves `suffixReserve` cells on every row, breaks at a space or a
  `|`, `/`, `,`, `;`, `&`, `=` mark only when the dropped slack is at most `maxCells/2` capped at
  16, and fits `...` inside the last permitted row's budget.
- **Source:** `internal/terminal/width/reflow.go:12` (`ReflowForDisplay`), break rule in `:94`.
- **Assertion:** `TestReflowForDisplayMatchesUpstreamFixtures` in
  `internal/terminal/width/reflow_test.go:8`; `:19` pins the reserve and `:20` the `...` row.
- **Rejected:** Appending `...` past the cell budget; `internal/terminal/width/reflow.go:50` keeps
  3 cells free and `internal/terminal/width/reflow_test.go:16` requires 7 glyphs plus `...`.

---

### Filter by every query term, then clamp the selection

- **Rule:** `Match` requires each non-empty space-separated query term to appear in the label as an
  ASCII case-insensitive substring, and a query change resets `selection` and `firstVisible` to 0
  while `Move`, `Page`, and `SelectLast` keep `selection` within `[0, len(matches)-1]`.
- **Source:** `internal/terminal/picker/core.go:60` (`Match`), `:301` (`updateMatches`).
- **Assertion:** `TestMatchRequiresEverySpaceSeparatedCaseInsensitiveTerm` in
  `internal/terminal/picker/core_test.go:5`, `TestCoreQueryNavigationAndViewportFollowMatches`
  at `:24`, and `TestCoreNavigationClampsAndCentersSelection` at `:48`.
- **Rejected:** Matching the trimmed query as one string: `OPUS claude` never appears in
  `anthropic/claude-opus-4`, which `internal/terminal/picker/core_test.go:11` requires it to match.

---

### Size a label from the width, the current tag, and the detail

- **Rule:** `LabelCells` charges `MarkerCells` (2) off `terminalColumns` and subtracts
  `CurrentTagCells` (11) for a `Current` item, sizing the label to `available - separator - detail`
  raised to `available/2` and capped at the label's natural width.
- **Source:** `internal/terminal/picker/core.go:113` (`LabelCells`), constants in `:12`.
- **Assertion:** `TestPickerCellLayoutAccountsForDetailsAndCurrentTag` in
  `internal/terminal/picker/core_test.go:75`.
- **Rejected:** Giving a labelled item all `available` cells when a detail exists; the case at
  `internal/terminal/picker/core_test.go:85` requires 20 cells for a 26-cell label.

---

### Replay cursor controls per row and bound the cursor

- **Rule:** `Resolve` interprets LF, CR, CSI cursor-left and cursor-right, CSI erase-line, and SGR
  one row at a time, keeps every other complete escape as a zero-width control, writes a trailing
  newline for a nonempty final row, and clamps cursor-right at `maxCursorColumn` (4096).
- **Source:** `internal/terminal/vtresolve/resolve.go:60` (`Resolve`), clamp in `:98`.
- **Assertion:** `TestResolveTerminalRows` in `internal/terminal/vtresolve/resolve_test.go:8`,
  and `TestResolveClampsAbsurdCursorForward` at `:62`.
- **Rejected:** Dropping an unmodeled CSI; `internal/terminal/vtresolve/resolve_test.go:24`
  requires `a\x1b[2Ab` verbatim.

---

### Fall back to a detected theme and warn on unknown names

- **Rule:** `New` lowercases both arguments, resolves an empty, `auto`, or unknown name through
  `detect`, falls back to `teal` for an empty or unknown tint, and warns
  `unknown theme '<value>' (expected auto, dark, light, ansi, or off)` or
  `unknown tint '<value>' (expected teal, violet, rose, or sage)`.
- **Source:** `internal/terminal/theme/theme.go:88` (`New`), order in `:207` (`detect`).
- **Assertion:** `TestAutoThemeAndInvalidValues` in `internal/terminal/theme/theme_test.go:88`.
- **Rejected:** Falling back silently; `internal/terminal/theme/theme_test.go:103` requires two
  warnings, so a fixed `dark` fallback fails despite `TERM=xterm-256color` selecting `dark`.

---

### Retint only the model roles, and keep emphasis when color is off

- **Rule:** `applyTint` rewrites the opens of `Stance`, `CodeInline`, `CodeBlock`, `Heading`, and
  `Link` for `dark` and `light` only, `off` drops every color while keeping `bold`, `dim`, and
  `underline`, and `TintOpen` returns nothing for other palettes or an unknown tint.
- **Source:** `internal/terminal/theme/theme.go:170` (`applyTint`), `off` table at `:156`.
- **Assertion:** `TestDarkThemeTintsOnlyModelRolesAndPreserveCloseSequences` in
  `internal/terminal/theme/theme_test.go:35`, `TestOffThemeKeepsNonColorAttributes` at `:59` and
  `TestTintPreviewUsesTheActiveThemePalette` at `:72`.
- **Rejected:** Tinting a fixed role such as `Accent`; `internal/terminal/theme/theme_test.go:41`
  requires its open to equal the teal one, and `:83` requires an empty `ansi` preview.

## Gaps

- `internal/terminal/width/cells.go` has no test file; `Cells` is asserted once, at
  `internal/terminal/width/display_test.go:43`, and no test reaches `RuneCells`' control-rune arm.
- `WrapRows` on empty text returning one empty row, and the clamps at
  `internal/terminal/width/cells.go:44` and `:48`, have no asserting test.
- `TextCells` (`internal/terminal/picker/core.go:105`) has no asserting test: no case in
  `internal/terminal/picker/core_test.go` passes a label containing `\r` or `\n`.
- `theme.Configure` (`internal/terminal/theme/theme.go:242`) and its `lastBadTheme`/`lastBadTint`
  repeat filter have no asserting test; both theme tests call `New` only.
- The `columns <= 0` guard at `internal/terminal/width/display.go:48` is unreachable through
  `DisplayWidth`, which floors at 20 or falls back to 100, and no test enters it.

## Review

- The 20-cell floor and the physical clamp disagree by design: `DisplayWidth(19, "auto", 0)` is 20
  while `ContentColumns(19, "auto", 0)` is 19 (`internal/terminal/width/display_test.go:13`).
- `reflowBreakPosition` treats `;` as a break mark with no fixture at all, and its mark arm keeps
  the mark (`internal/terminal/width/reflow.go:133`) while its space arm eats a space at `:131`.
- `internal/terminal/width/display_safe.go` consumes `Cells` but is outside this spec's read set;
  its tests are `internal/terminal/width/display_safe_test.go:8` and `:21`.
- `commitRow` emits the carried and reset sequences only for a row with a visible glyph
  (`internal/terminal/vtresolve/resolve.go:345`), so a control-only row stays bare;
  `internal/terminal/vtresolve/resolve_test.go:35` pins that.

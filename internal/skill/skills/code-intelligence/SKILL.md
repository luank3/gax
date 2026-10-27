---
name: code-intelligence
description: Explore, navigate, and search codebases using structural search (ast-grep), tags/symbols (ctags), targeted regex, and git history without whole-file reads.
---

# Code Intelligence & Navigation

Read this skill when navigating unfamiliar codebases, finding definitions, callers, interfaces,
or searching syntax structures rather than guessing text patterns.

## Core Rules

1. **Avoid whole-file dumps into context.** When exploring, reading entire large files consumes
   precious context and tokens. Use targeted symbol searches, line ranges (`read` tool with `offset`/`limit`),
   or file outlines first.
2. **Prefer AST/structural patterns over naive substring grep.** Substring search returns string literals,
   comments, and test fixtures. Use AST-aware patterns (`ast-grep`) or anchored symbol regexes whenever possible.
3. **Trace call hierarchies and history before altering shared abstractions.** Include test files
   when searching callers to understand verification contracts and breakages.

## Technique Precedence & Ordering

1. **AST-Aware Search** (`ast-grep`): Structural AST matching when `ast-grep` is installed.
2. **Targeted Regex** (`rg -n`): Fast symbol and outline search with line numbers when `ast-grep` is unavailable.
3. **Tags Files** (`tags`): Pre-indexed symbol lookups across large source trees.
4. **Git History & Provenance**: Evolution and co-change analysis.

---

## Techniques

### 1. File Outlines & Slicing

Quickly inspect a file's structure with line numbers without loading its entire contents:

* **Go outline**: `rg -n --type go '^func\s+' <file>`
* **Python outline**: `rg -n --type py '^\s*(class|def)\s+' <file>`
* **Rust outline**: `rg -n --type rust '^\s*(pub(\([^)]+\))?\s+)?(async\s+|const\s+|unsafe\s+|extern\s+)?(fn|struct|enum|trait|impl)\b' <file>`
* **TypeScript / JavaScript outline**: `rg -n --type-add 'ts:*.{ts,tsx,js,jsx,mts,cts}' -t ts '^\s*(export\s+)?(default\s+)?(async\s+)?(function|class|interface|type)\s+' <file>`
* **Slice specific lines**: use `read` with `offset` and `limit`, or `sed -n '<start>,<end>p' <file>`.

### 2. AST-Aware Search with `ast-grep`

> [!NOTE]
> On Linux, `sg` may execute `/usr/bin/sg` (shadow-utils group tool). Always run `ast-grep` directly, or verify `sg --version` first. Note that shell environment variables do not persist across separate `bash` calls.

Check if `ast-grep` is installed:
```bash
command -v ast-grep || (command -v sg && sg --version 2>&1 | grep -q "ast-grep")
```

When available, use structural pattern matching or tree-sitter node kinds:
* Match all Go function and method declarations (regardless of return signature):
  ```bash
  ast-grep scan --inline-rules 'id: go-funcs
  language: go
  rule:
    any:
      - kind: function_declaration
      - kind: method_declaration'
  ```
* Find React hook invocations or component trees:
  ```bash
  ast-grep --pattern 'useEffect($$$)' --lang tsx
  ```
* Search method calls matching a syntax pattern:
  ```bash
  ast-grep --pattern '$OBJ.Close()' --lang go
  ```

### 3. Targeted Symbol & Definition Lookup with `rg`

Always pass `-n` so line numbers are output even when piped:

* **Go**:
  * Functions/Methods: `rg -n --type go '^func\s+(\([^)]+\)\s+)?<Name>\b'`
  * Types (standalone or grouped): `rg -n --type go '^\s*(type\s+<Name>\b|<Name>\s+(struct|interface)\b)'`
* **Rust**:
  * Named definitions (`pub`, `pub(crate)`, `async fn`, `const fn`, `unsafe fn`, `extern "C" fn`):
    `rg -n --type rust '^\s*(pub(\([^)]+\))?\s+)?(async\s+|const\s+|unsafe\s+|extern(\s+"[^"]+")?\s+)*(fn|struct|enum|trait|type)\s+<Name>\b'`
  * `impl` blocks for `<Name>`:
    `rg -n --type rust '^\s*(unsafe\s+)?impl(<[^>]+>)?\s+([A-Za-z_][A-Za-z0-9_:]*(\s*<[^>]+>)?\s+for\s+)?<Name>\b'`
* **TypeScript / JavaScript**:
  * Definitions (`export`, `async`, `class`, `interface`):
    `rg -n --type-add 'ts:*.{ts,tsx,js,jsx,mts,cts}' -t ts '^\s*(export\s+)?(default\s+)?(async\s+)?(function|class|interface|type|const|let|var)\s+<Name>\b'`
* **Python**:
  * Classes/Functions: `rg -n --type py '^\s*(async\s+def|def|class)\s+<Name>\b'`
* **C / C++**:
  * Function definitions and class methods (`int *foo(`, `Class::method`, brace on same or next line):
    `rg -n -U '^[ \t]*([A-Za-z_][A-Za-z0-9_:<>]*[\s*&]+)+([A-Za-z_][A-Za-z0-9_:<>]*::)?<Name>\s*\([^;{}]*\)(\s*(const|noexcept|override|final))*\s*\{'`

### 4. Reference Finding & Hotspots

To find where `<Symbol>` is called or referenced across the codebase:
* Search all callers (always include test files to see how functions are exercised and tested):
  ```bash
  rg -n -w '<Symbol>'
  ```
* Identify high-traffic usage hotspots sorted by frequency:
  ```bash
  rg -c -w '<Symbol>' | sort -t: -k2 -nr | head -20
  ```

### 5. Ctags & Tags Files

If a `tags` file exists in the repository root:
* Lookup a tag using tab-delimited matching (avoid regex `\t` portability bugs):
  ```bash
  awk -F'\t' '$1 == "<Name>" { print }' tags
  ```
* Note: `TAGS` files are Emacs etags format; standard ctags files are named `tags`. Avoid re-indexing entire trees (`ctags -R .`) in large repositories unless instructed.

### 6. Architectural Provenance with Git

When commit history is needed to understand *why* code was structured a certain way:
* **Trace commit introducing or removing a symbol (pickaxe)**:
  ```bash
  git log -S "<Symbol>" --oneline
  ```
* **Trace line history or function evolution** (restrict count to avoid context explosion):
  ```bash
  git log -L :<funcName>:<path/to/file> -n 5
  ```
* **Find files frequently modified together with `<path/to/file>`**:
  ```bash
  git log --format="%H" -n 30 -- "<path/to/file>" | while read -r rev; do
    git show --pretty="" --name-only "$rev"
  done | grep -v -x -F "<path/to/file>" | grep -v '^$' | sort | uniq -c | sort -nr | head -10
  ```

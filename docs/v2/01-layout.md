# gax v2 — Repository Layout Tree

```text
gax/
├── cmd/
│   └── gax/
│       └── main.go                 # Entrypoint: CLI parse -> tui | oneshot | jsonstream | serve
├── internal/
│   ├── core/                       # Layer 0: Message, Item, Usage, ToolDef, Event, Outcome
│   ├── platform/                   # Layer 1: ONLY pkg allowed syscall / x/sys / runtime.GOOS
│   │   ├── platform.go             # Consumer interfaces: Terminal, Shell, ProcessGroup, Clipboard, Dirs, FileLock, KeepAwake
│   │   ├── *_unix.go               # POSIX termios, TIOCGWINSZ, setpgid/kill, flock, XDG
│   │   ├── *_linux.go              # Pdeathsig, wl-paste/xclip, systemd-inhibit
│   │   ├── *_darwin.go             # pbcopy/pbpaste/osascript, caffeinate
│   │   └── *_windows.go            # Win32 console VT, Job Objects, Win32 clipboard, LockFileEx, PowerShell/CMD/Git Bash
│   ├── config/                     # Layer 2: Settings registry, layered resolution, presets, state.json
│   ├── session/                    # Layer 2: Append-only hax v1 JSONL store, lazy reader, history, pruning
│   ├── catalog/                    # Layer 2: models.dev cache & per-model overrides
│   ├── skill/                      # Layer 2: Skill discovery (.agents/skills, ~/.config/gax/skills) + embedded skills
│   ├── transport/                  # Layer 2: Streaming HTTP/SSE client, retry, CA roots
│   ├── wiretrace/                  # Layer 2: Redacted HTTP/SSE trace writer
│   ├── bashcompress/               # Layer 2: Command output compression filters
│   ├── web/                        # Layer 2: web_fetch HTML-to-text + 8-backend web_search
│   ├── provider/                   # Layer 3: Provider interface + wires + shipped providers
│   │   ├── openai/                 # Responses API + Chat Completions (flat/nested reasoning, reasoning_required)
│   │   ├── anthropic/              # Messages API (adaptive/budget thinking, prompt caching)
│   │   ├── codex/                  # ChatGPT OAuth (browser + device) + token refresh + usage
│   │   ├── openrouter/             # Attribution headers, route provenance, /usage
│   │   ├── opencode/               # opencode-zen + opencode-go (shared OPENCODE_API_KEY, /usage)
│   │   ├── deepseek/               # Reasoning replay + /usage balance
│   │   ├── llamacpp/               # Auto-discovery, router mode, port 9931 default
│   │   ├── mock/                   # Scripted & heuristic mock provider
│   │   └── registry/               # Shipped provider table + custom providers.<id> resolver
│   ├── tools/                      # Layer 3: read, edit, write, bash, task_wait, web_fetch, web_search, skill, mcp_*
│   ├── mcp/                        # Layer 3: Lazy MCP manager (stdio + streamable-HTTP), disk index cache, crash recovery
│   ├── agent/                      # Layer 4: Turn loop, context window, compaction + overflow recovery, Event emitter
│   ├── tui/                        # Layer 5: Interactive REPL, live region + spinner/footer, markdown, editor, pickers, OSC 7501/9
│   ├── oneshot/                    # Layer 5: -p runner, piped stdin + prompt, clean stdout, stderr banner/stats, signals
│   ├── jsonstream/                 # Layer 5: --json JSONL live stream + closing result record
│   ├── serve/                      # Layer 5: HTTP + SSE + POST remote-control server, project registry, auth, embedded UI
│   │   ├── ui/
│   │   │   ├── embed_webui.go      # //go:build webui -> //go:embed dist/*
│   │   │   ├── embed_stub.go       # //go:build !webui -> helpful message when built without make web
│   │   │   └── dist/               # Gitignored Vite output (built by make web)
│   └── archtest/                   # Cross-cutting AST import-direction & forbidden-import tests
├── web/                            # Preact + Tailwind CSS v4 + Vite frontend (build-time only)
│   ├── package.json
│   ├── package-lock.json
│   ├── vite.config.js
│   └── src/
│       ├── tokens.css              # Design tokens + @import "tailwindcss" + @theme
│       ├── main.jsx
│       └── api.js                  # Auth, fetch/POST, EventSource SSE client
├── docs/
│   ├── adr/                        # ADRs 0001–0011
│   ├── v2/                         # 00-audit.md, 01-layout.md, decisions-needed.md, plan.md
│   └── dependencies.md             # Approved third-party modules & versions (populated upon approval)
├── go.mod                          # Zero require lines until ADRs approved
├── Makefile                        # web, build (-tags webui), test, test-race, vet, fmt-check, cross, budget
└── .goreleaser.yaml                # Runs make web before building 6 static targets
```

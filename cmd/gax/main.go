// Command gax is the CLI entrypoint for the gax coding agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/jsonstream"
	"github.com/luank3/gax/internal/mcp"
	"github.com/luank3/gax/internal/oneshot"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/serve"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/subagent"
	"github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/tui"
	"github.com/luank3/gax/internal/wiretrace"
)

var version = "dev"

type cliOptions struct {
	help       bool
	version    bool
	printMode  bool
	jsonMode   bool
	cont       bool
	resumeSet  bool
	resumeID   string
	noSession  bool
	raw        bool
	bare       bool
	provider   string
	model      string
	effort     string
	preset     string
	promptArgs []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, platform.IsTerminal(os.Stdin), os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) int {
	dirs := platform.ResolveDirs()
	store, err := config.Load(dirs.Config, dirs.State)
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}

	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], store, dirs.State, dirs.Cache, stderr)
	}

	opts, err := parseArgs(args, store)
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	if opts.help {
		printHelp(stdout)
		return 0
	}
	if opts.version {
		fmt.Fprintf(stdout, "gax %s\n", version)
		return 0
	}
	if err := subagent.CheckDepth(os.Getenv("GAX_SUBAGENT_DEPTH")); err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}

	cwd, _ := os.Getwd()
	if opts.preset != "" {
		store.SetOverride("preset", opts.preset)
		if p, ok := store.Presets()[opts.preset]; ok {
			if p.Provider != "" {
				store.SetOverride("provider", p.Provider)
			}
			if p.Model != "" {
				store.SetOverride("model", p.Model)
			}
			if p.Effort != "" {
				store.SetOverride("effort", p.Effort)
			}
		}
	}
	if opts.provider != "" {
		store.SetOverride("provider", opts.provider)
	}
	if opts.model != "" {
		store.SetOverride("model", opts.model)
	}
	if opts.effort != "" {
		store.SetOverride("effort", opts.effort)
	}
	if opts.noSession {
		store.SetOverride("no_session", "on")
	}

	var loaded *session.Loaded
	var sessPath string
	if opts.cont || (opts.resumeSet && opts.resumeID != "") {
		if opts.cont {
			sessPath, err = session.Latest(dirs.State, cwd)
		} else {
			sessPath, err = session.FindByID(dirs.State, cwd, opts.resumeID)
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return 1
		}
		if sessPath != "" {
			loaded, err = session.Load(sessPath)
			if err != nil {
				fmt.Fprintf(stderr, "gax: %v\n", err)
				return 1
			}
			if loaded.Selection.Provider != "" {
				store.SetSession("provider", loaded.Selection.Provider)
			}
			if loaded.Selection.Model != "" {
				store.SetSession("model", loaded.Selection.Model)
			}
			if loaded.Selection.Effort != "" {
				store.SetSession("effort", loaded.Selection.Effort)
			}
		}
	}

	provID := store.String("provider")
	if provID == "" || provID == "auto" {
		provID = registry.AutoSelect(store)
		if provID == "" {
			fmt.Fprintln(stderr, "gax: no provider available (set an API key, run /login, or pass --provider)")
			return 1
		}
	}
	modelID := store.String("model")
	effort := store.String("effort")
	if provID == "codex" && (modelID == "" || effort == "") {
		dm, de := codex.ReadCodexDefaults(dirs.Home)
		if modelID == "" {
			modelID = dm
		}
		if effort == "" {
			effort = de
		}
	}
	var discoveredCtx int64
	if provID == "llamacpp" {
		dm, dc := registry.DiscoverLlamaCpp(context.Background(), store)
		if modelID == "" {
			modelID = dm
		}
		discoveredCtx = dc
	}
	ctxLimit := store.Int("context_limit")
	if ctxLimit <= 0 && discoveredCtx > 0 {
		ctxLimit = discoveredCtx
	}

	cat := catalog.Load(dirs.Cache)
	cat.SetOverrides(store.RawSection("catalog"))
	if cid := registry.CatalogIDFor(provID, store); cid != "" && cat.NeedsRefresh(store.Duration("catalog.refresh")) {
		if u := store.String("catalog.url"); u != "" {
			go func() {
				_ = cat.Refresh(context.Background(), u, func(c context.Context, target string) ([]byte, error) {
					req, err := http.NewRequestWithContext(c, http.MethodGet, target, nil)
					if err != nil {
						return nil, err
					}
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						return nil, err
					}
					defer resp.Body.Close()
					return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
				})
			}()
		}
	}
	streamOpts := registry.ResolveStreamOptions(provID, modelID, store, cat)

	tracer, _ := wiretrace.Open(store.String("trace"))
	if tracer != nil {
		defer tracer.Close()
	}
	httpClient := transport.NewClient(transport.RetryPolicy{
		MaxRetries: int(store.Int("http.max_retries")),
		BaseDelay:  store.Duration("http.retry_base"),
	}, tracer)

	prov, err := registry.Resolve(provID, modelID, cwd, store, httpClient)
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}

	prompt, err := oneshot.BuildPrompt(opts.promptArgs, stdin, stdinTTY)
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	mcpMgr := mcp.NewManager(store.MCPServers(), dirs.Cache)
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}

	if !opts.printMode && !opts.jsonMode {
		var toolReg *tools.Registry
		if !opts.raw {
			toolReg = tools.NewRegistry(cwd, store)
			toolReg.SetMCP(mcpMgr)
			defer toolReg.Close()
		}
		sess := agent.NewSession(core.SessionHeader{
			Version:    1,
			GaxVersion: version,
			ID:         session.NewUUID(),
			CWD:        cwd,
			Provider:   provID,
			Model:      modelID,
			Effort:     effort,
			Preset:     opts.preset,
		})
		if loaded != nil {
			sess.Header = loaded.Header
			sess.Items = append(sess.Items, loaded.LiveItems...)
		}
		recordTUI := !store.Bool("no_session") && !(store.String("no_session") == "auto" && provID == "mock")
		var tuiWriter *session.Writer
		if recordTUI {
			if loaded != nil {
				tuiWriter, _ = session.OpenAppend(loaded.Path)
			} else {
				tuiWriter, _ = session.Create(dirs.State, cwd, sess.Header)
			}
			if tuiWriter != nil {
				defer tuiWriter.Close()
				sess.SetOnAppend(func(it core.Item) { _ = tuiWriter.AppendItem(it) })
			}
		}
		return tui.Run(context.Background(), tui.Options{
			CWD:           cwd,
			StateDir:      dirs.State,
			Version:       version,
			Store:         store,
			Catalog:       cat,
			HTTPClient:    httpClient,
			Session:       sess,
			Writer:        tuiWriter,
			Provider:      prov,
			Tools:         toolReg,
			Model:         modelID,
			Effort:        effort,
			Preset:        opts.preset,
			ContextLimit:  ctxLimit,
			StreamOptions: streamOpts,
			Bare:          opts.bare,
			Raw:           opts.raw,
			Stdin:         stdin,
			StdinFile:     os.Stdin,
			Stdout:        stdout,
			StdoutFile:    os.Stdout,
			Stderr:        stderr,
		})
	}
	if prompt == "" && loaded == nil {
		fmt.Fprintln(stderr, "gax: -p requires a prompt or --resume")
		return 1
	}

	recordSession := !store.Bool("no_session") && !(store.String("no_session") == "auto" && provID == "mock")
	var writer *session.Writer
	var sess *agent.Session
	var hdr core.SessionHeader

	if loaded != nil {
		hdr = loaded.Header
		sess = agent.NewSession(hdr)
		sess.Items = append(sess.Items, loaded.LiveItems...)
		if recordSession {
			writer, err = session.OpenAppend(loaded.Path)
			if err != nil {
				fmt.Fprintf(stderr, "gax: %v\n", err)
				return 1
			}
		}
	} else {
		hdr = core.SessionHeader{
			Version:    1,
			GaxVersion: version,
			ID:         session.NewUUID(),
			CWD:        cwd,
			Provider:   provID,
			Model:      modelID,
			Effort:     effort,
			Preset:     opts.preset,
		}
		sess = agent.NewSession(hdr)
		if recordSession {
			writer, err = session.Create(dirs.State, cwd, hdr)
			if err != nil {
				fmt.Fprintf(stderr, "gax: %v\n", err)
				return 1
			}
			hdr.ID = writer.Path() // keep UUID from header
			hdr.ID = sess.Header.ID
		}
	}
	if writer != nil {
		defer writer.Close()
		go session.Prune(dirs.State, int(store.Int("session_retention_days")), writer.Path())
	}

	sess.SetOnAppend(func(it core.Item) {
		if writer != nil {
			_ = writer.AppendItem(it)
		}
		if opts.jsonMode {
			b, merr := json.Marshal(it)
			if merr == nil {
				_, _ = fmt.Fprintln(stdout, string(b))
			}
		}
	})

	if prompt != "" {
		if loaded != nil && len(sess.Items) > 0 {
			sess.Append(core.Item{Kind: core.KindTurnBoundary})
		}
		sess.Append(core.Item{Kind: core.KindUser, Text: prompt})
	}

	var toolReg *tools.Registry
	if !opts.raw {
		toolReg = tools.NewRegistry(cwd, store)
		toolReg.SetMCP(mcpMgr)
		defer toolReg.Close()
	}

	shellName := ""
	if sp, err := platform.ResolveShell(store.String("bash.shell")); err == nil {
		shellName = sp.Name
	}
	sysPrompt := agent.BuildSystemPrompt(agent.PromptOptions{
		CWD:       cwd,
		Model:     modelID,
		ShellName: shellName,
		Store:     store,
		MCPIndex:  mcpMgr.PromptIndex(),
		Raw:       opts.raw,
		Bare:      opts.bare,
	})

	ctx := context.Background()
	if opts.jsonMode {
		return jsonstream.Run(ctx, jsonstream.Options{
			Header:           hdr,
			Session:          sess,
			Provider:         prov,
			Tools:            toolReg,
			SystemPrompt:     sysPrompt,
			Model:            modelID,
			Effort:           effort,
			MaxTurns:         int(store.Int("max_turns")),
			ContextLimit:     ctxLimit,
			CompactAuto:      store.Bool("compact.auto"),
			CompactThreshold: store.Int("compact.threshold"),
			StreamOptions:    streamOpts,
			Stdout:           stdout,
			Stderr:           stderr,
		})
	}
	return oneshot.Run(ctx, oneshot.Options{
		Session:          sess,
		SessionID:        hdr.ID,
		Provider:         prov,
		Tools:            toolReg,
		SystemPrompt:     sysPrompt,
		Model:            modelID,
		Effort:           effort,
		MaxTurns:         int(store.Int("max_turns")),
		ContextLimit:     ctxLimit,
		CompactAuto:      store.Bool("compact.auto"),
		CompactThreshold: store.Int("compact.threshold"),
		StreamOptions:    streamOpts,
		Stdout:           stdout,
		Stderr:           stderr,
	})
}

func parseArgs(args []string, store *config.Store) (cliOptions, error) {
	var opts cliOptions
	presets := store.Presets()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if _, ok := presets[args[0]]; ok {
			opts.preset = args[0]
			args = args[1:]
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			opts.promptArgs = append(opts.promptArgs, args[i+1:]...)
			break
		}
		switch {
		case a == "-h" || a == "--help":
			opts.help = true
		case a == "-v" || a == "--version":
			opts.version = true
		case a == "-p" || a == "--print":
			opts.printMode = true
		case a == "--json":
			opts.jsonMode = true
			opts.printMode = true
		case a == "-c" || a == "--continue":
			opts.cont = true
		case a == "--resume":
			opts.resumeSet = true
		case strings.HasPrefix(a, "--resume="):
			opts.resumeSet = true
			opts.resumeID = strings.TrimPrefix(a, "--resume=")
			if opts.resumeID == "" {
				return opts, fmt.Errorf("--resume= requires a session id")
			}
		case a == "--no-session":
			opts.noSession = true
		case a == "--raw":
			opts.raw = true
		case a == "--bare":
			opts.bare = true
		case strings.HasPrefix(a, "--provider="):
			opts.provider = strings.TrimPrefix(a, "--provider=")
		case a == "--provider" && i+1 < len(args):
			i++
			opts.provider = args[i]
		case strings.HasPrefix(a, "--model="):
			opts.model = strings.TrimPrefix(a, "--model=")
		case a == "--model" && i+1 < len(args):
			i++
			opts.model = args[i]
		case strings.HasPrefix(a, "--effort="):
			opts.effort = strings.TrimPrefix(a, "--effort=")
		case a == "--effort" && i+1 < len(args):
			i++
			opts.effort = args[i]
		case strings.HasPrefix(a, "--preset="):
			opts.preset = strings.TrimPrefix(a, "--preset=")
		case a == "--preset" && i+1 < len(args):
			i++
			opts.preset = args[i]
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("unknown option %q", a)
		default:
			opts.promptArgs = append(opts.promptArgs, a)
		}
	}
	return opts, nil
}

func runServe(args []string, store *config.Store, stateDir, cacheDir string, stderr io.Writer) int {
	listen := serve.DefaultListen
	allowRemote := false
	token := os.Getenv("GAX_SERVE_TOKEN")
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--listen="):
			listen = strings.TrimPrefix(a, "--listen=")
		case a == "--allow-remote":
			allowRemote = true
		case a == "-h" || a == "--help":
			fmt.Fprintln(stderr, "Usage: gax serve [--listen=127.0.0.1:8484] [--allow-remote]")
			return 0
		}
	}
	if allowRemote {
		fmt.Fprintln(stderr, "WARNING: --allow-remote exposes remote code execution; keep the bearer token secret.")
	}
	httpClient := transport.NewClient(transport.RetryPolicy{
		MaxRetries: int(store.Int("http.max_retries")),
		BaseDelay:  store.Duration("http.retry_base"),
	}, nil)
	mcpMgr := mcp.NewManager(store.MCPServers(), cacheDir)
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}
	srv, err := serve.NewServer(serve.Options{
		Listen:      listen,
		AllowRemote: allowRemote,
		Token:       token,
		StateDir:    stateDir,
		Version:     version,
		Store:       store,
		HTTPClient:  httpClient,
		MCP:         mcpMgr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gax serve: %v\n", err)
		return 1
	}
	defer srv.Close()
	fmt.Fprintf(stderr, "gax serve listening on http://%s\n", listen)
	if token == "" {
		fmt.Fprintf(stderr, "bearer token: %s\n", srv.Token())
	}
	if err := http.ListenAndServe(listen, srv.Handler()); err != nil {
		fmt.Fprintf(stderr, "gax serve: %v\n", err)
		return 1
	}
	return 0
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "gax - minimalist terminal-native coding agent")
	fmt.Fprintln(w, "Usage: gax [preset] [options] [prompt...]")
	fmt.Fprintln(w, "  -p, --print          Run one-shot and print final response to stdout")
	fmt.Fprintln(w, "      --json           One-shot JSONL stream on stdout")
	fmt.Fprintln(w, "  -c, --continue       Resume latest session for current directory")
	fmt.Fprintln(w, "      --resume[=ID]    Resume session by id or unique prefix")
	fmt.Fprintln(w, "      --no-session     Do not record session or history")
	fmt.Fprintln(w, "      --raw            Prompt only (no system prompt or tools)")
	fmt.Fprintln(w, "      --bare           Omit AGENTS.md, skills, and subagent guidance")
	fmt.Fprintln(w, "      --provider=NAME  Select provider")
	fmt.Fprintln(w, "      --model=ID       Select model")
	fmt.Fprintln(w, "      --effort=LEVEL   Select reasoning effort")
	fmt.Fprintln(w, "      --preset=NAME    Apply named preset")
	fmt.Fprintln(w, "  -h, --help           Show help")
	fmt.Fprintln(w, "  -v, --version        Show version")
}

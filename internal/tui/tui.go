// Package tui implements the interactive terminal REPL frontend for gax.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transcript"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/tui/editor"
	"github.com/luank3/gax/internal/tui/live"
	"github.com/luank3/gax/internal/tui/markdown"
	"github.com/luank3/gax/internal/tui/notify"
	"github.com/luank3/gax/internal/tui/picker"
	"github.com/luank3/gax/internal/tui/slash"
	"github.com/luank3/gax/internal/tui/theme"
	"github.com/luank3/gax/internal/tui/width"
)

// Options configures an interactive TUI session.
type Options struct {
	CWD           string
	StateDir      string
	Version       string
	Store         *config.Store
	Catalog       *catalog.Store
	HTTPClient    *transport.Client
	Session       *agent.Session
	Writer        *session.Writer
	Provider      provider.Client
	Tools         *tools.Registry
	Model         string
	Effort        string
	Preset        string
	ContextLimit  int64
	StreamOptions provider.StreamOptions
	Bare          bool
	Raw           bool
	Stdin         io.Reader
	StdinFile     *os.File
	Stdout        io.Writer
	StdoutFile    *os.File
	Stderr        io.Writer
}

// Run runs the interactive terminal REPL until EOF, Ctrl-C twice, or Ctrl-D.
func Run(ctx context.Context, opts Options) int {
	if opts.Store == nil {
		opts.Store = config.NewStore()
	}
	isTTY := opts.StdoutFile != nil && platform.IsTerminal(opts.StdoutFile)
	pal := theme.Resolve(opts.Store.String("theme"), opts.Store.String("tint"), isTTY)
	var colsAtom atomic.Int64
	colsAtom.Store(int64(resolveCols(opts.Store, opts.StdoutFile)))
	if isTTY {
		stopResize := platform.WatchResize(func() {
			colsAtom.Store(int64(resolveCols(opts.Store, opts.StdoutFile)))
		})
		defer stopResize()
	}
	shellName := ""
	if sp, err := platform.ResolveShell(opts.Store.String("bash.shell")); err == nil {
		shellName = sp.Name
	}

	fmt.Fprintf(opts.Stdout, "%sgax %s%s · %s", pal.Bold, opts.Version, pal.Reset, opts.Provider.ID())
	if opts.Model != "" {
		fmt.Fprintf(opts.Stdout, " · %s", opts.Model)
	}
	if opts.Effort != "" {
		fmt.Fprintf(opts.Stdout, " · %s", opts.Effort)
	}
	fmt.Fprintf(opts.Stdout, "\n%sType /help for commands, Ctrl-D to quit.%s\n\n", pal.Dim, pal.Reset)

	hist := session.LoadHistory(opts.StateDir, opts.CWD)
	ed := editor.NewBuffer(hist)
	var focused atomic.Bool
	focused.Store(true)
	if isTTY {
		_, _ = io.WriteString(opts.Stdout, "\x1b[?1004h")
		defer func() { _, _ = io.WriteString(opts.Stdout, "\x1b[?1004l") }()
	}
	paused := false
	inReader := bufio.NewReader(opts.Stdin)

	argChoices := func(cmd string) []string {
		switch cmd {
		case "/provider":
			avail := registry.AvailableProviders(opts.Store)
			out := make([]string, 0, len(avail))
			for _, d := range avail {
				out = append(out, d.ID)
			}
			return out
		case "/model":
			var out []string
			if opts.Model != "" {
				out = append(out, opts.Model)
			}
			if opts.Catalog != nil {
				if cid := registry.CatalogIDFor(opts.Provider.ID(), opts.Store); cid != "" {
					out = append(out, opts.Catalog.ListModels(cid)...)
				}
			}
			return out
		case "/effort":
			return []string{"low", "medium", "high", "xhigh", "max"}
		case "/preset", "/new", "/clear":
			raw := opts.Store.RawSection("presets")
			out := make([]string, 0, len(raw))
			for k := range raw {
				out = append(out, k)
			}
			return out
		case "/config":
			out := make([]string, 0, len(config.Registry))
			for _, d := range config.Registry {
				out = append(out, d.Key)
			}
			return out
		case "/tasks":
			return []string{"kill", "all"}
		case "/login", "/logout":
			return []string{"codex"}
		default:
			return nil
		}
	}

	for {
		promptLabel := fmt.Sprintf("%s> %s", pal.User, pal.Reset)
		if paused {
			promptLabel = fmt.Sprintf("%s[paused — Enter to continue]> %s", pal.Dim, pal.Reset)
		}
		cols := int(colsAtom.Load())
		line, eof, action := readPromptLine(ctx, opts.CWD, opts.StateDir, inReader, opts.StdinFile, opts.Stdout, opts.StdoutFile, isTTY, cols, promptLabel, ed, opts.Store, pal, &focused, argChoices)
		if eof {
			if isTTY {
				_, _ = io.WriteString(opts.Stdout, "\r\x1b[2K")
			}
			notify.EmitProgramStatus(opts.Stdout, opts.Store.String("program_status"), notify.StateClear, isTTY)
			return 0
		}
		switch action {
		case actionPagerHistory:
			openPager(renderHistoryText(opts.Session.Items, cols, pal, opts.Store.Bool("markdown")))
			continue
		case actionPagerTranscript:
			sys := agent.BuildSystemPrompt(agent.PromptOptions{
				CWD: opts.CWD, Model: opts.Model, ShellName: shellName, Store: opts.Store, Raw: opts.Raw, Bare: opts.Bare,
			})
			var defs []core.ToolDef
			if opts.Tools != nil {
				defs = opts.Tools.Definitions()
			}
			openPager(transcript.Render(sys, defs, opts.Session.ContextWindow()))
			continue
		}

		if line == "" {
			if !paused {
				continue
			}
			paused = false
		} else if strings.HasPrefix(line, "/") {
			if handleSlashCommand(ctx, line, &opts, &paused, pal, isTTY) {
				continue
			}
		} else {
			paused = false
			if opts.StateDir != "" && !opts.Store.Bool("no_session") {
				_ = session.AppendHistory(opts.StateDir, opts.CWD, line)
			}
			if len(opts.Session.Items) > 0 {
				opts.Session.Append(core.Item{Kind: core.KindTurnBoundary})
			}
			opts.Session.Append(core.Item{
				Kind:   core.KindUser,
				Text:   line,
				Images: agent.ExtractPromptImages(opts.CWD, line),
			})
		}

		sysPrompt := agent.BuildSystemPrompt(agent.PromptOptions{
			CWD: opts.CWD, Model: opts.Model, ShellName: shellName, Store: opts.Store, Raw: opts.Raw, Bare: opts.Bare,
		})

		notify.EmitProgramStatus(opts.Stdout, opts.Store.String("program_status"), notify.StateWorking, isTTY)
		var keepAwake platform.KeepAwake
		if opts.Store.Bool("keep_awake") {
			keepAwake = platform.InhibitSleep("gax turn")
		}

		res := runInteractiveTurn(ctx, opts, sysPrompt, &colsAtom, pal, isTTY, &focused)
		if keepAwake != nil {
			keepAwake.Stop()
		}

		if res.Outcome == core.OutcomePaused || res.Outcome == core.OutcomeInterrupted {
			paused = true
			notify.EmitProgramStatus(opts.Stdout, opts.Store.String("program_status"), notify.StateWaiting, isTTY)
		} else if res.Outcome == core.OutcomeError {
			notify.EmitProgramStatus(opts.Stdout, opts.Store.String("program_status"), notify.StateFailed, isTTY)
		} else {
			notify.EmitProgramStatus(opts.Stdout, opts.Store.String("program_status"), notify.StateDone, isTTY)
		}
		notify.EmitAttention(opts.Stdout, opts.Store.String("notify"), focused.Load(), res.FinalText, isTTY)
	}
}

func runInteractiveTurn(ctx context.Context, opts Options, sysPrompt string, colsAtom *atomic.Int64, pal theme.Palette, isTTY bool, focused *atomic.Bool) agent.LoopResult {
	region := live.NewRegion(opts.Stdout, isTTY)
	cols := int(colsAtom.Load())
	region.SetCols(cols)
	var sig atomic.Int32
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopSig := platform.WatchSignals(
		func() { sig.Store(int32(agent.SignalPause)) },
		func() {
			if sig.Load() == int32(agent.SignalAbort) {
				cancel()
				return
			}
			sig.Store(int32(agent.SignalAbort))
			cancel()
		},
	)
	defer stopSig()

	escDone := make(chan struct{})
	var escWG sync.WaitGroup
	if isTTY && opts.StdinFile != nil {
		if rawSt, rerr := platform.EnterRaw(opts.StdinFile, nil, true); rerr == nil {
			escWG.Add(1)
			go func() {
				defer escWG.Done()
				defer rawSt.Restore()
				for {
					select {
					case <-escDone:
						return
					default:
					}
					b, ok, _ := platform.ReadByteTimeout(opts.StdinFile, 50*time.Millisecond)
					if ok && b == 0x1b {
						if b2, ok2, _ := platform.ReadByteTimeout(opts.StdinFile, 10*time.Millisecond); ok2 && b2 == '[' {
							if b3, ok3, _ := platform.ReadByteTimeout(opts.StdinFile, 10*time.Millisecond); ok3 {
								if b3 == 'I' {
									focused.Store(true)
									continue
								}
								if b3 == 'O' {
									focused.Store(false)
									continue
								}
							}
						}
						if sig.Load() == int32(agent.SignalPause) {
							sig.Store(int32(agent.SignalAbort))
							cancel()
						} else {
							sig.Store(int32(agent.SignalPause))
						}
					}
				}
			}()
		}
	}
	defer func() {
		close(escDone)
		escWG.Wait()
	}()

	var textBuf strings.Builder
	var toolOutBuf strings.Builder
	var statusLbl live.StatusLabel
	t0 := time.Now()
	frame := 0

	res := agent.RunLoop(turnCtx, agent.LoopOptions{
		Session:          opts.Session,
		Provider:         opts.Provider,
		Tools:            opts.Tools,
		SystemPrompt:     sysPrompt,
		Model:            opts.Model,
		Effort:           opts.Effort,
		SessionID:        opts.Session.Header.ID,
		Catalog:          opts.Catalog,
		CatalogID:        registry.CatalogIDFor(opts.Provider.ID(), opts.Store),
		MaxTurns:         int(opts.Store.Int("max_turns")),
		ContextLimit:     opts.ContextLimit,
		CompactAuto:      opts.Store.Bool("compact.auto"),
		CompactThreshold: opts.Store.Int("compact.threshold"),
		StreamOptions:    opts.StreamOptions,
		Sink: core.EventFunc(func(ev core.Event) {
			cols = int(colsAtom.Load())
			region.SetCols(cols)
			switch e := ev.(type) {
			case core.TextDelta:
				textBuf.WriteString(e.Text)
				lbl := statusLbl.Update("streaming", false, time.Now())
				frame++
				region.Paint(
					markdown.Render(textBuf.String(), cols, pal, opts.Store.Bool("markdown")),
					live.FormatSpinner(frame, time.Since(t0), lbl, pal.Dim, pal.Reset),
				)
			case core.ToolCallStarted:
				toolOutBuf.Reset()
				msg := "running " + e.Name
				if e.ArgsSummary != "" {
					msg += " " + truncateSingleLine(e.ArgsSummary, 48)
				}
				lbl := statusLbl.Update(msg, true, time.Now())
				frame++
				region.Paint(live.FormatSpinner(frame, time.Since(t0), lbl, pal.Dim, pal.Reset))
			case core.ToolOutputDelta:
				toolOutBuf.WriteString(e.Chunk)
				if toolOutBuf.Len() > 4096 {
					s := toolOutBuf.String()
					toolOutBuf.Reset()
					toolOutBuf.WriteString(s[len(s)-2048:])
				}
				preview := tailToolPreview(toolOutBuf.String(), 3, cols, pal)
				frame++
				spin := live.FormatSpinner(frame, time.Since(t0), statusLbl.Update("", false, time.Now()), pal.Dim, pal.Reset)
				if preview != "" {
					region.Paint(preview, spin)
				} else {
					region.Paint(spin)
				}
			case core.ToolCallFinished:
				toolOutBuf.Reset()
				if textBuf.Len() > 0 {
					region.Commit(markdown.Render(textBuf.String(), cols, pal, opts.Store.Bool("markdown")) + "\n")
					textBuf.Reset()
				} else {
					region.Clear()
				}
				mark := "✓"
				color := pal.Tool
				if e.IsError {
					mark = "✗"
					color = pal.Error
				}
				if e.ArgsSummary != "" {
					fmt.Fprintf(opts.Stdout, "%s%s %s%s %s %s(%dms)%s\n", color, mark, e.Name, pal.Reset, e.ArgsSummary, pal.Dim, e.DurationMS, pal.Reset)
				} else {
					fmt.Fprintf(opts.Stdout, "%s%s %s %s(%dms)%s\n", color, mark, e.Name, pal.Dim, e.DurationMS, pal.Reset)
				}
				if strings.HasPrefix(e.Summary, "--- ") {
					printColoredDiff(opts.Stdout, e.Summary, pal)
				} else if sum := truncateSingleLine(e.Summary, max(24, cols-4)); sum != "" {
					fmt.Fprintf(opts.Stdout, "%s  %s%s\n", pal.Dim, sum, pal.Reset)
				}
			case core.CompactStarted:
				lbl := statusLbl.Update("compacting", true, time.Now())
				region.Paint(live.FormatSpinner(frame, time.Since(t0), lbl, pal.Dim, pal.Reset))
			case core.TurnUsage:
				region.Clear()
			}
		}),
		CheckSignal: func() agent.Signal {
			return agent.Signal(sig.Load())
		},
	})

	region.Clear()
	if textBuf.Len() > 0 {
		fmt.Fprintln(opts.Stdout, markdown.Render(textBuf.String(), cols, pal, opts.Store.Bool("markdown")))
	}
	if res.ErrorMessage != "" {
		fmt.Fprintf(opts.Stdout, "%serror: %s%s\n", pal.Error, res.ErrorMessage, pal.Reset)
	}
	totals := opts.Session.Totals()
	ctxPart := formatContextUsage(res.LastContextTokens, res.ContextLimit)
	if ctxPart != "" {
		ctxPart = " · " + ctxPart
	}
	fmt.Fprintf(opts.Stdout, "%s%s · %d turns · in %d · out %d%s · $%.4f%s\n\n",
		pal.Dim, time.Since(t0).Round(time.Millisecond), res.Turns, totals.InputTokens, totals.OutputTokens, ctxPart, totals.CostTotal, pal.Reset)
	return res
}

func formatContextUsage(used, limit int64) string {
	if used <= 0 {
		return ""
	}
	if limit > 0 {
		pct := (used * 100) / limit
		return fmt.Sprintf("ctx %s/%s (%d%%)", formatTokenCount(used), formatTokenCount(limit), pct)
	}
	return "ctx " + formatTokenCount(used)
}

func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

type inputAction uint8

const (
	actionNone inputAction = iota
	actionPagerHistory
	actionPagerTranscript
)

func readPromptLine(ctx context.Context, cwd, stateDir string, r *bufio.Reader, inFile *os.File, out io.Writer, outFile *os.File, isTTY bool, cols int, prompt string, ed *editor.Buffer, store *config.Store, pal theme.Palette, focused *atomic.Bool, argChoices func(string) []string) (string, bool, inputAction) {
	if !isTTY || inFile == nil {
		_, _ = io.WriteString(out, prompt)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", true, actionNone
		}
		return strings.TrimSpace(line), false, actionNone
	}
	st, err := platform.EnterRaw(inFile, outFile, false)
	if err != nil {
		_, _ = io.WriteString(out, prompt)
		line, rerr := r.ReadString('\n')
		if rerr != nil && line == "" {
			return "", true, actionNone
		}
		return strings.TrimSpace(line), false, actionNone
	}
	defer st.Restore()
	_, _ = io.WriteString(out, "\x1b[?2004h")
	defer func() { _, _ = io.WriteString(out, "\x1b[?2004l") }()

	prevCursorRow := 0
	repaint := func() {
		seq, cRow, _ := ed.RepaintSeq(prompt, cols, prevCursorRow)
		prevCursorRow = cRow
		_, _ = io.WriteString(out, seq)
	}
	repaint()
	ctrlCCount := 0
	pasting := false
	for {
		rn, _, err := r.ReadRune()
		if err != nil {
			return "", true, actionNone
		}
		if pasting {
			if rn == 0x1b && r.Buffered() > 0 {
				next, _ := r.ReadByte()
				if next == '[' {
					csi := readCSISeq(r)
					if csi == "201~" {
						pasting = false
						repaint()
					}
					continue
				}
			}
			if rn == '\r' {
				if r.Buffered() > 0 {
					if b, err := r.Peek(1); err == nil && b[0] == '\n' {
						_, _ = r.ReadByte()
					}
				}
				ed.Insert("\n")
			} else if rn == '\n' || rn == '\t' || rn >= 32 {
				ed.Insert(string(rn))
			}
			if r.Buffered() == 0 {
				repaint()
			}
			continue
		}
		if rn != 0x03 {
			ctrlCCount = 0
		}
		switch rn {
		case '\r', '\n':
			ed.MoveEnd()
			repaint()
			_, _ = io.WriteString(out, "\r\n")
			return ed.Submit(), false, actionNone
		case 0x01: // Ctrl-A
			ed.MoveHome()
		case 0x05: // Ctrl-E
			ed.MoveEnd()
		case 0x04: // Ctrl-D
			if ed.Text() == "" {
				return "", true, actionNone
			}
			ed.DeleteForward()
		case 0x03: // Ctrl-C
			if ed.Text() != "" {
				ed.Clear()
			} else {
				ctrlCCount++
				if ctrlCCount >= 2 {
					return "", true, actionNone
				}
			}
		case 0x0c: // Ctrl-L
			_, _ = io.WriteString(out, "\x1b[2J\x1b[H")
			prevCursorRow = 0
		case 0x07: // Ctrl-G
			_ = st.Restore()
			if edited, ok := editInExternalEditor(ed.Text()); ok {
				ed.Set(edited)
			}
			st, _ = platform.EnterRaw(inFile, outFile, false)
			prevCursorRow = 0
		case 0x12: // Ctrl-R
			_ = st.Restore()
			searchHistoryPicker(stateDir, cwd, inFile, outFile, ed)
			st, _ = platform.EnterRaw(inFile, outFile, false)
			prevCursorRow = 0
		case 0x16: // Ctrl-V
			pasteClipboardIntoEditor(ctx, ed)
		case 0x0f: // Ctrl-O
			_, _ = io.WriteString(out, "\r\n")
			return "", false, actionPagerHistory
		case 0x14: // Ctrl-T
			_, _ = io.WriteString(out, "\r\n")
			return "", false, actionPagerTranscript
		case 0x17: // Ctrl-W
			ed.DeleteWordBackward()
		case 0x15: // Ctrl-U
			ed.KillToStart()
		case 0x0b: // Ctrl-K
			ed.KillToEnd()
		case 0x09: // Tab
			if completeFileMention(cwd, ed) {
				break
			}
			matches, lcp := slash.Complete(ed.Text(), argChoices)
			if lcp != "" && lcp != ed.Text() {
				ed.Set(lcp)
			} else if len(matches) > 1 {
				ed.MoveEnd()
				repaint()
				_, _ = fmt.Fprintf(out, "\r\n%s%s%s\r\n", pal.Dim, strings.Join(matches, "  "), pal.Reset)
				prevCursorRow = 0
			}
		case 0x7f, 0x08:
			ed.Backspace()
		case 0x1b:
			if r.Buffered() > 0 {
				next, _ := r.ReadByte()
				switch next {
				case '[':
					switch readCSISeq(r) {
					case "I":
						focused.Store(true)
						continue
					case "O":
						focused.Store(false)
						continue
					case "A":
						ed.HistoryPrev()
					case "B":
						ed.HistoryNext()
					case "C":
						ed.MoveRight()
					case "D":
						ed.MoveLeft()
					case "H", "1~":
						ed.MoveHome()
					case "F", "4~":
						ed.MoveEnd()
					case "3~":
						ed.DeleteForward()
					case "200~":
						pasting = true
						continue
					case "13;2u", "27;2;13~":
						ed.Insert("\n")
					case "1;5C", "1;3C":
						ed.MoveWordRight()
					case "1;5D", "1;3D":
						ed.MoveWordLeft()
					}
				case '\r', '\n':
					ed.Insert("\n")
				case 0x7f, 0x08:
					ed.DeleteWordBackward()
				case 'b':
					ed.MoveWordLeft()
				case 'f':
					ed.MoveWordRight()
				case 'd':
					ed.DeleteWordForward()
				}
			}
		default:
			if rn >= 32 {
				ed.Insert(string(rn))
			}
		}
		_ = store
		repaint()
	}
}

func readCSISeq(r *bufio.Reader) string {
	var b strings.Builder
	for i := 0; i < 32; i++ {
		c, err := r.ReadByte()
		if err != nil {
			break
		}
		b.WriteByte(c)
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return b.String()
}

func printColoredDiff(w io.Writer, diff string, pal theme.Palette) {
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			fmt.Fprintf(w, "  %s%s%s\n", pal.DiffAdd, l, pal.Reset)
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			fmt.Fprintf(w, "  %s%s%s\n", pal.DiffDel, l, pal.Reset)
		default:
			fmt.Fprintf(w, "  %s%s%s\n", pal.Dim, l, pal.Reset)
		}
	}
}

func truncateSingleLine(s string, maxRunes int) string {
	s = live.StripNewlines(s)
	rs := []rune(s)
	if len(rs) > maxRunes && maxRunes > 3 {
		return string(rs[:maxRunes-3]) + "..."
	}
	return s
}

func tailToolPreview(s string, maxLines, cols int, pal theme.Palette) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var nonEmpty []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if l != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	if len(nonEmpty) > maxLines {
		nonEmpty = nonEmpty[len(nonEmpty)-maxLines:]
	}
	maxW := max(20, cols-4)
	for i, l := range nonEmpty {
		rs := []rune(l)
		if len(rs) > maxW {
			l = string(rs[:maxW-3]) + "..."
		}
		nonEmpty[i] = pal.Dim + "  " + l + pal.Reset
	}
	return strings.Join(nonEmpty, "\n")
}

func handleSlashCommand(ctx context.Context, line string, opts *Options, paused *bool, pal theme.Palette, isTTY bool) bool {
	fields := strings.Fields(line)
	cmd := fields[0]
	arg := ""
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	switch cmd {
	case "/resume", "/undo", "/fork", "/preset", "/preset-save", "/tasks":
		return handleSessionCommands(ctx, cmd, arg, opts, paused, pal)
	case "/help":
		for _, c := range slash.Commands {
			fmt.Fprintf(opts.Stdout, "  %-16s %s\n", c.Name+" "+c.ArgHint, c.Description)
		}
		return true
	case "/session":
		st := opts.Session.Totals()
		ctxLim := opts.ContextLimit
		if ctxLim <= 0 && opts.Catalog != nil && opts.Model != "" {
			if info, ok := opts.Catalog.Lookup(opts.Provider.ID(), registry.CatalogIDFor(opts.Provider.ID(), opts.Store), opts.Model); ok {
				ctxLim = info.ContextLimit
			}
		}
		var lastCtx int64
		for i := len(opts.Session.Items) - 1; i >= 0; i-- {
			it := opts.Session.Items[i]
			if it.Kind == core.KindTurnUsage && it.Usage != nil && !it.Inherited {
				lastCtx = it.Usage.Input + it.Usage.Output
				break
			}
		}
		ctxStr := formatContextUsage(lastCtx, ctxLim)
		if ctxStr == "" && ctxLim > 0 {
			ctxStr = "ctx 0/" + formatTokenCount(ctxLim)
		}
		if ctxStr != "" {
			ctxStr = " · " + ctxStr
		}
		fmt.Fprintf(opts.Stdout, "session %s · %s · %s · turns=%d · in=%d · out=%d%s · cost=$%.4f\n",
			opts.Session.Header.ID, opts.Provider.ID(), opts.Model, st.Turns, st.InputTokens, st.OutputTokens, ctxStr, st.CostTotal)
		return true
	case "/new", "/clear":
		opts.Session = agent.NewSession(core.SessionHeader{
			Version: 1, GaxVersion: opts.Version, ID: session.NewUUID(), CWD: opts.CWD,
			Provider: opts.Provider.ID(), Model: opts.Model, Effort: opts.Effort,
		})
		*paused = false
		fmt.Fprintln(opts.Stdout, "started a new conversation")
		return true
	case "/provider":
		if arg == "" {
			avail := registry.AvailableProviders(opts.Store)
			items := make([]picker.Item, 0, len(avail))
			for _, d := range avail {
				items = append(items, picker.Item{ID: d.ID, Label: d.DisplayName + " (" + d.ID + ")"})
			}
			chosen, _ := picker.Run("provider", items, opts.StdinFile, opts.Stdout)
			if chosen == nil {
				return true
			}
			arg = chosen.ID
		}
		prov, err := registry.Resolve(arg, opts.Model, opts.CWD, opts.Store, opts.HTTPClient)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%s%v%s\n", pal.Error, err, pal.Reset)
			return true
		}
		opts.Provider = prov
		opts.StreamOptions = registry.ResolveStreamOptions(prov.ID(), opts.Model, opts.Store, opts.Catalog)
		_ = opts.Store.SaveState("provider", prov.ID())
		fmt.Fprintf(opts.Stdout, "provider: %s\n", prov.ID())
		return true
	case "/model":
		var pickedLimit int64
		if arg == "" {
			models, err := registry.ListProviderModels(ctx, opts.Provider.ID(), opts.Store)
			if err != nil {
				fmt.Fprintf(opts.Stdout, "current model: %s (%v)\n", opts.Model, err)
				return true
			}
			items := make([]picker.Item, 0, len(models))
			limits := make(map[string]int64, len(models))
			for _, m := range models {
				items = append(items, picker.Item{ID: m.ID, Label: m.DisplayName})
				limits[m.ID] = m.ContextLimit
			}
			chosen, _ := picker.Run("model", items, opts.StdinFile, opts.Stdout)
			if chosen == nil {
				return true
			}
			arg = chosen.ID
			pickedLimit = limits[arg]
		}
		opts.Model = arg
		if pickedLimit > 0 {
			opts.ContextLimit = pickedLimit
		} else if opts.Store.Int("context_limit") <= 0 {
			opts.ContextLimit = 0
		}
		opts.StreamOptions = registry.ResolveStreamOptions(opts.Provider.ID(), opts.Model, opts.Store, opts.Catalog)
		_ = opts.Store.SaveState("model", opts.Model)
		fmt.Fprintf(opts.Stdout, "model: %s\n", opts.Model)
		return true
	case "/usage":
		rep, err := registry.QueryAccountUsage(ctx, opts.Provider.ID(), opts.HTTPClient)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%s%v%s\n", pal.Error, err, pal.Reset)
		} else {
			fmt.Fprintln(opts.Stdout, rep.Summary)
		}
		return true
	case "/login":
		if err := codex.BrowserLogin(ctx, opts.StateDir, opts.Stdout); err != nil {
			fmt.Fprintf(opts.Stdout, "%slogin failed: %v%s\n", pal.Error, err, pal.Reset)
		} else {
			if opts.Provider != nil && opts.Provider.ID() == "codex" {
				if prov, rerr := registry.Resolve("codex", opts.Model, opts.CWD, opts.Store, opts.HTTPClient); rerr == nil {
					opts.Provider = prov
				}
			}
			fmt.Fprintln(opts.Stdout, "codex login saved")
		}
		return true
	case "/logout":
		if err := codex.RemoveCredentials(opts.StateDir); err != nil {
			fmt.Fprintf(opts.Stdout, "%slogout failed: %v%s\n", pal.Error, err, pal.Reset)
		} else {
			fmt.Fprintln(opts.Stdout, "codex login removed")
		}
		return true
	case "/effort":
		opts.Effort = arg
		_ = opts.Store.SaveState("effort", opts.Effort)
		fmt.Fprintf(opts.Stdout, "effort: %s\n", opts.Effort)
		return true
	case "/config":
		if len(fields) == 1 {
			for _, d := range config.Registry {
				val, src := opts.Store.Resolve(d.Key)
				fmt.Fprintf(opts.Stdout, "  %-36s = %-20s (%s)\n", d.Key, val, src)
			}
			return true
		}
		if len(fields) >= 3 {
			opts.Store.SetOverride(fields[1], strings.Join(fields[2:], " "))
			fmt.Fprintf(opts.Stdout, "%s = %s\n", fields[1], opts.Store.String(fields[1]))
		}
		return true
	case "/compact":
		if err := agent.Compact(ctx, opts.Session, opts.Provider, opts.Model, arg, false, nil); err != nil {
			fmt.Fprintf(opts.Stdout, "%scompact failed: %v%s\n", pal.Error, err, pal.Reset)
		} else {
			fmt.Fprintln(opts.Stdout, "conversation compacted")
		}
		return true
	case "/copy":
		for i := len(opts.Session.Items) - 1; i >= 0; i-- {
			if opts.Session.Items[i].Kind == core.KindAssistant && opts.Session.Items[i].Text != "" {
				if err := platform.WriteClipboard(ctx, opts.Session.Items[i].Text); err != nil {
					fmt.Fprintf(opts.Stdout, "%scopy failed: %v%s\n", pal.Error, err, pal.Reset)
				} else {
					fmt.Fprintln(opts.Stdout, "copied latest response")
				}
				return true
			}
		}
		fmt.Fprintln(opts.Stdout, "no assistant response to copy")
		return true
	default:
		_ = isTTY
		fmt.Fprintf(opts.Stdout, "unknown command %q (type /help)\n", cmd)
		return true
	}
}

func resolveCols(store *config.Store, f *os.File) int {
	dw := store.String("display_width")
	if c, _, ok := platform.TerminalSize(f); ok && (dw == "" || dw == "auto" || dw == "terminal") {
		if dw == "auto" && c > 100 {
			return 100
		}
		return c
	}
	return 80
}

func renderHistoryText(items []core.Item, cols int, pal theme.Palette, md bool) string {
	var b strings.Builder
	for _, it := range items {
		switch it.Kind {
		case core.KindUser:
			fmt.Fprintf(&b, "> %s\n\n", it.Text)
		case core.KindAssistant:
			b.WriteString(markdown.Render(it.Text, cols, pal, md))
			b.WriteString("\n\n")
		case core.KindToolCall:
			fmt.Fprintf(&b, "[tool %s] %s\n", it.ToolName, it.Arguments)
		case core.KindToolResult:
			fmt.Fprintf(&b, "%s\n\n", it.Output)
		}
	}
	return width.ResolveVT(b.String())
}

func openPager(content string) {
	pager := os.Getenv("PAGER")
	var args []string
	if pager == "" {
		pager = "less"
		args = []string{"-R"}
	}
	cmd := exec.Command(pager, args...)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

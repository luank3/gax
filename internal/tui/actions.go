package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/tui/editor"
	"github.com/luank3/gax/internal/tui/picker"
	"github.com/luank3/gax/internal/tui/theme"
)

func handleSessionCommands(ctx context.Context, cmd, arg string, opts *Options, paused *bool, pal theme.Palette) (handled bool) {
	switch cmd {
	case "/resume":
		list, err := session.List(opts.StateDir, opts.CWD)
		if err != nil || len(list) == 0 {
			fmt.Fprintln(opts.Stdout, "no past sessions in this directory")
			return true
		}
		items := make([]picker.Item, 0, len(list))
		for _, s := range list {
			label := s.ID[:8] + " · " + s.Provider
			if s.Preview != "" {
				label += " · " + s.Preview
			}
			items = append(items, picker.Item{ID: s.Path, Label: label})
		}
		chosen, _ := picker.Run("resume", items, opts.StdinFile, opts.Stdout)
		if chosen == nil {
			return true
		}
		loaded, err := session.Load(chosen.ID)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%sresume failed: %v%s\n", pal.Error, err, pal.Reset)
			return true
		}
		switchToLoaded(loaded, opts, paused)
		fmt.Fprintf(opts.Stdout, "resumed session %s\n", loaded.Header.ID[:8])
		return true

	case "/undo":
		back := 1
		if arg != "" {
			n, err := strconv.Atoi(strings.TrimSpace(arg))
			if err != nil || n < 1 {
				fmt.Fprintln(opts.Stdout, "usage: /undo [turns_back]")
				return true
			}
			back = n
		}
		total := session.CountUserTurns(opts.Session.Items)
		if total == 0 {
			fmt.Fprintln(opts.Stdout, "nothing to undo")
			return true
		}
		keep := max(0, total-back)
		opts.Session.Items = session.ApplyUndo(opts.Session.Items, keep)
		if opts.Writer != nil {
			_ = opts.Writer.AppendUndo(keep)
		}
		*paused = false
		fmt.Fprintf(opts.Stdout, "rolled back to %d user turns\n", keep)
		return true

	case "/fork":
		if opts.Writer == nil {
			fmt.Fprintln(opts.Stdout, "session recording is disabled")
			return true
		}
		keep := 0
		if arg != "" {
			back, err := strconv.Atoi(strings.TrimSpace(arg))
			if err != nil || back < 0 {
				fmt.Fprintln(opts.Stdout, "usage: /fork [turns_back]")
				return true
			}
			if back > 0 {
				keep = max(0, session.CountUserTurns(opts.Session.Items)-back)
			}
		}
		forked, err := session.Fork(opts.Writer.Path(), opts.StateDir, opts.CWD, opts.Version, keep)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%sfork failed: %v%s\n", pal.Error, err, pal.Reset)
			return true
		}
		loaded, err := session.Load(forked.Path())
		if err != nil {
			_ = forked.Close()
			fmt.Fprintf(opts.Stdout, "%sfork load failed: %v%s\n", pal.Error, err, pal.Reset)
			return true
		}
		_ = forked.Close()
		switchToLoaded(loaded, opts, paused)
		fmt.Fprintf(opts.Stdout, "forked into session %s\n", loaded.Header.ID[:8])
		return true

	case "/preset":
		presets := opts.Store.Presets()
		if len(presets) == 0 {
			fmt.Fprintln(opts.Stdout, "no presets configured")
			return true
		}
		if arg == "" {
			items := make([]picker.Item, 0, len(presets))
			for name, p := range presets {
				lbl := name
				if p.Description != "" {
					lbl += " — " + p.Description
				}
				items = append(items, picker.Item{ID: name, Label: lbl})
			}
			chosen, _ := picker.Run("preset", items, opts.StdinFile, opts.Stdout)
			if chosen == nil {
				return true
			}
			arg = chosen.ID
		}
		p, ok := presets[arg]
		if !ok {
			fmt.Fprintf(opts.Stdout, "unknown preset %q\n", arg)
			return true
		}
		opts.Preset = arg
		if p.Provider != "" {
			if prov, err := registry.Resolve(p.Provider, p.Model, opts.CWD, opts.Store, opts.HTTPClient); err == nil {
				opts.Provider = prov
			}
		}
		if p.Model != "" {
			opts.Model = p.Model
		}
		if p.Effort != "" {
			opts.Effort = p.Effort
		}
		opts.StreamOptions = registry.ResolveStreamOptions(opts.Provider.ID(), opts.Model, opts.Store, opts.Catalog)
		_ = opts.Store.SaveState("preset", arg)
		fmt.Fprintf(opts.Stdout, "preset: %s (%s %s)\n", arg, opts.Provider.ID(), opts.Model)
		return true

	case "/preset-save":
		fields := strings.Fields(arg)
		if len(fields) == 0 {
			fmt.Fprintln(opts.Stdout, "usage: /preset-save <name> [tint]")
			return true
		}
		p := config.Preset{
			Provider: opts.Provider.ID(),
			Model:    opts.Model,
			Effort:   opts.Effort,
		}
		if len(fields) > 1 {
			p.Tint = fields[1]
		}
		if err := opts.Store.SavePreset(fields[0], p); err != nil {
			fmt.Fprintf(opts.Stdout, "%ssave failed: %v%s\n", pal.Error, err, pal.Reset)
		} else {
			opts.Preset = fields[0]
			fmt.Fprintf(opts.Stdout, "saved preset %q\n", fields[0])
		}
		return true

	case "/tasks":
		if opts.Tools == nil {
			fmt.Fprintln(opts.Stdout, "tools are disabled")
			return true
		}
		fields := strings.Fields(arg)
		if len(fields) >= 2 && fields[0] == "kill" {
			n := opts.Tools.KillTask(fields[1])
			fmt.Fprintf(opts.Stdout, "stopped %d task(s)\n", n)
			return true
		}
		list := opts.Tools.ListTasks()
		if len(list) == 0 {
			fmt.Fprintln(opts.Stdout, "no background tasks")
			return true
		}
		for _, t := range list {
			state := "running"
			if t.Finished {
				state = "done"
			}
			fmt.Fprintf(opts.Stdout, "  %-12s [%s] %s\n", t.ID, state, t.Command)
		}
		return true
	}
	_ = ctx
	return false
}

func switchToLoaded(loaded *session.Loaded, opts *Options, paused *bool) {
	if opts.Writer != nil {
		_ = opts.Writer.Close()
	}
	sess := agent.NewSession(loaded.Header)
	sess.Items = append(sess.Items, loaded.LiveItems...)
	if w, err := session.OpenAppend(loaded.Path); err == nil {
		opts.Writer = w
		sess.SetOnAppend(func(it core.Item) { _ = w.AppendItem(it) })
	}
	opts.Session = sess
	if loaded.Selection.Provider != "" {
		if prov, err := registry.Resolve(loaded.Selection.Provider, loaded.Selection.Model, opts.CWD, opts.Store, opts.HTTPClient); err == nil {
			opts.Provider = prov
		}
	}
	if loaded.Selection.Model != "" {
		opts.Model = loaded.Selection.Model
	}
	if loaded.Selection.Effort != "" {
		opts.Effort = loaded.Selection.Effort
	}
	opts.StreamOptions = registry.ResolveStreamOptions(opts.Provider.ID(), opts.Model, opts.Store, opts.Catalog)
	*paused = false
}

func searchHistoryPicker(stateDir, cwd string, inFile *os.File, out *os.File, ed *editor.Buffer) {
	hist := session.LoadHistory(stateDir, cwd)
	if len(hist) == 0 {
		return
	}
	items := make([]picker.Item, 0, len(hist))
	for i := len(hist) - 1; i >= 0; i-- {
		items = append(items, picker.Item{ID: hist[i], Label: strings.ReplaceAll(hist[i], "\n", " ")})
	}
	if chosen, _ := picker.Run("history", items, inFile, out); chosen != nil {
		ed.Set(chosen.ID)
	}
}

func editInExternalEditor(current string) (string, bool) {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		for _, cand := range []string{"editor", "nano", "vim", "vi"} {
			if p, err := exec.LookPath(cand); err == nil {
				ed = p
				break
			}
		}
	}
	if ed == "" {
		return "", false
	}
	f, err := os.CreateTemp("", "gax-prompt-*.md")
	if err != nil {
		return "", false
	}
	path := f.Name()
	defer os.Remove(path)
	_, _ = f.WriteString(current)
	_ = f.Close()

	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(b), "\r\n"), true
}

func pasteClipboardIntoEditor(ctx context.Context, ed *editor.Buffer) {
	data, err := platform.ReadClipboard(ctx)
	if err != nil {
		return
	}
	if len(data.ImagePNG) > 0 {
		if f, err := os.CreateTemp("", "gax-paste-*.png"); err == nil {
			_, _ = f.Write(data.ImagePNG)
			_ = f.Close()
			ed.Insert("@" + f.Name() + " ")
			return
		}
	}
	if data.Text != "" {
		ed.Insert(data.Text)
	}
}

func completeFileMention(cwd string, ed *editor.Buffer) bool {
	text := ed.Text()
	idx := strings.LastIndex(text, "@")
	if idx < 0 {
		return false
	}
	if idx > 0 && text[idx-1] != ' ' && text[idx-1] != '\t' {
		return false
	}
	fzfPath, err := exec.LookPath("fzf")
	if err != nil {
		return false
	}
	query := text[idx+1:]
	var files []byte
	if out, err := exec.Command("git", "-C", cwd, "ls-files", "--cached", "--others", "--exclude-standard").Output(); err == nil {
		files = out
	} else {
		entries, _ := os.ReadDir(cwd)
		var b bytes.Buffer
		for _, e := range entries {
			b.WriteString(e.Name())
			b.WriteByte('\n')
		}
		files = b.Bytes()
	}
	cmd := exec.Command(fzfPath, "--query="+query, "--select-1", "--exit-0")
	cmd.Stdin = bytes.NewReader(files)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return true
	}
	picked := strings.TrimSpace(string(out))
	if picked != "" {
		ed.Set(text[:idx+1] + filepath.Clean(picked) + " ")
	}
	return true
}

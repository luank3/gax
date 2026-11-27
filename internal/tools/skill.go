package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/skill"
)

type skillTool struct{ reg *Registry }

func (t *skillTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "skill",
		Description: "Load a skill by name into the conversation, or omit name to list available skills.",
		Params: []core.ToolParam{
			{Name: "name", Type: "string", Description: "Skill to load. Omit to list available skills."},
		},
	}
}

func (t *skillTool) Run(_ context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Name string `json:"name"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	name := strings.TrimSpace(args.Name)
	dirs := platform.ResolveDirs()
	entries := skill.Discover(skill.Options{Home: dirs.Home, CWD: t.reg.cwd})
	if name == "" {
		var b strings.Builder
		for i, e := range entries {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "- %s: %s (%s)", e.Name, e.Description, e.DisplayPath)
		}
		if b.Len() == 0 {
			return Result{Text: "no skills are available"}
		}
		return Result{Text: b.String()}
	}

	for _, e := range entries {
		if e.Name != name {
			continue
		}
		body, err := e.Load()
		if err != nil {
			return Result{Text: fmt.Sprintf("error loading skill %q: %v", name, err), IsError: true}
		}
		doc := fmt.Sprintf("[skill: %s (%s)]\n%s", e.Name, e.DisplayPath, strings.TrimRight(body, "\n"))
		if t.alreadyLoaded(doc) {
			return Result{Text: fmt.Sprintf("[skill: %s (%s) — already loaded in active conversation; continue following the instructions above without reloading]", e.Name, e.DisplayPath)}
		}
		return Result{Text: doc}
	}
	return Result{Text: fmt.Sprintf("unknown skill %q", name), IsError: true}
}

func (t *skillTool) alreadyLoaded(doc string) bool {
	t.reg.mu.Lock()
	fn := t.reg.activeItems
	t.reg.mu.Unlock()
	if fn == nil {
		return false
	}
	for _, it := range fn() {
		if it.Kind == core.KindToolResult && (it.ToolName == "" || it.ToolName == "skill") && it.Output == doc {
			return true
		}
	}
	return false
}

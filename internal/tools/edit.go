package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/luank3/gax/internal/core"
)

type editTool struct{ reg *Registry }

func (t *editTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "edit",
		Description: "Replace exact text in a file. Fails if old_text is missing or ambiguous (unless replace_all is true).",
		Params: []core.ToolParam{
			{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
			{Name: "old_text", Type: "string", Required: true, Description: "Exact text to replace."},
			{Name: "new_text", Type: "string", Required: true, Description: "Replacement text."},
			{Name: "replace_all", Type: "boolean", Description: "Replace all occurrences."},
		},
	}
}

func (t *editTool) Run(_ context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if args.Path == "" || args.OldText == "" {
		return Result{Text: "error: 'path' and 'old_text' are required", IsError: true}
	}

	resolved := t.reg.resolvePath(args.Path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %v", args.Path, err), IsError: true}
	}
	src := string(raw)
	count := strings.Count(src, args.OldText)
	if count == 0 {
		return Result{Text: fmt.Sprintf("error: old_text not found in %s", args.Path), IsError: true}
	}
	if count > 1 && !args.ReplaceAll {
		return Result{Text: fmt.Sprintf("error: old_text matched %d times in %s; provide more context or set replace_all", count, args.Path), IsError: true}
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(src, args.OldText, args.NewText)
	} else {
		updated = strings.Replace(src, args.OldText, args.NewText, 1)
	}

	info, _ := os.Stat(resolved)
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(resolved, []byte(updated), mode); err != nil {
		return Result{Text: fmt.Sprintf("error writing %s: %v", args.Path, err), IsError: true}
	}
	return Result{Text: formatDiff(args.Path, src, updated)}
}

package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luank3/gax/internal/core"
)

type writeTool struct{ reg *Registry }

func (t *writeTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "write",
		Description: "Write content to a file, creating parent directories if needed.",
		Params: []core.ToolParam{
			{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
			{Name: "content", Type: "string", Required: true, Description: "Full file contents."},
		},
	}
}

func (t *writeTool) Run(_ context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if strings.TrimSpace(args.Path) == "" {
		return Result{Text: "error: 'path' is required", IsError: true}
	}
	resolved := t.reg.resolvePath(args.Path)
	oldBytes, _ := os.ReadFile(resolved)

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return Result{Text: fmt.Sprintf("error creating directory for %s: %v", args.Path, err), IsError: true}
	}
	if err := os.WriteFile(resolved, []byte(args.Content), 0o644); err != nil {
		return Result{Text: fmt.Sprintf("error writing %s: %v", args.Path, err), IsError: true}
	}
	if len(oldBytes) == 0 {
		return Result{Text: fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content))}
	}
	return Result{Text: formatDiff(args.Path, string(oldBytes), args.Content)}
}

// formatDiff produces a compact unified-style diff summary for edit/write results.
func formatDiff(path, before, after string) string {
	if before == after {
		return fmt.Sprintf("no changes in %s", path)
	}
	oldLines := strings.Split(before, "\n")
	newLines := strings.Split(after, "\n")

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	ctxStart := max(0, prefix-2)
	oldEnd := len(oldLines) - suffix
	newEnd := len(newLines) - suffix
	ctxEndOld := min(len(oldLines), oldEnd+2)
	ctxEndNew := min(len(newLines), newEnd+2)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", ctxStart+1, ctxEndOld-ctxStart, ctxStart+1, ctxEndNew-ctxStart)
	for i := ctxStart; i < prefix; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	for i := prefix; i < oldEnd; i++ {
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for i := prefix; i < newEnd; i++ {
		fmt.Fprintf(&b, "+%s\n", newLines[i])
	}
	for i := oldEnd; i < ctxEndOld; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	return b.String()
}

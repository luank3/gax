// Package tools implements gax's built-in agent tools and background task registry.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/web"
)

// OutputSink receives live streamed tool output chunks (e.g., bash stdout/stderr).
type OutputSink func(stream, chunk string)

// Result is the outcome of one tool invocation.
type Result struct {
	Text    string
	Images  []core.ImageAttachment
	IsError bool
}

// Tool is a single executable tool advertised to the model.
type Tool interface {
	Definition() core.ToolDef
	Run(ctx context.Context, arguments string, sink OutputSink) Result
}

// Registry holds active tools, configuration, and background tasks.
type Registry struct {
	cwd         string
	store       *config.Store
	activeItems func() []core.Item
	tools       map[string]Tool
	order       []string
	tasks       *taskManager
	mu          sync.Mutex
}

// NewRegistry creates a tool registry rooted at cwd.
func NewRegistry(cwd string, store *config.Store) *Registry {
	if store == nil {
		store = config.NewStore()
	}
	r := &Registry{
		cwd:   cwd,
		store: store,
		tools: make(map[string]Tool),
		tasks: newTaskManager(int(store.Int("task.max_running"))),
	}
	r.Register(&readTool{reg: r})
	r.Register(&editTool{reg: r})
	r.Register(&writeTool{reg: r})
	r.Register(&bashTool{reg: r})
	if !store.Bool("no_tasks") {
		r.Register(&taskWaitTool{reg: r})
	}
	if !store.Bool("no_skills") {
		r.Register(&skillTool{reg: r})
	}
	if !store.Bool("no_web") {
		wc := web.NewClient()
		r.Register(&webFetchTool{reg: r, client: wc})
		r.Register(&webSearchTool{reg: r, client: wc})
	}
	return r
}

// Register adds or replaces a tool in the registry.
func (r *Registry) Register(t Tool) {
	def := t.Definition()
	if _, exists := r.tools[def.Name]; !exists {
		r.order = append(r.order, def.Name)
	}
	r.tools[def.Name] = t
}

// SetActiveItems supplies a callback returning current active conversation items
// (used by skill deduplication and MCP schema compaction checks).
func (r *Registry) SetActiveItems(fn func() []core.Item) {
	r.mu.Lock()
	r.activeItems = fn
	r.mu.Unlock()
}

// Definitions returns tool definitions in registration order.
func (r *Registry) Definitions() []core.ToolDef {
	defs := make([]core.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Definition())
	}
	return defs
}

// Execute runs the named tool with JSON arguments.
func (r *Registry) Execute(ctx context.Context, name, arguments string, sink OutputSink) Result {
	t, ok := r.tools[name]
	if !ok {
		return Result{Text: fmt.Sprintf("error: unknown tool %q", name), IsError: true}
	}
	return t.Run(ctx, arguments, sink)
}

// Close stops any running background tasks owned by this registry.
func (r *Registry) Close() {
	if r != nil && r.tasks != nil {
		r.tasks.stopAll()
	}
}

func (r *Registry) resolvePath(p string) string {
	if p == "" || filepath.IsAbs(p) || r.cwd == "" {
		return p
	}
	return filepath.Join(r.cwd, p)
}

func (r *Registry) outputCap() int {
	capBytes := int(r.store.Bytes("tool_output_cap"))
	if capBytes <= 0 {
		return 50 * 1024
	}
	return capBytes
}

func capOutput(text string, capBytes int, spillPrefix string) string {
	if len(text) <= capBytes {
		return text
	}
	spillPath := ""
	if f, err := os.CreateTemp("", spillPrefix+"-*.log"); err == nil {
		_, _ = f.WriteString(text)
		spillPath = f.Name()
		_ = f.Close()
	}
	head := capBytes / 2
	tail := capBytes / 2
	omitted := len(text) - head - tail
	note := fmt.Sprintf("\n\n[... %d bytes omitted; full output saved to %s ...]\n\n", omitted, spillPath)
	if spillPath == "" {
		note = fmt.Sprintf("\n\n[... %d bytes omitted ...]\n\n", omitted)
	}
	return text[:head] + note + text[len(text)-tail:]
}

func decodeArgs(raw string, dst any) error {
	if raw == "" {
		raw = "{}"
	}
	return json.Unmarshal([]byte(raw), dst)
}

func intPtr(v int) *int { return &v }

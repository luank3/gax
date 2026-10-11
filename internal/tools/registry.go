// Package tools implements the model-callable tools built into gax.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/diag"
	"github.com/luank3/gax/internal/provider"
	systemshell "github.com/luank3/gax/internal/system/shell"
	"github.com/luank3/gax/internal/system/tempfiles"
	"github.com/luank3/gax/internal/web"
)

const defaultOutputCap = 50 * 1024

type Options struct {
	OutputCap        int
	NoTasks          bool
	NoWeb            bool
	BashTimeout      time.Duration
	BashTimeoutMax   time.Duration
	BashTimeoutGrace time.Duration
	BackgroundYield  time.Duration
	TaskWaitTimeout  time.Duration
	TaskMaxRunning   int
	Shell            string
	WorkDir          string
	UseExactValues   bool
	WebTimeout       time.Duration
	WebMaxBytes      int64
	WebClient        *web.Client
}

type Result struct {
	Text                    string
	Images                  []conversation.Image
	OutputSummarizesDisplay bool
	HiddenTail              int
}

// TaskSnapshot describes one background command without exposing its process handle.
type TaskSnapshot struct {
	ID         string
	Command    string
	Running    bool
	ExitCode   int
	TermSignal int
	Elapsed    time.Duration
}

// DisplaySink receives synchronous tool bytes for the terminal presentation only.
type DisplaySink func([]byte) error

type ToolOutputStyle uint8

const (
	OutputPlain ToolOutputStyle = iota
	OutputUnifiedDiff
)

type ToolPreviewMode uint8

const (
	PreviewHead ToolPreviewMode = iota
	PreviewHeadTail
	PreviewCollapsed
)

// ToolPresentation contains user-facing metadata resolved from the original tool call.
type ToolPresentation struct {
	Argument          string
	HasArgument       bool
	RawArgument       bool
	Extra             string
	OutputStyle       ToolOutputStyle
	PreviewMode       ToolPreviewMode
	HeaderRows        int
	CollapsedArgument string
}

// ToolExecution separates the canonical model result from bytes already sent to the display.
type ToolExecution struct {
	Result                  conversation.Item
	Presentation            ToolPresentation
	OutputSummarizesDisplay bool
	// FallbackOutput is shown when streamed display bytes settle to no visible rows.
	FallbackOutput *string
	HiddenTail     int
}

type Registry struct {
	outputCap   int
	options     Options
	store       *config.Store
	tasks       *taskManager
	definitions []provider.ToolDefinition
	activeMu    sync.Mutex
	active      map[int]*commandTask
	selectionMu sync.RWMutex
	selection   childSelection
	shellMu     sync.Mutex
	shellWarned bool
	diagnostics io.Writer
	spoolMu     sync.Mutex
	spoolPaths  map[string]struct{}
	spoolClosed bool
}

type childSelection struct {
	provider string
	model    string
	effort   string
	active   bool
}

func New(options Options) *Registry {
	capBytes := options.OutputCap
	if capBytes <= 0 {
		capBytes = defaultOutputCap
	}
	if !options.UseExactValues && options.BashTimeout <= 0 {
		options.BashTimeout = 2 * time.Minute
	}
	if !options.UseExactValues && options.BashTimeoutMax <= 0 {
		options.BashTimeoutMax = 30 * time.Minute
	}
	if !options.UseExactValues && options.BashTimeoutGrace <= 0 {
		options.BashTimeoutGrace = 2 * time.Second
	}
	if !options.UseExactValues && options.BackgroundYield <= 0 {
		options.BackgroundYield = 5 * time.Second
	}
	if !options.UseExactValues && options.TaskWaitTimeout <= 0 {
		options.TaskWaitTimeout = 10 * time.Minute
	}
	if options.TaskMaxRunning <= 0 {
		options.TaskMaxRunning = 32
	}
	registry := &Registry{outputCap: capBytes, options: options, active: make(map[int]*commandTask)}
	registry.definitions = builtInDefinitions(options.NoTasks, options.NoWeb)
	registry.tasks = newTaskManager(options.TaskMaxRunning)
	return registry
}

// SetSelection updates the provider selection exported to shell children. An empty provider
// clears the override so the inherited environment passes through unchanged.
func (registry *Registry) SetSelection(provider string, model, effort *string) {
	if registry == nil {
		return
	}
	selection := childSelection{}
	if provider != "" {
		selection = childSelection{
			provider: provider, model: stringValueOrEmpty(model), effort: stringValueOrEmpty(effort), active: true,
		}
	}
	registry.selectionMu.Lock()
	registry.selection = selection
	registry.selectionMu.Unlock()
}

// SetDiagnosticWriter routes shell-configuration warnings through the owning application.
func (registry *Registry) SetDiagnosticWriter(writer io.Writer) {
	if registry == nil {
		return
	}
	registry.shellMu.Lock()
	registry.diagnostics = writer
	registry.shellMu.Unlock()
}

// ResolveShell resolves the configured shell for the environment prompt and reports an invalid
// configuration once. Tool execution uses the same resolver and warning path.
func (registry *Registry) ResolveShell() string {
	if registry == nil {
		return ""
	}
	path, _ := registry.resolveShell()
	return path
}

func (registry *Registry) resolveShell() (string, error) {
	configuredShell := registry.options.Shell
	if registry.store != nil {
		configuredShell = configString(registry.store, "bash.shell")
	}
	path, invalid := systemshell.Resolve(configuredShell)
	if invalid {
		registry.shellMu.Lock()
		if !registry.shellWarned && registry.diagnostics != nil {
			diag.Warn(registry.diagnostics, "bash.shell: '%s' not found or not executable; using default", configuredShell)
			registry.shellWarned = true
		}
		registry.shellMu.Unlock()
	}
	if path == "" {
		return "", errors.New("no bash or sh executable found")
	}
	return path, nil
}

func stringValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func NewFromConfig(store *config.Store) *Registry {
	if store == nil {
		store = config.New(config.DefaultSettings())
	}
	registry := New(Options{
		OutputCap: int(store.Size("tool_output_cap")), NoTasks: store.Bool("no_tasks"), NoWeb: store.Bool("no_web"),
		BashTimeout: store.Duration("bash.timeout"), BashTimeoutMax: store.Duration("bash.timeout_max"),
		BashTimeoutGrace: store.Duration("bash.timeout_grace"), BackgroundYield: store.Duration("bash.background_yield"),
		TaskWaitTimeout: store.Duration("task.wait_timeout"), TaskMaxRunning: store.Int("task.max_running"),
		Shell: configString(store, "bash.shell"), UseExactValues: true,
		WebTimeout: store.Duration("web.timeout"), WebMaxBytes: store.Size("web.max_bytes"),
	})
	registry.store = store
	return registry
}

func (registry *Registry) outputLimit() int {
	if registry != nil && registry.store != nil {
		if value := registry.store.Size("tool_output_cap"); value > 0 && value <= int64(^uint(0)>>1) {
			return int(value)
		}
	}
	if registry == nil {
		return defaultOutputCap
	}
	return registry.outputCap
}

func (registry *Registry) noTasks() bool {
	if registry != nil && registry.store != nil {
		return registry.store.Bool("no_tasks")
	}
	return registry != nil && registry.options.NoTasks
}

func (registry *Registry) bashTimeout() time.Duration {
	if registry != nil && registry.store != nil {
		return registry.store.Duration("bash.timeout")
	}
	return registry.options.BashTimeout
}

func (registry *Registry) bashTimeoutMax() time.Duration {
	if registry != nil && registry.store != nil {
		return registry.store.Duration("bash.timeout_max")
	}
	return registry.options.BashTimeoutMax
}

func (registry *Registry) bashTimeoutGrace() time.Duration {
	if registry != nil && registry.store != nil {
		return registry.store.Duration("bash.timeout_grace")
	}
	return registry.options.BashTimeoutGrace
}

func (registry *Registry) backgroundYield() time.Duration {
	if registry != nil && registry.store != nil {
		return registry.store.Duration("bash.background_yield")
	}
	return registry.options.BackgroundYield
}

func (registry *Registry) taskWaitTimeout() time.Duration {
	if registry != nil && registry.store != nil {
		return registry.store.Duration("task.wait_timeout")
	}
	return registry.options.TaskWaitTimeout
}

func (registry *Registry) taskMaxRunning() int {
	if registry != nil && registry.store != nil {
		if value := registry.store.Int("task.max_running"); value > 0 {
			return value
		}
	}
	if registry == nil {
		return 0
	}
	return registry.options.TaskMaxRunning
}

func configString(store *config.Store, key string) string {
	if store == nil {
		return ""
	}
	value := store.String(key)
	if value == nil {
		return ""
	}
	return *value
}

func (registry *Registry) Close() {
	if registry != nil && registry.tasks != nil {
		registry.tasks.Close()
	}
	if registry != nil {
		registry.spoolMu.Lock()
		registry.spoolClosed = true
		paths := make([]string, 0, len(registry.spoolPaths))
		for path := range registry.spoolPaths {
			paths = append(paths, path)
		}
		registry.spoolPaths = nil
		registry.spoolMu.Unlock()
		for _, path := range paths {
			_ = os.Remove(path)
			tempfiles.Untrack(path)
		}
	}
}

// KillChildren immediately kills every active shell process group. It is used when a repeated
// termination signal escalates past normal task cleanup.
func (registry *Registry) KillChildren() {
	if registry == nil {
		return
	}
	registry.activeMu.Lock()
	tasks := make([]*commandTask, 0, len(registry.active))
	for _, task := range registry.active {
		tasks = append(tasks, task)
	}
	registry.activeMu.Unlock()
	for _, task := range tasks {
		if !task.isDone() {
			killTaskGroup(task, syscall.SIGKILL)
		}
	}
}

func (registry *Registry) trackChild(task *commandTask) {
	if registry == nil || task == nil || task.cmd == nil || task.cmd.Process == nil {
		return
	}
	registry.activeMu.Lock()
	registry.active[task.cmd.Process.Pid] = task
	registry.activeMu.Unlock()
}

func (registry *Registry) untrackChild(task *commandTask) {
	if registry == nil || task == nil || task.cmd == nil || task.cmd.Process == nil {
		return
	}
	registry.activeMu.Lock()
	delete(registry.active, task.cmd.Process.Pid)
	registry.activeMu.Unlock()
}

// CollectTaskNotes returns one-line notices for completed tasks without delivering their output.
// A notice is emitted once; output remains available to task_wait while it is pending.
func (registry *Registry) CollectTaskNotes() string {
	if registry == nil || registry.tasks == nil {
		return ""
	}
	return registry.tasks.collectNotes()
}

// ExitTaskNotes reports the final state of tasks before shutdown discards their output.
func (registry *Registry) ExitTaskNotes() string {
	if registry == nil || registry.tasks == nil {
		return ""
	}
	return registry.tasks.exitNotes()
}

// ListTasks returns the uncollected background tasks in launch order.
func (registry *Registry) ListTasks() []TaskSnapshot {
	if registry == nil || registry.tasks == nil {
		return nil
	}
	return registry.tasks.list()
}

// StopTasks signals matching tasks, or every uncollected task when all is true. Unknown IDs and
// duplicates are ignored, and the return value counts tasks that were running when selected.
func (registry *Registry) StopTasks(ids []string, all bool) int {
	if registry == nil || registry.tasks == nil {
		return 0
	}
	return registry.tasks.stop(ids, all, registry.bashTimeoutGrace())
}

// ResetTasks stops tasks from the current conversation and starts a fresh task registry.
func (registry *Registry) ResetTasks() {
	if registry == nil {
		return
	}
	if registry.tasks != nil {
		registry.tasks.Close()
	}
	registry.tasks = newTaskManager(registry.taskMaxRunning())
	registry.spoolMu.Lock()
	paths := make([]string, 0, len(registry.spoolPaths))
	for path := range registry.spoolPaths {
		paths = append(paths, path)
	}
	registry.spoolPaths = nil
	registry.spoolMu.Unlock()
	for _, path := range paths {
		_ = os.Remove(path)
		tempfiles.Untrack(path)
	}
}

func (registry *Registry) preserveOutputLog(path string) {
	registry.spoolMu.Lock()
	if registry.spoolClosed {
		registry.spoolMu.Unlock()
		_ = os.Remove(path)
		tempfiles.Untrack(path)
		return
	}
	if registry.spoolPaths == nil {
		registry.spoolPaths = make(map[string]struct{})
	}
	registry.spoolPaths[path] = struct{}{}
	registry.spoolMu.Unlock()
}

func (registry *Registry) bashWorkingDirectory() string {
	if registry == nil {
		return ""
	}
	cwd := registry.options.WorkDir
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	} else {
		var err error
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return ""
		}
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	if !filepath.IsAbs(cwd) {
		return ""
	}
	return filepath.Clean(cwd)
}

func (registry *Registry) Definitions() []provider.ToolDefinition {
	if registry == nil {
		return nil
	}
	definitionsSource := registry.definitions
	if registry.store != nil {
		definitionsSource = builtInDefinitions(registry.noTasks(), registry.noWeb())
	}
	definitions := make([]provider.ToolDefinition, len(definitionsSource))
	for index, definition := range definitionsSource {
		definitions[index] = definition
		definitions[index].Params = append([]provider.ToolParam(nil), definition.Params...)
	}
	return definitions
}

func (registry *Registry) Execute(call conversation.Item, imageInput int) conversation.Item {
	return registry.ExecuteWithDisplay(call, imageInput, nil).Result
}

func (registry *Registry) ExecuteWithDisplay(call conversation.Item, imageInput int, display DisplaySink) ToolExecution {
	return registry.ExecuteWithDisplayContext(context.Background(), call, imageInput, display)
}

// ExecuteWithDisplayContext runs a tool with cancellation attached to its foreground work.
func (registry *Registry) ExecuteWithDisplayContext(ctx context.Context, call conversation.Item, imageInput int,
	display DisplaySink) ToolExecution {
	if ctx == nil {
		ctx = context.Background()
	}
	toolName := stringValue(call.ToolName)
	arguments := stringValue(call.Arguments)
	if toolName == "bash" && registry != nil {
		arguments = preprocessBashArguments(arguments, registry.bashWorkingDirectory(), os.Getenv("HOME"))
	}
	if toolName == "read" || toolName == "write" || toolName == "edit" {
		arguments = relativizeToolPath(arguments)
	}
	var displayed atomic.Bool
	if display != nil {
		sink := display
		display = func(data []byte) error {
			if len(data) > 0 {
				displayed.Store(true)
			}
			return sink(data)
		}
	}
	var result Result
	if registry == nil {
		result = Result{Text: "error: tool registry is unavailable"}
	} else {
		switch toolName {
		case "bash":
			result = registry.runBashContext(ctx, arguments, imageInput, display)
		case "task_wait":
			result = registry.runTaskWaitContext(ctx, arguments, imageInput, display)
		case "web_fetch":
			result = registry.runWebFetchContext(ctx, arguments)
		case "web_search":
			result = registry.runWebSearchContext(ctx, arguments)
		default:
			result = registry.RunWithDisplay(toolName, arguments, imageInput, display)
		}
	}
	item := conversation.Item{
		Kind: conversation.KindToolResult, CallID: cloneString(call.CallID),
		ToolName: cloneString(call.ToolName), Output: conversation.String(result.Text), Images: result.Images,
	}
	summarizesDisplay := result.OutputSummarizesDisplay || displayed.Load()
	var fallbackOutput *string
	if summarizesDisplay {
		fallbackOutput = conversation.String(result.Text)
	}
	return ToolExecution{
		Result: item, Presentation: registry.Presentation(call),
		OutputSummarizesDisplay: summarizesDisplay, FallbackOutput: fallbackOutput, HiddenTail: result.HiddenTail,
	}
}

func (registry *Registry) Run(name, arguments string, imageInput int) Result {
	return registry.RunWithDisplay(name, arguments, imageInput, nil)
}

func (registry *Registry) RunWithDisplay(name, arguments string, imageInput int, display DisplaySink) Result {
	if registry == nil {
		return Result{Text: "error: tool registry is unavailable"}
	}
	var run func(string, int, DisplaySink) Result
	switch name {
	case "read":
		run = registry.runRead
	case "write":
		run = registry.runWrite
	case "edit":
		run = registry.runEdit
	case "bash":
		run = registry.runBash
	case "task_wait":
		run = registry.runTaskWait
	case "web_fetch":
		run = registry.runWebFetch
	case "web_search":
		run = registry.runWebSearch
	default:
		return Result{Text: fmt.Sprintf("error: unknown tool %q", name)}
	}
	return run(arguments, imageInput, display)
}

// Presentation resolves upstream tool-display metadata from the same preprocessed arguments used
// for execution while leaving the original conversation item untouched.
func (registry *Registry) Presentation(call conversation.Item) ToolPresentation {
	name := stringValue(call.ToolName)
	arguments := stringValue(call.Arguments)
	switch name {
	case "bash":
		arguments = preprocessBashArguments(arguments, registry.bashWorkingDirectory(), os.Getenv("HOME"))
	case "read", "write", "edit":
		arguments = relativizeToolPath(arguments)
	}
	object, _ := parseArguments(arguments)
	presentation := ToolPresentation{}
	argument := func(key string) (string, bool) {
		if object == nil {
			return arguments, false
		}
		value, ok := object.string(key)
		if !ok {
			return arguments, false
		}
		return value, true
	}
	switch name {
	case "read":
		path, present := argument("path")
		presentation.Argument = path
		presentation.HasArgument = present || arguments != ""
		presentation.RawArgument = !present && arguments != ""
		if present {
			presentation.CollapsedArgument = collapseDisplayPath(path)
		}
		if present {
			presentation.Extra = readDisplayRange(object)
		}
		presentation.PreviewMode = PreviewCollapsed
	case "write":
		presentation.Argument, presentation.HasArgument = argument("path")
		presentation.RawArgument = !presentation.HasArgument && arguments != ""
		presentation.HasArgument = presentation.HasArgument || presentation.RawArgument
		presentation.OutputStyle = OutputUnifiedDiff
	case "edit":
		presentation.Argument, presentation.HasArgument = argument("path")
		presentation.RawArgument = !presentation.HasArgument && arguments != ""
		presentation.HasArgument = presentation.HasArgument || presentation.RawArgument
		presentation.OutputStyle = OutputUnifiedDiff
	case "bash":
		presentation.Argument, presentation.HasArgument = argument("command")
		presentation.RawArgument = !presentation.HasArgument && arguments != ""
		presentation.HasArgument = presentation.HasArgument || presentation.RawArgument
		presentation.PreviewMode = PreviewHeadTail
		presentation.HeaderRows = 3
		if isExplorationBash(presentation.Argument) {
			presentation.PreviewMode = PreviewCollapsed
		}
	case "task_wait":
		presentation.Argument = formatTaskWaitArgument(object, arguments)
		id, validID := argument("id")
		validID = validID && id != ""
		presentation.RawArgument = !validID && arguments != ""
		presentation.HasArgument = validID || presentation.RawArgument
		presentation.PreviewMode = PreviewHeadTail
	case "web_fetch", "web_search":
		key := "url"
		if name == "web_search" {
			key = "query"
		}
		presentation.Argument, presentation.HasArgument = argument(key)
		presentation.RawArgument = !presentation.HasArgument && arguments != ""
		presentation.HasArgument = presentation.HasArgument || presentation.RawArgument
		presentation.PreviewMode = PreviewHeadTail
	}
	if !presentation.HasArgument && arguments != "" {
		presentation.Argument = arguments
		presentation.HasArgument = true
		presentation.RawArgument = true
	}
	return presentation
}

func readDisplayRange(object argumentObject) string {
	if object == nil {
		return ""
	}
	path, _ := object.string("path")
	if isImagePath(path) {
		return ""
	}
	_, hasOffset := object["offset"]
	_, hasLimit := object["limit"]
	if !hasOffset && !hasLimit {
		return ""
	}
	offset := int64(1)
	if raw, ok := object["offset"]; ok {
		if value, err := parseIntegerRaw(raw); err == nil {
			offset = value
		}
	}
	if raw, ok := object["limit"]; ok {
		if limit, err := parseIntegerRaw(raw); err == nil {
			if limit < 1 {
				return fmt.Sprintf(":%d-", offset)
			}
			if offset > int64(^uint64(0)>>1)-limit+1 {
				return fmt.Sprintf(":%d-%d", offset, int64(^uint64(0)>>1))
			}
			return fmt.Sprintf(":%d-%d", offset, offset+limit-1)
		}
	}
	return fmt.Sprintf(":%d-", offset)
}

func formatTaskWaitArgument(object argumentObject, raw string) string {
	if object == nil {
		return raw
	}
	id, ok := object.string("id")
	if !ok || id == "" {
		return raw
	}
	result := id
	kill := object.boolean("kill")
	if rawTimeout, ok := object["timeout_seconds"]; ok {
		if seconds, err := parseIntegerRaw(rawTimeout); err == nil && seconds > 0 {
			duration := displayDuration(time.Duration(seconds) * time.Second)
			if kill {
				return result + " (up to " + duration + ", then kill)"
			}
			return result + " (up to " + duration + ")"
		}
	}
	if kill {
		result += " (kill)"
	}
	return result
}

func displayDuration(duration time.Duration) string {
	seconds := int64(0)
	if duration > 0 {
		seconds = int64((duration + 500*time.Millisecond) / time.Second)
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600 && seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dh %02dm", seconds/3600, seconds%3600/60)
	}
}

func collapseDisplayPath(path string) string {
	if path == "" {
		return "?"
	}
	slash := strings.LastIndexByte(path, '/')
	if slash < 0 || slash == len(path)-1 {
		return path
	}
	base := path[slash+1:]
	if !isGenericDisplayBasename(base) {
		return base
	}
	parentStart := strings.LastIndexByte(path[:slash], '/') + 1
	if parentStart == slash {
		return base
	}
	if parentStart == 0 || parentStart == 1 && path[0] == '/' {
		return path
	}
	return ".../" + path[parentStart:]
}

func isGenericDisplayBasename(name string) bool {
	if name == "" || name[0] == '.' {
		return true
	}
	stemEnd := strings.IndexByte(name, '.')
	if stemEnd < 0 {
		stemEnd = len(name)
	}
	stem := name[:stemEnd]
	hasUpper, allUpper := false, true
	for index := 0; index < len(stem); index++ {
		char := stem[index]
		if char >= 'a' && char <= 'z' {
			allUpper = false
		}
		if char >= 'A' && char <= 'Z' {
			hasUpper = true
		}
	}
	if hasUpper && allUpper || displayStemEquals(name, "index") || displayStemEquals(name, "main") {
		return true
	}
	_, ok := genericDisplayBasenames[strings.ToLower(name)]
	return ok
}

func displayStemEquals(name, stem string) bool {
	return len(name) >= len(stem) && strings.EqualFold(name[:len(stem)], stem) &&
		(len(name) == len(stem) || name[len(stem)] == '.')
}

var genericDisplayBasenames = map[string]struct{}{
	"androidmanifest.xml": {}, "build.gradle": {}, "build.gradle.kts": {}, "build.rs": {}, "build.zig": {},
	"cargo.toml": {}, "chart.yaml": {}, "cmakelists.txt": {}, "compose.yaml": {}, "compose.yml": {},
	"composer.json": {}, "configure.ac": {}, "conftest.py": {}, "default.nix": {}, "docker-compose.yaml": {},
	"docker-compose.yml": {}, "dockerfile": {}, "flake.nix": {}, "gemfile": {}, "gnumakefile": {},
	"go.mod": {}, "go.sum": {}, "__init__.py": {}, "jenkinsfile": {}, "justfile": {}, "kbuild": {},
	"kconfig": {}, "kustomization.yaml": {}, "lib.rs": {}, "__main__.py": {}, "makefile": {},
	"manifest.json": {}, "meson.build": {}, "mix.exs": {}, "mod.rs": {}, "outputs.tf": {},
	"package.swift": {}, "package.json": {}, "podfile": {}, "pom.xml": {}, "project.pbxproj": {},
	"pyproject.toml": {}, "rakefile": {}, "requirements.txt": {}, "settings.gradle": {},
	"settings.gradle.kts": {}, "setup.py": {}, "shell.nix": {}, "tsconfig.json": {},
	"values.yaml": {}, "variables.tf": {}, "versions.tf": {},
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func builtInDefinitions(noTasks, noWeb bool) []provider.ToolDefinition {
	definitions := []provider.ToolDefinition{
		{
			Name:        "read",
			Description: "Read a file from disk and return its contents in `cat -n` style: each line is prefixed with its 1-indexed line number, a → arrow, then the line's content. The prefix is presentation only — it is NOT part of the file on disk; do not include it in `edit` tool `old_string`/`new_string` arguments. Optional 1-indexed line `offset` and `limit` slice a range; without them, the whole file is returned. Image files (PNG/JPEG/GIF/WebP) are detected by content and attached to the result as images when the model supports image input.",
			Params: []provider.ToolParam{
				{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
				{Name: "offset", Type: "integer", Minimum: 1, Description: "1-indexed first line to return. Default 1."},
				{Name: "limit", Type: "integer", Minimum: 1, Description: "Maximum number of lines to return. Default: to EOF."},
			},
		},
		{
			Name:        "edit",
			Description: "Replace an exact string in a file. The `old_string` must match a byte sequence in the file exactly once unless `replace_all` is true. The `read` tool prefixes each line with a line number and a → arrow for display; that prefix is NOT part of the file on disk, so do not include it in `old_string` or `new_string`. Returns a unified diff of the change.",
			Params: []provider.ToolParam{
				{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
				{Name: "old_string", Type: "string", Required: true, Description: "Exact text to find. Must be unique unless replace_all is set."},
				{Name: "new_string", Type: "string", Required: true, Description: "Replacement text."},
				{Name: "replace_all", Type: "boolean", Description: "Replace every occurrence instead of requiring uniqueness."},
			},
		},
		{
			Name:        "write",
			Description: "Write a file, replacing it entirely (creating it if needed). Parent directories are created automatically.",
			Params: []provider.ToolParam{
				{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
				{Name: "content", Type: "string", Required: true, Description: "Full new contents of the file."},
			},
		},
	}
	definitions = append(definitions, bashDefinition(noTasks))
	if !noTasks {
		definitions = append(definitions, taskWaitDefinition())
	}
	if !noWeb {
		definitions = append(definitions, webFetchDefinition(), webSearchDefinition())
	}
	return definitions
}

func bashDefinition(noTasks bool) provider.ToolDefinition {
	common := "Run a shell command via bash -c (POSIX sh -c where bash is unavailable). Returns combined stdout+stderr plus exit code. Prefer the native read/edit/write tools for ordinary file reads and edits.\n\nRules:\n- Each call starts in the working directory listed under `# Environment`; `cd` does not persist across calls.\n- Follow the command preferences under `# Environment` when present.\n"
	if noTasks {
		return provider.ToolDefinition{
			Name: "bash", Description: common + "- Default timeout is 120s; pass `timeout_seconds` for slow commands (test suites, builds). The harness enforces a hard ceiling.",
			Params: []provider.ToolParam{
				{Name: "command", Type: "string", Required: true, Description: "Shell command to run."},
				{Name: "timeout_seconds", Type: "integer", Minimum: 1, Description: "Optional override of the default timeout. Use a higher value for slow builds or test suites; the harness clamps to a configured maximum."},
			},
		}
	}
	return provider.ToolDefinition{
		Name: "bash", Description: common + "- Usually omit `timeout_seconds`: a command that outlives the default timeout (120s) is not killed — it detaches into a background task and you will be notified when it finishes.\n- Prefer synchronous calls when you need the result before doing anything else, including builds and tests. Use `background` for work you can overlap with other work or persistent processes such as servers and watchers: the call returns after a brief initial-output window and the command continues as a task. If you would immediately wait for it, leaving `background` unset is simpler. No trailing `&`: the task tracks the shell, and processes orphaned by an exited shell are killed.",
		Params: []provider.ToolParam{
			{Name: "command", Type: "string", Required: true, Description: "Shell command to run."},
			{Name: "timeout_seconds", Type: "integer", Minimum: 1, Description: "Optional override of the default timeout; rarely needed — on expiry the command detaches into a background task instead of dying. The harness clamps to a configured maximum."},
			{Name: "background", Type: "boolean", Description: "Run as a background task; usually leave unset if you would wait immediately. Returns after a brief initial-output window while the command keeps running; `timeout_seconds` is ignored. A command that finishes within the window returns synchronously and creates no task."},
			{Name: "name", Type: "string", Description: "Optional short task name used instead of the automatic id if the command detaches (letters/digits/-/_, max 32 chars, e.g. \"tests\")."},
		},
	}
}

func taskWaitDefinition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Name:        "task_wait",
		Description: "Wait on one background task; returns the output it produced since you last saw it plus its status.\n\nReturns immediately for an already-finished task (this is also how you collect a task announced as finished), and returns early when a different task finishes so you can react to it. Wait on the task whose result you need next; do not poll in a loop of short waits.\n\nWith `kill`, the task is stopped (SIGTERM, then SIGKILL after a grace period) and its final output is returned; add `timeout_seconds` to first give the task that long to finish on its own.",
		Params: []provider.ToolParam{
			{Name: "id", Type: "string", Required: true, Description: "Task id to wait on (e.g. \"t1\")."},
			{Name: "timeout_seconds", Type: "integer", Description: "How long to block waiting for the task to finish; 0 does not block. Defaults to a configured value (10 minutes unless changed); with `kill` it defaults to 0 (kill immediately)."},
			{Name: "kill", Type: "boolean", Description: "Kill the task and report its final output. With `timeout_seconds`, the task first gets that window to finish on its own; the kill fires only if it is still running when the timeout elapses."},
		},
	}
}

type argumentObject map[string]json.RawMessage

func parseArguments(arguments string) (argumentObject, string) {
	if arguments == "" {
		arguments = "{}"
	}
	var object argumentObject
	if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("arguments must be a JSON object")
		}
		return nil, "invalid arguments: " + err.Error()
	}
	return object, ""
}

func (object argumentObject) string(name string) (string, bool) {
	raw, ok := object[name]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func (object argumentObject) integer(name string) (int64, bool, string) {
	raw, ok := object[name]
	if !ok {
		return 0, false, ""
	}
	value, err := parseIntegerRaw(raw)
	if err != nil {
		return 0, true, fmt.Sprintf("'%s' must be an integer", name)
	}
	if value < 1 {
		return 0, true, fmt.Sprintf("'%s' must be >= 1", name)
	}
	return value, true, ""
}

func parseIntegerRaw(raw json.RawMessage) (int64, error) {
	valueText := strings.TrimSpace(string(raw))
	if valueText == "" || strings.ContainsAny(valueText, ".eE") || (valueText[0] != '-' && (valueText[0] < '0' || valueText[0] > '9')) {
		return 0, fmt.Errorf("not an integer")
	}
	value, err := strconv.ParseInt(valueText, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func (object argumentObject) boolean(name string) bool {
	var value bool
	return json.Unmarshal(object[name], &value) == nil && value
}

func (object argumentObject) boolValue(name string) (bool, bool, string) {
	raw, ok := object[name]
	if !ok {
		return false, false, ""
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, fmt.Sprintf("'%s' must be a boolean", name)
	}
	return value, true, ""
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
)

func TestBashDefinitionsExposeTaskFields(t *testing.T) {
	registry := New(Options{})
	definitions := registry.Definitions()
	if len(definitions) != 7 {
		t.Fatalf("default tools = %d, want read/write/edit/bash/task_wait/web_fetch/web_search", len(definitions))
	}
	if definitions[3].Name != "bash" || definitions[4].Name != "task_wait" {
		t.Fatalf("shell tool definitions = %q, %q", definitions[3].Name, definitions[4].Name)
	}
	noTasks := New(Options{NoTasks: true}).Definitions()
	if len(noTasks) != 6 || noTasks[3].Name != "bash" || len(noTasks[3].Params) != 2 {
		t.Fatalf("no-tasks tool definitions = %#v", noTasks)
	}
}

func TestNewFromConfigPreservesExplicitZeroDurations(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"no_tasks":true,"tool_output_cap":"1k","bash.timeout":"0","bash.timeout_max":"0","bash.timeout_grace":"0","bash.background_yield":"0","task.wait_timeout":"0","task.max_running":4}`)); err != nil {
		t.Fatal(err)
	}
	registry := NewFromConfig(store)
	defer registry.Close()
	if registry.outputCap != 1024 || !registry.options.NoTasks || registry.options.BashTimeout != 0 ||
		registry.options.BashTimeoutMax != 0 || registry.options.BashTimeoutGrace != 0 ||
		registry.options.BackgroundYield != 0 || registry.options.TaskWaitTimeout != 0 || registry.options.TaskMaxRunning != 4 {
		t.Fatalf("config values were not preserved: cap=%d options=%+v", registry.outputCap, registry.options)
	}
	if len(registry.Definitions()) != 6 {
		t.Fatalf("no_tasks config still advertised task_wait: %#v", registry.Definitions())
	}
}

func TestSetDiagnosticWriterWarnsForInvalidConfiguredShellOnce(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	configured := filepath.Join(t.TempDir(), "missing-shell")
	store.SetOverride("bash.shell", &configured)
	registry := NewFromConfig(store)
	defer registry.Close()
	var stderr bytes.Buffer
	registry.SetDiagnosticWriter(&stderr)
	registry.SetDiagnosticWriter(&stderr)
	registry.ResolveShell()
	registry.ResolveShell()
	want := "gax: bash.shell: '" + configured + "' not found or not executable; using default\n"
	if stderr.String() != want {
		t.Fatalf("configured-shell warning = %q, want %q", stderr.String(), want)
	}
}

func TestNewFromConfigUsesLiveBashTimeout(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	noTasks, initialTimeout, grace := "on", "25ms", "0"
	store.SetOverride("no_tasks", &noTasks)
	store.SetOverride("bash.timeout", &initialTimeout)
	store.SetOverride("bash.timeout_grace", &grace)
	registry := NewFromConfig(store)
	defer registry.Close()
	liveTimeout := "1s"
	store.SetOverride("bash.timeout", &liveTimeout)

	result := registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 0.08; printf live"}), 1)
	if !strings.Contains(result.Text, "live") || strings.Contains(result.Text, "timed out") {
		t.Fatalf("command used stale bash.timeout: %q", result.Text)
	}
}

func TestNewFromConfigUsesLiveTaskLimit(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	initialLimit, yield := "1", "1ms"
	store.SetOverride("task.max_running", &initialLimit)
	store.SetOverride("bash.background_yield", &yield)
	registry := NewFromConfig(store)
	defer registry.Close()

	first := registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 1", "background": true}), 1)
	if !strings.Contains(first.Text, "detached as task t1") {
		t.Fatalf("first background task did not detach: %q", first.Text)
	}
	updatedLimit := "2"
	store.SetOverride("task.max_running", &updatedLimit)
	second := registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 1", "background": true}), 1)
	if !strings.Contains(second.Text, "detached as task t2") {
		t.Fatalf("second background task did not use the live task limit: %q", second.Text)
	}
}

func TestBashCombinesOutputAndReportsExitStatus(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "printf 'out'; printf 'err' >&2; exit 7"}), 1)
	if !strings.Contains(result.Text, "out") || !strings.Contains(result.Text, "err") || !strings.Contains(result.Text, "[exit 7]") {
		t.Fatalf("bash result = %q", result.Text)
	}
}

func TestExecuteWithDisplayStreamsBashAndTaskWaitOutput(t *testing.T) {
	registry := New(Options{
		UseExactValues: true, BashTimeout: 2 * time.Second, BashTimeoutMax: 2 * time.Second,
		BackgroundYield: 20 * time.Millisecond, TaskWaitTimeout: 2 * time.Second,
	})
	defer registry.Close()
	call := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("bash-1"),
		ToolName: conversation.String("bash"), Arguments: conversation.String(jsonArgs(map[string]any{"command": "printf 'live-output'; exit 7"})),
	}
	var bashOutput strings.Builder
	execution := registry.ExecuteWithDisplay(call, 1, func(data []byte) error {
		bashOutput.Write(data)
		return nil
	})
	if got, want := bashOutput.String(), "live-output\n[exit 7]"; got != want {
		t.Fatalf("streamed Bash output = %q, want %q", got, want)
	}
	if execution.Result.Output == nil || !strings.Contains(*execution.Result.Output, "live-output") || !execution.OutputSummarizesDisplay {
		t.Fatalf("Bash execution = %#v", execution)
	}

	background := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("bash-2"),
		ToolName: conversation.String("bash"), Arguments: conversation.String(jsonArgs(map[string]any{
			"command": "sleep 0.1; printf 'task-output'", "background": true, "name": "display_task",
		})),
	}
	if started := registry.ExecuteWithDisplay(background, 1, func([]byte) error { return nil }); started.Result.Output == nil || !strings.Contains(*started.Result.Output, "detached as task display_task") {
		t.Fatalf("background Bash result = %#v", started.Result)
	}
	waitCall := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("wait-1"),
		ToolName: conversation.String("task_wait"), Arguments: conversation.String(jsonArgs(map[string]any{"id": "display_task", "timeout_seconds": 2})),
	}
	var taskOutput strings.Builder
	waited := registry.ExecuteWithDisplay(waitCall, 1, func(data []byte) error {
		taskOutput.Write(data)
		return nil
	})
	if got := taskOutput.String(); !strings.Contains(got, "task-output") {
		t.Fatalf("streamed task_wait output = %q", got)
	}
	if !strings.Contains(taskOutput.String(), "[display_task finished") {
		t.Fatalf("task_wait footer was not streamed: %q", taskOutput.String())
	}
	if waited.Result.Output == nil || !strings.Contains(*waited.Result.Output, "task-output") || !waited.OutputSummarizesDisplay {
		t.Fatalf("task_wait execution = %#v", waited)
	}
}

func TestBashTruncationKeepsFullOutputAvailableUntilRegistryCloses(t *testing.T) {
	registry := New(Options{})
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "echo HEAD; seq 1 20000; echo TAIL",
	}), 1)
	if !strings.Contains(result.Text, "HEAD") || !strings.Contains(result.Text, "TAIL") {
		t.Fatalf("truncated output did not keep its head and tail: %q", result.Text[:min(len(result.Text), 256)])
	}
	marker := strings.Index(result.Text, "[output truncated")
	if marker < 0 || strings.Index(result.Text, "HEAD") > marker || strings.Index(result.Text, "TAIL") < marker {
		t.Fatalf("truncation marker is not between the retained head and tail: %q", result.Text[:min(len(result.Text), 512)])
	}
	pathStart := strings.Index(result.Text, "full output saved to ")
	if pathStart < 0 {
		t.Fatalf("truncation marker did not expose the full output log path: %q", result.Text[marker:])
	}
	pathStart += len("full output saved to ")
	pathEnd := strings.IndexByte(result.Text[pathStart:], ']')
	if pathEnd < 0 {
		t.Fatalf("truncation marker did not close after the log path: %q", result.Text[marker:])
	}
	path := result.Text[pathStart : pathStart+pathEnd]
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saved output log is unavailable before registry close: %v", err)
	}
	if !strings.HasPrefix(string(content), "HEAD\n1\n") || !strings.HasSuffix(string(content), "20000\nTAIL\n") {
		t.Fatalf("saved output log does not contain the complete command output: prefix=%q suffix=%q", content[:min(len(content), 16)], content[max(0, len(content)-16):])
	}
	registry.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("saved output log survived registry close, stat err=%v", err)
	}
}

func TestBashTruncationReportsLongLineByteGap(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "printf 'SUMMARY\\n'; printf 'x%.0s' {1..60000}",
	}), 1)
	head := strings.Index(result.Text, "SUMMARY")
	marker := strings.Index(result.Text, "[output truncated")
	if head < 0 || marker < 0 || head > marker || !strings.Contains(result.Text, "mid-line; full output saved to ") {
		t.Fatalf("long-line truncation summary = %q", result.Text[:min(len(result.Text), 1024)])
	}
}

func TestBashTruncationFallsBackToFullTailWhenHeadHasNoLine(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "printf 'X%.0s' {1..8000}; echo; seq 1 5000",
	}), 1)
	if !strings.Contains(result.Text, "last 2000 of 5001 lines") || !strings.Contains(result.Text, "full output saved to ") {
		t.Fatalf("tail-only truncation summary = %q", result.Text[:min(len(result.Text), 1024)])
	}
	if strings.Contains(result.Text, "last 1750 of") || !strings.Contains(result.Text, "5000\n") {
		t.Fatalf("tail-only fallback did not spend the full line budget: %q", result.Text[:min(len(result.Text), 1024)])
	}
}

func TestTaskWaitUsesIncrementalGapMarkerAndKeepsLog(t *testing.T) {
	registry := New(Options{BackgroundYield: 10 * time.Millisecond, TaskWaitTimeout: time.Second})
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 0.05; seq 1 10000", "background": true, "name": "large",
	}), 1)
	if !strings.Contains(result.Text, "detached as task large") {
		registry.Close()
		t.Fatalf("large task launch result = %q", result.Text)
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "large", "timeout_seconds": 2}), 1)
	if !strings.Contains(result.Text, "[output truncated: omitted ") || !strings.Contains(result.Text, "; full output: ") {
		registry.Close()
		t.Fatalf("task wait truncation marker = %q", result.Text[:min(len(result.Text), 1024)])
	}
	pathStart := strings.Index(result.Text, "; full output: ") + len("; full output: ")
	pathEnd := strings.IndexByte(result.Text[pathStart:], ']')
	if pathEnd < 0 {
		registry.Close()
		t.Fatalf("task wait marker did not close after the log path: %q", result.Text[:min(len(result.Text), 1024)])
	}
	path := result.Text[pathStart : pathStart+pathEnd]
	if _, err := os.Stat(path); err != nil {
		registry.Close()
		t.Fatalf("task wait log is unavailable before registry close: %v", err)
	}
	registry.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("task wait log survived registry close, stat err=%v", err)
	}
}

func TestStripBashCDPrefixOnlyWhenItIsProvablyTheCurrentDirectory(t *testing.T) {
	tests := []struct {
		name, command, cwd, home, want string
	}{
		{"absolute path", "cd /Users/me/proj && rg foo", "/Users/me/proj", "/Users/me", "rg foo"},
		{"trailing slash and whitespace", "  cd /Users/me/proj/  &&  rg foo", "/Users/me/proj", "/Users/me", "rg foo"},
		{"tilde", "cd ~/proj && rg foo", "/Users/me/proj", "/Users/me", "rg foo"},
		{"home variable", "cd ${HOME}/proj && rg foo", "/Users/me/proj", "/Users/me", "rg foo"},
		{"dot", "cd \".\" && pwd", "/Users/me/proj", "/Users/me", "pwd"},
		{"tight separator", "cd /Users/me/proj&&rg foo", "/Users/me/proj", "/Users/me", "rg foo"},
		{"different directory", "cd /elsewhere && rg foo", "/Users/me/proj", "/Users/me", ""},
		{"relative path", "cd proj && rg foo", "/Users/me", "/Users/me", ""},
		{"quoted tilde is literal", "cd '~/proj' && rg foo", "/Users/me/proj", "/Users/me", ""},
		{"command substitution", "cd $(pwd) && rg foo", "/Users/me/proj", "/Users/me", ""},
		{"glob", "cd /Users/me/pro* && rg foo", "/Users/me/proj", "/Users/me", ""},
		{"split home variable", "cd $HOME/proj && rg foo", "/tmp/a b/proj", "/tmp/a b", ""},
		{"quoted home with spaces", "cd \"$HOME/proj\" && rg foo", "/tmp/a b/proj", "/tmp/a b", "rg foo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offset := stripBashCDPrefix(test.command, test.cwd, test.home)
			if test.want == "" {
				if offset != 0 {
					t.Fatalf("unexpectedly stripped to %q", test.command[offset:])
				}
				return
			}
			if offset == 0 || test.command[offset:] != test.want {
				t.Fatalf("stripped suffix = %q at offset %d, want %q", test.command[offset:], offset, test.want)
			}
		})
	}
}

func TestPreprocessBashArgumentsRewritesOnlyTheCommand(t *testing.T) {
	original := `{"command":"cd /Users/me/proj && rg foo","background":true,"name":"scan"}`
	rewritten := preprocessBashArguments(original, "/Users/me/proj", "/Users/me")
	object, errorText := parseArguments(rewritten)
	if errorText != "" {
		t.Fatalf("rewritten arguments are invalid: %s", errorText)
	}
	command, ok := object.string("command")
	if !ok || command != "rg foo" {
		t.Fatalf("rewritten command = %q, want %q", command, "rg foo")
	}
	background, _, _ := object.boolValue("background")
	name, _ := object.string("name")
	if !background || name != "scan" {
		t.Fatalf("preprocessor changed unrelated arguments: %s", rewritten)
	}
	unchanged := `{"command":"cd /elsewhere && rg foo"}`
	if got := preprocessBashArguments(unchanged, "/Users/me/proj", "/Users/me"); got != unchanged {
		t.Fatalf("non-noop cd command was rewritten: %s", got)
	}
}

func TestBashBackgroundTaskCanBeCollected(t *testing.T) {
	registry := New(Options{BackgroundYield: 15 * time.Millisecond, TaskWaitTimeout: time.Second})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 0.08; printf task-output", "background": true, "name": "worker_1",
	}), 1)
	if !strings.Contains(result.Text, "detached as task worker_1") {
		t.Fatalf("launch result = %q", result.Text)
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "worker_1", "timeout_seconds": 2}), 1)
	if !strings.Contains(result.Text, "task-output") || !strings.Contains(result.Text, "finished (exit 0)") {
		t.Fatalf("task wait result = %q", result.Text)
	}
}

func TestBashTimeoutDetachesAndWaitReportsNewOutput(t *testing.T) {
	registry := New(Options{
		BashTimeout: 20 * time.Millisecond, BashTimeoutMax: time.Second,
		BashTimeoutGrace: 10 * time.Millisecond, TaskWaitTimeout: time.Second,
	})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 0.08; printf after-timeout"}), 1)
	if !strings.Contains(result.Text, "detached as task t1 after 20ms timeout") {
		t.Fatalf("timeout launch result = %q", result.Text)
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "t1", "timeout_seconds": 2}), 1)
	if !strings.Contains(result.Text, "after-timeout") || !strings.Contains(result.Text, "finished (exit 0)") {
		t.Fatalf("timeout task result = %q", result.Text)
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "t1", "timeout_seconds": 0}), 1)
	if !strings.Contains(result.Text, "no new output") || !strings.Contains(result.Text, "finished (exit 0)") {
		t.Fatalf("repeated wait result = %q", result.Text)
	}
}

func TestFinishedTaskNoteReportsPendingOutputWithoutCollectingIt(t *testing.T) {
	registry := New(Options{BackgroundYield: 10 * time.Millisecond, TaskWaitTimeout: time.Second})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 0.05; printf note-body", "background": true, "name": "note-job",
	}), 1)
	if !strings.Contains(result.Text, "detached as task note-job") {
		t.Fatalf("launch result = %q", result.Text)
	}
	task := registry.tasks.find("note-job")
	if task == nil {
		t.Fatal("detached task was not registered")
	}
	select {
	case <-task.done:
	case <-time.After(time.Second):
		t.Fatal("task did not finish in time")
	}

	note := registry.CollectTaskNotes()
	if !strings.Contains(note, "[task note-job finished (exit 0) after ") ||
		!strings.Contains(note, "; 9B output pending]") || strings.Contains(note, "note-body") {
		t.Fatalf("finished task note = %q", note)
	}
	if registry.CollectTaskNotes() != "" {
		t.Fatal("finished task note was repeated")
	}
	if task.isCollected() {
		t.Fatal("announcing pending output collected the task prematurely")
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "note-job", "timeout_seconds": 0}), 1)
	if !strings.Contains(result.Text, "note-body") || !strings.Contains(result.Text, "finished (exit 0)") {
		t.Fatalf("task wait after its note = %q", result.Text)
	}
}

func TestExitTaskNotesReportDiscardedOutputAndRunningTasks(t *testing.T) {
	registry := New(Options{BackgroundYield: 5 * time.Millisecond, TaskWaitTimeout: time.Second})
	defer registry.Close()
	for _, test := range []struct {
		name    string
		command string
	}{
		{name: "running-job", command: "sleep 30"},
		{name: "completed-job", command: "sleep 0.05; printf done"},
	} {
		result := registry.Run("bash", jsonArgs(map[string]any{
			"command": test.command, "background": true, "name": test.name,
		}), 1)
		if !strings.Contains(result.Text, "detached as task "+test.name) {
			t.Fatalf("launch %s: %q", test.name, result.Text)
		}
	}
	completed := registry.tasks.find("completed-job")
	select {
	case <-completed.done:
	case <-time.After(time.Second):
		t.Fatal("completed task did not finish in time")
	}

	note := registry.ExitTaskNotes()
	if !strings.Contains(note, "[task running-job killed at exit]") ||
		!strings.Contains(note, "[task completed-job finished (exit 0) after ") ||
		!strings.Contains(note, "; 4B output discarded]") || strings.Contains(note, "done") {
		t.Fatalf("exit task note = %q", note)
	}
}

func TestResetTasksStopsTheOldConversationAndRestartsTaskIDs(t *testing.T) {
	registry := New(Options{BackgroundYield: 5 * time.Millisecond, TaskWaitTimeout: time.Second})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 30", "background": true, "name": "old-job",
	}), 1)
	if !strings.Contains(result.Text, "detached as task old-job") {
		t.Fatalf("old task launch = %q", result.Text)
	}
	if note := registry.ExitTaskNotes(); note != "[task old-job killed at exit]" {
		t.Fatalf("old conversation task note = %q", note)
	}

	registry.ResetTasks()
	if registry.tasks.find("old-job") != nil {
		t.Fatal("old task remained registered after reset")
	}
	result = registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 0.05; printf fresh", "background": true,
	}), 1)
	if !strings.Contains(result.Text, "detached as task t1") || registry.tasks.find("t1") == nil {
		t.Fatalf("new conversation did not restart task IDs: %q", result.Text)
	}
}

func TestTaskListAndStopExposeConversationTasks(t *testing.T) {
	registry := New(Options{BashTimeoutGrace: 0, BackgroundYield: 5 * time.Millisecond})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "sleep 30", "background": true, "name": "listed-job",
	}), 1)
	if !strings.Contains(result.Text, "detached as task listed-job") {
		t.Fatalf("background launch = %q", result.Text)
	}
	tasks := registry.ListTasks()
	if len(tasks) != 1 || tasks[0].ID != "listed-job" || tasks[0].Command != "sleep 30" || !tasks[0].Running {
		t.Fatalf("listed tasks = %+v", tasks)
	}
	if stopped := registry.StopTasks([]string{"missing", "listed-job", "listed-job"}, false); stopped != 1 {
		t.Fatalf("StopTasks() = %d, want one signalled task", stopped)
	}
	tasks = registry.ListTasks()
	if len(tasks) != 1 || tasks[0].Running || tasks[0].TermSignal == 0 {
		t.Fatalf("stopped task state = %+v", tasks)
	}
}

func TestBashKillsOrphanedChildrenWhenShellExits(t *testing.T) {
	registry := New(Options{BashTimeout: time.Second})
	defer registry.Close()
	marker := filepath.Join(t.TempDir(), "orphan-survived")
	command := "(sleep 0.3; touch " + marker + ") >/dev/null 2>&1 &"
	started := time.Now()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": command}), 1)
	if time.Since(started) > 250*time.Millisecond {
		t.Fatalf("shell waited for its orphaned child: elapsed=%s result=%q", time.Since(started), result.Text)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("orphaned child survived shell exit, stat err=%v", err)
	}
}

func TestTaskWaitCanStopBackgroundProcess(t *testing.T) {
	registry := New(Options{BackgroundYield: 10 * time.Millisecond, BashTimeoutGrace: 10 * time.Millisecond})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 10", "background": true, "name": "stoppable"}), 1)
	if !strings.Contains(result.Text, "detached as task stoppable") {
		t.Fatalf("launch result = %q", result.Text)
	}
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "stoppable", "kill": true}), 1)
	if !strings.Contains(result.Text, "killed (signal") {
		t.Fatalf("kill result = %q", result.Text)
	}
}

func TestTaskWaitReturnsWhenAnotherTaskFinishes(t *testing.T) {
	registry := New(Options{BackgroundYield: 10 * time.Millisecond, BashTimeoutGrace: 10 * time.Millisecond})
	defer registry.Close()
	registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 10", "background": true, "name": "long"}), 1)
	registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 0.05; echo short-done", "background": true, "name": "short"}), 1)
	result := registry.Run("task_wait", jsonArgs(map[string]any{"id": "long", "timeout_seconds": 2, "kill": true}), 1)
	if !strings.Contains(result.Text, "another task finished") || !strings.Contains(result.Text, "not killed") {
		t.Fatalf("wait did not yield to the completed task: %q", result.Text)
	}
	registry.Run("task_wait", jsonArgs(map[string]any{"id": "long", "kill": true}), 1)
	result = registry.Run("task_wait", jsonArgs(map[string]any{"id": "short", "timeout_seconds": 0}), 1)
	if !strings.Contains(result.Text, "short-done") || !strings.Contains(result.Text, "finished (exit 0)") {
		t.Fatalf("completed sibling result = %q", result.Text)
	}
}

func TestBashPassesProviderCredentialsThroughToChildEnvironment(t *testing.T) {
	t.Setenv("GAX_OPENAI_API_KEY", "inherited-key")
	registry := New(Options{})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "printenv GAX_OPENAI_API_KEY"}), 1)
	if !strings.Contains(result.Text, "inherited-key") {
		t.Fatalf("child environment did not inherit the provider credential: %q", result.Text)
	}
}

func TestBashChildEnvironmentExportsSelectionAndSubagentDepth(t *testing.T) {
	t.Setenv("GAX_SUBAGENT_DEPTH", "2")
	t.Setenv("GAX_PROVIDER", "parent-provider")
	t.Setenv("GAX_MODEL", "parent-model")
	t.Setenv("GAX_EFFORT", "parent-effort")
	t.Setenv("GAX_PRESET", "parent-preset")
	t.Setenv("GAX_TRACE", "/tmp/parent.trace")
	t.Setenv("GAX_TRANSCRIPT", "/tmp/parent.transcript")
	t.Setenv("AI_AGENT", "parent-agent")
	registry := New(Options{})
	defer registry.Close()
	model := "m-1"
	effort := "high"
	registry.SetSelection("mock", &model, &effort)
	command := "printf 'p=%s\\nm=%s\\ne=%s\\nps=%s\\nd=%s\\ntr=%s\\ntl=%s\\nai=%s\\n' \"$GAX_PROVIDER\" \"$GAX_MODEL\" \"$GAX_EFFORT\" \"$GAX_PRESET\" \"$GAX_SUBAGENT_DEPTH\" \"$GAX_TRACE\" \"$GAX_TRANSCRIPT\" \"$AI_AGENT\""
	result := registry.Run("bash", jsonArgs(map[string]any{"command": command}), 1)
	want := "p=mock\nm=m-1\ne=high\nps=\nd=3\ntr=\ntl=\nai=gax"
	if !strings.Contains(result.Text, want) {
		t.Fatalf("child selection environment = %q, want output containing %q", result.Text, want)
	}

	registry.SetSelection("", nil, nil)
	result = registry.Run("bash", jsonArgs(map[string]any{"command": "printf 'p=%s\\nps=%s\\nd=%s' \"$GAX_PROVIDER\" \"$GAX_PRESET\" \"$GAX_SUBAGENT_DEPTH\""}), 1)
	if !strings.Contains(result.Text, "p=parent-provider\nps=parent-preset\nd=3") {
		t.Fatalf("cleared selection should pass inherited values through: %q", result.Text)
	}
}

func TestBashRejectsInvalidAndDuplicateTaskNamesBeforeLaunching(t *testing.T) {
	registry := New(Options{BackgroundYield: 10 * time.Millisecond, BashTimeoutGrace: 10 * time.Millisecond})
	defer registry.Close()
	marker := filepath.Join(t.TempDir(), "should-not-run")
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "touch " + marker, "background": true, "name": "t12"}), 1)
	if !strings.Contains(result.Text, "automatic task id") {
		t.Fatalf("reserved task name result = %q", result.Text)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran before task-name validation, stat err=%v", err)
	}
	registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 10", "background": true, "name": "duplicate"}), 1)
	result = registry.Run("bash", jsonArgs(map[string]any{"command": "true", "background": true, "name": "duplicate"}), 1)
	if !strings.Contains(result.Text, "already in use") {
		t.Fatalf("duplicate task name result = %q", result.Text)
	}
	registry.Run("task_wait", jsonArgs(map[string]any{"id": "duplicate", "kill": true}), 1)
}

func TestBashTimeoutValidationPrecedesSideEffects(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	marker := filepath.Join(t.TempDir(), "ran")
	result := registry.Run("bash", jsonArgs(map[string]any{
		"command": "touch " + marker, "timeout_seconds": 0,
	}), 1)
	if !strings.Contains(result.Text, "timeout_seconds' must be >= 1") {
		t.Fatalf("invalid timeout result = %q", result.Text)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran before timeout validation, stat err=%v", err)
	}
}

func TestBashNoTasksModeRejectsBackgroundAndKillsTimeout(t *testing.T) {
	registry := New(Options{NoTasks: true, BashTimeout: 25 * time.Millisecond, BashTimeoutGrace: 10 * time.Millisecond})
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "true", "background": true}), 1)
	if !strings.Contains(result.Text, "background tasks are disabled") {
		t.Fatalf("background rejection = %q", result.Text)
	}
	start := time.Now()
	result = registry.Run("bash", jsonArgs(map[string]any{"command": "sleep 10"}), 1)
	if !strings.Contains(result.Text, "timed out after 25ms") || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout was not enforced: elapsed=%s result=%q", time.Since(start), result.Text)
	}
}

func TestTaskWaitValidatesUnknownAndMalformedRequests(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	if result := registry.Run("task_wait", `{"id":"missing","timeout_seconds":0}`, 1); !strings.Contains(result.Text, "no such task") {
		t.Fatalf("unknown task result = %q", result.Text)
	}
	if result := registry.Run("task_wait", `{"id":"missing","kill":"yes"}`, 1); !strings.Contains(result.Text, "'kill' must be a boolean") {
		t.Fatalf("invalid kill result = %q", result.Text)
	}
}

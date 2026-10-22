package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
)

// compressRegistry returns a registry with the bash compressor enabled, the way a configured
// session builds it.
func compressRegistry(t *testing.T) *Registry {
	t.Helper()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	registry := NewFromConfig(store)
	if !registry.bashCompress() {
		t.Fatal("bash.compress defaults to on, but the registry disabled it")
	}
	return registry
}

// compressFixture builds a directory whose long listing the compressor recognizes.
func compressFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBashCompressesRecognizedCommandOutput(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := compressFixture(t)
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + "'" + filepath.ToSlash(dir) + "'"}), 1)
	if !strings.Contains(result.Text, "cmd/") || !strings.Contains(result.Text, "main.go") {
		t.Fatalf("compressed listing = %q", result.Text)
	}
	if strings.Contains(result.Text, "node_modules") {
		t.Fatalf("noise directory survived compression: %q", result.Text)
	}
	if strings.Contains(result.Text, "drwx") || strings.Contains(result.Text, "[exit") {
		t.Fatalf("compressed listing kept raw columns or an exit suffix: %q", result.Text)
	}
	if !strings.Contains(result.Text, "(1 filtered)") {
		t.Fatalf("compressed listing did not report the filtered entry: %q", result.Text)
	}
}

func TestBashCompressionIsOffByDefaultInPlainOptions(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	dir := compressFixture(t)
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + "'" + filepath.ToSlash(dir) + "'"}), 1)
	if !strings.Contains(result.Text, "drwx") {
		t.Fatalf("an unconfigured registry compressed the listing: %q", result.Text)
	}
}

func TestBashCompressionHonorsTheSetting(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"bash.compress":"off"}`)); err != nil {
		t.Fatal(err)
	}
	registry := NewFromConfig(store)
	defer registry.Close()
	dir := compressFixture(t)
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + "'" + filepath.ToSlash(dir) + "'"}), 1)
	if !strings.Contains(result.Text, "drwx") {
		t.Fatalf("bash.compress=off still compressed the listing: %q", result.Text)
	}
}

func TestBashCompressionSkipsCommandsItCannotOwn(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := compressFixture(t)
	for _, command := range []string{
		"ls -l " + "'" + filepath.ToSlash(dir) + "'" + " | head -3",
		"ls -l " + "'" + filepath.ToSlash(dir) + "'" + " > /dev/null",
		"ls -l " + "'" + filepath.ToSlash(dir) + "'" + " && true",
	} {
		result := registry.Run("bash", jsonArgs(map[string]any{"command": command}), 1)
		if strings.Contains(result.Text, "(1 filtered)") {
			t.Errorf("%q was compressed although its output is another command's: %q", command, result.Text)
		}
	}
}

func TestBashCompressionKeepsFullOutputRecoverable(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := t.TempDir()
	var builder strings.Builder
	builder.WriteString("package main\n\n")
	for index := 0; index < 400; index++ {
		builder.WriteString("// filler comment line\n")
		builder.WriteString("func f() {}\n\n")
	}
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "cat " + "'" + filepath.ToSlash(path) + "'"}), 1)
	if strings.Contains(result.Text, "// filler comment line") {
		t.Fatalf("cat kept comment-only lines: %q", result.Text[:min(len(result.Text), 256)])
	}
	marker := strings.Index(result.Text, "[full output saved to ")
	if marker < 0 {
		t.Fatalf("compressed output did not name its recovery path: %q", result.Text[max(0, len(result.Text)-256):])
	}
	start := marker + len("[full output saved to ")
	end := strings.IndexByte(result.Text[start:], ']')
	if end < 0 {
		t.Fatalf("recovery note did not close: %q", result.Text[marker:])
	}
	saved := result.Text[start : start+end]
	content, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("full output is unavailable while the registry is open: %v", err)
	}
	if string(content) != builder.String() {
		t.Fatalf("saved output is not the raw command output: %d bytes", len(content))
	}
	registry.Close()
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatalf("the recovery file survived registry close: %v", err)
	}
}

func TestBashCompressionLeavesTheLiveDisplayRaw(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := compressFixture(t)
	call := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("bash-1"),
		ToolName:  conversation.String("bash"),
		Arguments: conversation.String(jsonArgs(map[string]any{"command": "ls -l " + "'" + filepath.ToSlash(dir) + "'"})),
	}
	var displayed strings.Builder
	execution := registry.ExecuteWithDisplay(call, 1, func(data []byte) error {
		displayed.Write(data)
		return nil
	})
	if !strings.Contains(displayed.String(), "drwx") {
		t.Fatalf("the live display lost its raw listing: %q", displayed.String())
	}
	if execution.Result.Output == nil || !strings.Contains(*execution.Result.Output, "(1 filtered)") {
		t.Fatalf("the model-facing result was not compressed: %#v", execution.Result.Output)
	}
}

func TestBashRawArgumentReturnsVerbatimOutput(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := compressFixture(t)
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + "'" + filepath.ToSlash(dir) + "'", "raw": true}), 1)
	if !strings.Contains(result.Text, "drwx") {
		t.Fatalf("raw:true did not return the command's own output: %q", result.Text)
	}
	if strings.Contains(result.Text, "[full output saved to") {
		t.Fatalf("raw:true still ran the compressor: %q", result.Text)
	}
	if _, err := parseArguments(jsonArgs(map[string]any{"command": "ls", "raw": "maybe"})); err != "" {
		t.Fatalf("raw:t\"maybe\" was refused as malformed JSON instead of a type error")
	}
	mismatch := registry.Run("bash", jsonArgs(map[string]any{"command": "ls", "raw": "maybe"}), 1)
	if !strings.Contains(mismatch.Text, "must be a boolean") {
		t.Fatalf("a non-boolean raw argument was accepted: %q", mismatch.Text)
	}
}

func TestBashRawArgumentAppliesToACollectedTask(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable unavailable for subprocess fixture")
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"bash.background_yield":"10ms","task.wait_timeout":"5s"}`)); err != nil {
		t.Fatal(err)
	}
	registry := NewFromConfig(store)
	defer registry.Close()
	launch := registry.Run("bash", jsonArgs(map[string]any{
		"command": "go test github.com/luank3/gax/internal/bashcompress", "background": true,
		"raw": true, "name": "tests",
	}), 1)
	if !strings.Contains(launch.Text, "detached as task") {
		t.Fatalf("the command did not detach: %q", launch.Text)
	}
	waited := registry.Run("task_wait", jsonArgs(map[string]any{"id": "tests", "timeout_seconds": 60}), 1)
	if strings.Contains(waited.Text, "Go test:") {
		t.Fatalf("a raw task's collected output was compressed: %q", waited.Text)
	}
	if !strings.Contains(waited.Text, "ok  ") && !strings.Contains(waited.Text, "(cached)") {
		t.Fatalf("a raw task lost its own output: %q", waited.Text)
	}
}

func TestBashCompressionNamesTheFullOutputOfACollectedTask(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable unavailable for subprocess fixture")
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"bash.background_yield":"10ms","task.wait_timeout":"60s"}`)); err != nil {
		t.Fatal(err)
	}
	registry := NewFromConfig(store)
	defer registry.Close()
	launch := registry.Run("bash", jsonArgs(map[string]any{
		"command": "go test github.com/luank3/gax/internal/bashcompress", "background": true, "name": "tests",
	}), 1)
	if !strings.Contains(launch.Text, "detached as task") {
		t.Fatalf("the command did not detach: %q", launch.Text)
	}
	waited := registry.Run("task_wait", jsonArgs(map[string]any{"id": "tests", "timeout_seconds": 60}), 1)
	if !strings.Contains(waited.Text, "Go test: 1 passed in 1 packages") {
		t.Fatalf("a collected task was not compressed: %q", waited.Text)
	}
	marker := strings.Index(waited.Text, "[full output saved to ")
	if marker < 0 {
		t.Fatalf("a collected task did not name its full output: %q", waited.Text)
	}
	start := marker + len("[full output saved to ")
	end := strings.IndexByte(waited.Text[start:], ']')
	if end < 0 {
		t.Fatalf("the recovery note did not close: %q", waited.Text)
	}
	path := waited.Text[start : start+end]
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the task's full output is unavailable: %v", err)
	}
	if !strings.Contains(string(content), "bashcompress") {
		t.Fatalf("the task's saved output is not the command's own: %q", string(content)[:min(len(content), 128)])
	}
}

func TestBashCompressionLeavesFailuresAlone(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "cat /nonexistent/file.go"}), 1)
	if !strings.Contains(result.Text, "No such file") {
		t.Fatalf("a failed cat lost its error text: %q", result.Text)
	}
}

package tools

import (
	"os"
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
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + dir}), 1)
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
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + dir}), 1)
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
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "ls -l " + dir}), 1)
	if !strings.Contains(result.Text, "drwx") {
		t.Fatalf("bash.compress=off still compressed the listing: %q", result.Text)
	}
}

func TestBashCompressionSkipsCommandsItCannotOwn(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	dir := compressFixture(t)
	for _, command := range []string{
		"ls -l " + dir + " | head -3",
		"ls -l " + dir + " > /dev/null",
		"ls -l " + dir + " && true",
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
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "cat " + path}), 1)
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
		Arguments: conversation.String(jsonArgs(map[string]any{"command": "ls -l " + dir})),
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

func TestBashCompressionLeavesFailuresAlone(t *testing.T) {
	registry := compressRegistry(t)
	defer registry.Close()
	result := registry.Run("bash", jsonArgs(map[string]any{"command": "cat /nonexistent/file.go"}), 1)
	if !strings.Contains(result.Text, "No such file") {
		t.Fatalf("a failed cat lost its error text: %q", result.Text)
	}
}

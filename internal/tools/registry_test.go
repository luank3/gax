package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

func TestBuiltInToolDefinitions(t *testing.T) {
	registry := New(Options{})
	definitions := registry.Definitions()
	if len(definitions) != 8 {
		t.Fatalf("got %d definitions, want read/edit/write/bash/task_wait/web_fetch/web_search/skill", len(definitions))
	}
	for index, name := range []string{"read", "edit", "write", "bash", "task_wait", "web_fetch", "web_search", "skill"} {
		if definitions[index].Name != name {
			t.Fatalf("definition %d is %q, want %q", index, definitions[index].Name, name)
		}
	}
	readSchema := parameterSchema(definitions[0])
	if got := readSchema["required"]; !reflectDeepEqual(got, []string{"path"}) {
		t.Fatalf("read required schema = %#v", got)
	}
	readProperties := readSchema["properties"].(map[string]any)
	if got := readProperties["offset"].(map[string]any)["minimum"]; got != int64(1) {
		t.Fatalf("read offset minimum = %#v", got)
	}
	editRequired := parameterSchema(definitions[1])["required"]
	if !reflectDeepEqual(editRequired, []string{"path", "old_string", "new_string"}) {
		t.Fatalf("edit required schema = %#v", editRequired)
	}
}

func TestReadTextSlicesAndCapsOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{OutputCap: 128})
	got := registry.Run("read", jsonArgs(map[string]any{"path": path}), 1)
	want := "     1→first\n     2→second\n     3→third"
	if got.Text != want {
		t.Fatalf("whole-file read = %q, want %q", got.Text, want)
	}
	got = registry.Run("read", jsonArgs(map[string]any{"path": path, "offset": 2, "limit": 1}), 1)
	if got.Text != "     2→second\n" {
		t.Fatalf("slice read = %q", got.Text)
	}
	got = registry.Run("read", jsonArgs(map[string]any{"path": path, "offset": 4}), 1)
	if got.Text != "(file has 3 lines; offset 4 is past EOF)" {
		t.Fatalf("past-EOF read = %q", got.Text)
	}

	longPath := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(longPath, []byte(strings.Repeat("x", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry = New(Options{OutputCap: 24})
	got = registry.Run("read", jsonArgs(map[string]any{"path": longPath, "offset": 1}), 1)
	if !strings.Contains(got.Text, "[truncated at 24 bytes") {
		t.Fatalf("explicit oversized slice was not marked truncated: %q", got.Text)
	}
	got = registry.Run("read", jsonArgs(map[string]any{"path": longPath}), 1)
	if !strings.Contains(got.Text, "cap is 24") {
		t.Fatalf("unbounded oversized read = %q", got.Text)
	}
}

func TestReadServesBuiltinSkillsWithoutTheFilesystem(t *testing.T) {
	// The cap is deliberately below the document size: a built-in reference is never sliced.
	registry := New(Options{OutputCap: 128})
	got := registry.Run("read", jsonArgs(map[string]any{"path": "gax:skills/customize-gax/SKILL.md"}), 1)
	if !strings.HasPrefix(got.Text, "     1→---\n") {
		t.Fatalf("built-in read is not numbered like a file: %q", got.Text)
	}
	if !strings.Contains(got.Text, "# Customizing gax") || !strings.Contains(got.Text, "| `theme` | `GAX_THEME` |") {
		t.Fatalf("built-in read lost the document: %q", got.Text)
	}
	if got := registry.Run("read", jsonArgs(map[string]any{"path": "gax:skills/missing/SKILL.md"}), 1).Text; !strings.Contains(got, "no such built-in skill") {
		t.Fatalf("unknown built-in read = %q", got)
	}
}

func TestReadRejectsBinaryAndNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "binary.dat")
	if err := os.WriteFile(binaryPath, []byte("a\x00b"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{})
	if got := registry.Run("read", jsonArgs(map[string]any{"path": binaryPath}), 1).Text; !strings.Contains(got, "binary") {
		t.Fatalf("binary read = %q", got)
	}
	if got := registry.Run("read", jsonArgs(map[string]any{"path": dir}), 1).Text; !strings.Contains(got, "not a regular file") {
		t.Fatalf("directory read = %q", got)
	}
}

func TestReadValidationAndLongLineElision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 800)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{OutputCap: 2048})
	result := registry.Run("read", jsonArgs(map[string]any{"path": path, "offset": 1, "limit": 1}), 1)
	if !strings.Contains(result.Text, "bytes elided]") || len(result.Text) > 600 {
		t.Fatalf("long line was not elided: len=%d output=%q", len(result.Text), result.Text)
	}
	for _, args := range []string{`{"path":"` + path + `","offset":null}`, `{"path":"` + path + `","limit":1.0}`, `{"path":"` + path + `","limit":0}`} {
		result = registry.Run("read", args, 1)
		if !strings.Contains(result.Text, "must be") {
			t.Fatalf("invalid line argument %s = %q", args, result.Text)
		}
	}
}

func TestNewFromConfigUsesLiveToolOutputCap(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	initialCap, updatedCap := "1k", "4k"
	store.SetOverride("tool_output_cap", &initialCap)
	registry := NewFromConfig(store)
	defer registry.Close()
	store.SetOverride("tool_output_cap", &updatedCap)
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}
	result := registry.Run("read", jsonArgs(map[string]any{"path": path}), 1)
	if strings.Contains(result.Text, "cap is 1024") {
		t.Fatalf("read tool used stale output cap: %q", result.Text)
	}
}

func TestReadAttachesImageOnlyWhenSupported(t *testing.T) {
	// A complete 1x1 PNG.
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/nWQAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pixel.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{})
	result := registry.Run("read", jsonArgs(map[string]any{"path": path}), 1)
	if len(result.Images) != 1 || result.Images[0].MIME != "image/png" || result.Images[0].Width != 1 || result.Images[0].Height != 1 {
		t.Fatalf("image result = %#v", result)
	}
	result = registry.Run("read", jsonArgs(map[string]any{"path": path}), 0)
	if len(result.Images) != 0 || !strings.Contains(result.Text, "does not accept image input") {
		t.Fatalf("unsupported image result = %#v", result)
	}
}

func TestReadImageDetectsGrowthPastAttachmentCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growing.png")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	result := New(Options{}).readImage(path, 0, "png", 1)
	if !strings.Contains(result.Text, "file changed while reading") {
		t.Fatalf("oversized image after stale stat = %q, want file-changed diagnostic", result.Text)
	}
}

func TestWriteCreatesAndUpdatesFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new", "notes.txt")
	registry := New(Options{})
	created := registry.Run("write", jsonArgs(map[string]any{"path": path, "content": "one\ntwo\n"}), 1)
	if !strings.Contains(created.Text, "created "+path) || !strings.Contains(created.Text, "2 lines, 8 bytes") {
		t.Fatalf("create result = %q", created.Text)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "one\ntwo\n" {
		t.Fatalf("created contents = %q, err=%v", got, err)
	}
	updated := registry.Run("write", jsonArgs(map[string]any{"path": path, "content": "one\nTWO\n"}), 1)
	if !strings.Contains(updated.Text, "-two\n+TWO") {
		t.Fatalf("update diff = %q", updated.Text)
	}
	unchanged := registry.Run("write", jsonArgs(map[string]any{"path": path, "content": "one\nTWO\n"}), 1)
	if unchanged.Text != "" {
		t.Fatalf("unchanged write diff = %q", unchanged.Text)
	}
}

func TestWriteFollowsDanglingSymlinkAndRespectsPermissions(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual.txt")
	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	registry := New(Options{})
	result := registry.Run("write", jsonArgs(map[string]any{"path": link, "content": "first\n"}), 1)
	if !strings.Contains(result.Text, "created "+link) {
		t.Fatalf("write through dangling link = %q", result.Text)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("write replaced symlink: info=%v err=%v", info, err)
	}
	if err := os.Chmod(actual, 0o750); err != nil {
		t.Fatal(err)
	}
	registry.Run("write", jsonArgs(map[string]any{"path": link, "content": "second\n"}), 1)
	info, err := os.Stat(actual)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("updated mode = %v, err=%v", info, err)
	}
}

func TestEditRequiresUniqueExactMatchAndCanReplaceAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.txt")
	if err := os.WriteFile(path, []byte("foo\nfoo\nfoo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{})
	args := map[string]any{"path": path, "old_string": "foo", "new_string": "bar"}
	result := registry.Run("edit", jsonArgs(args), 1)
	if !strings.Contains(result.Text, "matches 3 places") {
		t.Fatalf("ambiguous edit = %q", result.Text)
	}
	if got, _ := os.ReadFile(path); string(got) != "foo\nfoo\nfoo\n" {
		t.Fatalf("ambiguous edit changed file to %q", got)
	}
	args["replace_all"] = true
	result = registry.Run("edit", jsonArgs(args), 1)
	if !strings.Contains(result.Text, "+bar") {
		t.Fatalf("replace-all diff = %q", result.Text)
	}
	if got, _ := os.ReadFile(path); string(got) != "bar\nbar\nbar\n" {
		t.Fatalf("replace-all contents = %q", got)
	}
}

func TestEditRejectsOversizeFilesAndPreservesNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxEditBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{})
	result := registry.Run("edit", jsonArgs(map[string]any{"path": path, "old_string": "x", "new_string": "y", "replace_all": true}), 1)
	if !strings.Contains(result.Text, "refusing to edit") {
		t.Fatalf("oversize file edit = %q", result.Text)
	}
	if err := os.WriteFile(path, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = registry.Run("edit", jsonArgs(map[string]any{"path": path, "old_string": "missing", "new_string": "changed"}), 1)
	if !strings.Contains(result.Text, "not found") {
		t.Fatalf("no-match edit = %q", result.Text)
	}
	if got, _ := os.ReadFile(path); string(got) != "unchanged\n" {
		t.Fatalf("no-match edit changed contents to %q", got)
	}
}

func TestToolRunReturnsConversationToolResult(t *testing.T) {
	registry := New(Options{})
	call := conversation.Item{Kind: conversation.KindToolCall, CallID: conversation.String("call-1"), ToolName: conversation.String("missing"), Arguments: conversation.String("{}")}
	result := registry.Execute(call, 1)
	if result.Kind != conversation.KindToolResult || result.CallID == nil || *result.CallID != "call-1" || result.Output == nil || !strings.Contains(*result.Output, "unknown tool") {
		t.Fatalf("tool result = %#v", result)
	}
}

func TestExecuteRelativizesFileToolPathsInsideWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("missing-gax-path-preprocess-test", "notes.txt")
	absolute := filepath.Join(cwd, relative)
	call := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("read-1"),
		ToolName: conversation.String("read"), Arguments: conversation.String(jsonArgs(map[string]any{"path": absolute})),
	}
	result := New(Options{}).Execute(call, 1)
	if result.Output == nil || !strings.Contains(*result.Output, relative) || strings.Contains(*result.Output, absolute) {
		t.Fatalf("execution path was not made relative to the working directory: %#v", result)
	}
	if got := stringValue(call.Arguments); !strings.Contains(got, absolute) {
		t.Fatalf("path preprocessing modified the original conversation call: %q", got)
	}
}

func TestToolPresentationResolvesUpstreamBuiltInMetadata(t *testing.T) {
	registry := New(Options{})
	cases := []struct {
		name        string
		arguments   map[string]any
		argument    string
		extra       string
		collapsed   string
		outputStyle ToolOutputStyle
		preview     ToolPreviewMode
		headerRows  int
	}{
		{"read", map[string]any{"path": "/repo/src/notes.txt", "offset": 5, "limit": 3}, "/repo/src/notes.txt", ":5-7", "notes.txt", OutputPlain, PreviewCollapsed, 0},
		{"write", map[string]any{"path": "/repo/src/notes.txt", "content": "new"}, "/repo/src/notes.txt", "", "", OutputUnifiedDiff, PreviewHead, 0},
		{"edit", map[string]any{"path": "/repo/src/notes.txt", "old_string": "old", "new_string": "new"}, "/repo/src/notes.txt", "", "", OutputUnifiedDiff, PreviewHead, 0},
		{"bash", map[string]any{"command": "find . -type f"}, "find . -type f", "", "", OutputPlain, PreviewCollapsed, 3},
		{"bash", map[string]any{"command": "cat file.txt | tee copy.txt"}, "cat file.txt | tee copy.txt", "", "", OutputPlain, PreviewHeadTail, 3},
		{"bash", map[string]any{"command": "printf ok"}, "printf ok", "", "", OutputPlain, PreviewHeadTail, 3},
		{"task_wait", map[string]any{"id": "worker_1", "timeout_seconds": 2, "kill": true}, "worker_1 (up to 2s, then kill)", "", "", OutputPlain, PreviewHeadTail, 0},
		{"web_fetch", map[string]any{"url": "https://example.com/a"}, "https://example.com/a", "", "", OutputPlain, PreviewHeadTail, 0},
		{"web_search", map[string]any{"query": "golang timeout"}, "golang timeout", "", "", OutputPlain, PreviewHeadTail, 0},
	}
	for _, test := range cases {
		call := conversation.Item{Kind: conversation.KindToolCall, ToolName: conversation.String(test.name), Arguments: conversation.String(jsonArgs(test.arguments))}
		got := registry.Presentation(call)
		if got.Argument != test.argument || got.Extra != test.extra || got.CollapsedArgument != test.collapsed ||
			got.OutputStyle != test.outputStyle || got.PreviewMode != test.preview || got.HeaderRows != test.headerRows {
			t.Errorf("Presentation(%s) = %#v", test.name, got)
		}
	}
}

func TestToolPresentationUsesPreprocessedDisplayArguments(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	registry := New(Options{})
	bashArguments := jsonArgs(map[string]any{"command": "cd " + cwd + " && printf ok"})
	bashCall := conversation.Item{ToolName: conversation.String("bash"), Arguments: conversation.String(bashArguments)}
	if got := registry.Presentation(bashCall).Argument; got != "printf ok" {
		t.Fatalf("displayed bash argument = %q, want the no-op cd prefix stripped", got)
	}
	path := filepath.Join(cwd, "src", "main.go")
	readArguments := jsonArgs(map[string]any{"path": path})
	readCall := conversation.Item{ToolName: conversation.String("read"), Arguments: conversation.String(readArguments)}
	if got := registry.Presentation(readCall).Argument; got != filepath.Join("src", "main.go") {
		t.Fatalf("displayed read path = %q, want a path relative to cwd", got)
	}
	if *bashCall.Arguments != bashArguments || *readCall.Arguments != readArguments {
		t.Fatalf("display preprocessing mutated stored calls: bash=%q read=%q", *bashCall.Arguments, *readCall.Arguments)
	}
}

func TestToolPresentationDistinguishesEmptyAndRawArguments(t *testing.T) {
	registry := New(Options{})
	empty := registry.Presentation(conversation.Item{
		ToolName:  conversation.String("read"),
		Arguments: conversation.String(`{"path":""}`),
	})
	if !empty.HasArgument || empty.RawArgument || empty.Argument != "" || empty.CollapsedArgument != "?" {
		t.Fatalf("valid empty path metadata = %#v", empty)
	}
	raw := registry.Presentation(conversation.Item{
		ToolName:  conversation.String("read"),
		Arguments: conversation.String(`{"offset":1}`),
	})
	if !raw.HasArgument || !raw.RawArgument || raw.Argument != `{"offset":1}` || raw.Extra != "" {
		t.Fatalf("missing display argument did not fall back to raw JSON: %#v", raw)
	}
	malformed := registry.Presentation(conversation.Item{
		ToolName:  conversation.String("task_wait"),
		Arguments: conversation.String(`{"id":""}`),
	})
	if !malformed.HasArgument || !malformed.RawArgument || malformed.Argument != `{"id":""}` {
		t.Fatalf("malformed formatted argument did not fall back to raw JSON: %#v", malformed)
	}
}

func TestCollapseDisplayPathMatchesUpstreamRules(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"src/render/markdown.c", "markdown.c"},
		{"agent.c", "agent.c"},
		{"/home/user/project/src/Turn.c", "Turn.c"},
		{"", "?"},
		{"src/", "src/"},
		{"skills/commit-helper/SKILL.md", ".../commit-helper/SKILL.md"},
		{"docs/guides/README.md", ".../guides/README.md"},
		{"subprojects/jansson/meson.build", ".../jansson/meson.build"},
		{"a/pkg/__init__.py", ".../pkg/__init__.py"},
		{"src/components/Button/index.tsx", ".../Button/index.tsx"},
		{"project/cmd/serve/main.go", ".../serve/main.go"},
		{"app/src/main/AndroidManifest.xml", ".../main/AndroidManifest.xml"},
		{"deep/tree/gnumakefile", ".../tree/gnumakefile"},
		{"services/api/.env", ".../api/.env"},
		{"/home/user/.claude/skills/commit/SKILL.md", ".../commit/SKILL.md"},
		{"tests/meson.build", "tests/meson.build"},
		{"README.md", "README.md"},
		{"/etc/Makefile", "/etc/Makefile"},
		{"a//SKILL.md", "SKILL.md"},
		{"src/Index.ts", "src/Index.ts"},
		{"pkg/MAIN.go", "pkg/MAIN.go"},
	}
	for _, test := range tests {
		if got := collapseDisplayPath(test.path); got != test.want {
			t.Errorf("collapseDisplayPath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestExecuteWithDisplayStreamsNewWriteButPreservesCanonicalResult(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()
	path := filepath.Join(t.TempDir(), "created.txt")
	content := "line one\nline two\n"
	call := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("write-1"),
		ToolName: conversation.String("write"), Arguments: conversation.String(jsonArgs(map[string]any{"path": path, "content": content})),
	}
	var displayed strings.Builder
	execution := registry.ExecuteWithDisplay(call, 1, func(data []byte) error {
		displayed.Write(data)
		return nil
	})
	if got := displayed.String(); got != content {
		t.Fatalf("displayed write content = %q, want %q", got, content)
	}
	if execution.Result.Kind != conversation.KindToolResult || execution.Result.CallID == nil || *execution.Result.CallID != "write-1" {
		t.Fatalf("canonical tool result = %#v", execution.Result)
	}
	if execution.Result.Output == nil || !strings.Contains(*execution.Result.Output, "created "+path) {
		t.Fatalf("canonical model result = %#v", execution.Result.Output)
	}
	if !execution.OutputSummarizesDisplay || execution.FallbackOutput == nil || *execution.FallbackOutput != *execution.Result.Output ||
		execution.HiddenTail != 0 || execution.Presentation.Argument != path {
		t.Fatalf("tool execution metadata = %#v", execution)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Fatalf("written file = %q, err = %v", got, err)
	}
}

func jsonArgs(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func parameterSchema(definition provider.ToolDefinition) map[string]any {
	return provider.BuildToolSchema(definition)
}

func reflectDeepEqual(left, right any) bool { return reflect.DeepEqual(left, right) }

func TestNilRegistryReportsUnavailable(t *testing.T) {
	var registry *Registry
	if got := registry.Run("read", jsonArgs(map[string]any{"path": "x"}), 1); got.Text != "error: tool registry is unavailable" {
		t.Fatalf("nil registry Run = %q", got.Text)
	}
	execution := registry.Execute(conversation.Item{
		Kind:      conversation.KindToolCall,
		ToolName:  conversation.String("read"),
		Arguments: conversation.String(`{"path":"x"}`),
	}, 1)
	if execution.Output == nil || *execution.Output != "error: tool registry is unavailable" {
		t.Fatalf("nil registry Execute = %v", execution.Output)
	}
}

func TestReadWholeFileIsCappedAtTheLineLimit(t *testing.T) {
	var builder strings.Builder
	for line := 0; line < maxReadLines+3; line++ {
		builder.WriteString("x\n")
	}
	path := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{OutputCap: 64 * 1024})
	got := registry.Run("read", jsonArgs(map[string]any{"path": path}), 1)
	marker := fmt.Sprintf("[truncated at %d lines; file has more — pass offset/limit to read more]", maxReadLines)
	if !strings.HasSuffix(got.Text, marker) {
		t.Fatalf("line-capped read = %q, want it to end with %q", got.Text, marker)
	}
	body := strings.TrimSuffix(strings.TrimSuffix(got.Text, marker), "\n\n")
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != maxReadLines {
		t.Fatalf("line-capped read returned %d lines, want %d", len(lines), maxReadLines)
	}
	if !strings.HasPrefix(lines[len(lines)-1], fmt.Sprintf("%6d%s", maxReadLines, readLineDelimiter)) {
		t.Fatalf("last returned line = %q, want line %d", lines[len(lines)-1], maxReadLines)
	}
}

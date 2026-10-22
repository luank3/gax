package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/cli"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/version"
)

func TestTTYPromptUsesASCIIOrUTF8LocaleGlyph(t *testing.T) {
	if got := ttyPrompt(false); got != "\x1b[1m>\x1b[22m " {
		t.Fatalf("ASCII prompt = %q", got)
	}
	if got := ttyPrompt(true); !strings.Contains(got, "❯") {
		t.Fatalf("UTF-8 prompt = %q", got)
	}
}

func TestWriteNoEffortLevelsDistinguishesPinnedMessagesProvider(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"messages":{"base_url":"https://messages.example/v1","api":"anthropic-messages","thinking_mode":"off"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("messages")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writeNoEffortLevels(&output, provider, "claude-test")
	if got, want := output.String(), "the messages provider doesn't expose reasoning-effort levels\n"; got != want {
		t.Fatalf("no-effort note = %q, want %q", got, want)
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestUnknownLeadingWordListsAvailablePresets(t *testing.T) {
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "gax")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"zeta":{"provider":"openai-compatible","model":"z"},"alpha":{"provider":"openai-compatible","model":"a"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	var stdout, stderr strings.Builder
	if code := Run([]string{"unknown"}, strings.NewReader(""), false, &stdout, &stderr); code != 1 {
		t.Fatalf("Run() code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "gax: 'unknown' is not a preset (presets: zeta, alpha)\nA prompt needs -p / --print. Try 'gax --help' for usage.\n"
	if stderr.String() != want {
		t.Fatalf("unknown leading word diagnostic = %q, want %q", stderr.String(), want)
	}
}

func TestUnknownLeadingWordWarnsOnceForInvalidPresetDefinitions(t *testing.T) {
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "gax")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"broken":"draft","typo":{"provider":"mock","modle":"x"},"valid":{"provider":"mock"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	var stdout, stderr strings.Builder
	if code := Run([]string{"unknown"}, strings.NewReader(""), false, &stdout, &stderr); code != 1 {
		t.Fatalf("Run() code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	want := "gax: preset 'broken' is not an object (define a presets.broken block) — ignoring it\n" +
		"gax: preset 'typo': 'modle' is not presettable (allowed: provider, model, effort, system_prompt, system_prompt_append, tint); endpoint settings belong in a providers.<name> block, context/recording in the --bare/--no-session flags — ignoring it\n" +
		"gax: 'unknown' is not a preset (presets: valid)\nA prompt needs -p / --print. Try 'gax --help' for usage.\n"
	if stderr.String() != want {
		t.Fatalf("invalid preset enumeration diagnostics = %q, want %q", stderr.String(), want)
	}
}

func TestRunInvalidOptionsMatchGetoptDiagnostics(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--bogus"}, want: "unrecognized option '--bogus'"},
		{args: []string{"-z"}, want: "invalid option -- 'z'"},
		{args: []string{"--model"}, want: "option '--model' requires an argument"},
		{args: []string{"--prov"}, want: "option '--provider' requires an argument"},
		{args: []string{"--json=yes"}, want: "option '--json' doesn't allow an argument"},
		{args: []string{"--pr"}, want: "option '--pr' is ambiguous; possibilities: '--print' '--provider' '--preset'"},
	} {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := Run(test.args, strings.NewReader(""), false, &stdout, &stderr); code != 1 {
				t.Fatalf("Run() code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			want := os.Args[0] + ": " + test.want + "\nTry 'gax --help' for usage.\n"
			if stderr.String() != want {
				t.Fatalf("diagnostic = %q, want %q", stderr.String(), want)
			}
		})
	}
}

func TestValidPresetNamesPreserveConfigOrderAndSkipInvalidEntries(t *testing.T) {
	store := config.New(config.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"presets":{"zeta":{"provider":"openai-compatible","model":"z"},"broken":"draft","alpha":{"provider":"openai-compatible","model":"a"}}}`)); err != nil {
		t.Fatal(err)
	}
	got := validPresetNames(store)
	want := []string{"zeta", "alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validPresetNames() = %v, want %v", got, want)
	}
}

func TestRunBareDoesNotOfferTheSkillTool(t *testing.T) {
	var offered []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		var definitions []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(body["tools"], &definitions); err != nil {
			t.Errorf("decode tools: %v", err)
		}
		for _, definition := range definitions {
			offered = append(offered, definition.Function.Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--bare", "-p", "--provider=openai-compatible", "--model=test-model", "hi"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || stdout.String() != "answer\n" {
		t.Fatalf("bare run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(offered) != 7 || slices.Contains(offered, "skill") {
		t.Fatalf("--bare offered %v", offered)
	}
}

func TestRunOneShotStreamsAnswerThroughConfiguredProviderAndTools(t *testing.T) {
	var requestSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen.Store(true)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		if model != "test-model" {
			t.Errorf("request model = %q", model)
		}
		var tools []json.RawMessage
		if err := json.Unmarshal(body["tools"], &tools); err != nil || len(tools) != 8 {
			t.Errorf("request tools = %d, err=%v", len(tools), err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	tracePath := filepath.Join(t.TempDir(), "wire.md")
	t.Setenv("GAX_TRACE", tracePath)
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=openai-compatible", "--model=test-model", "say", "hello"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || stdout.String() != "answer\n" ||
		stderr.String() != "gax: openai-compatible · test-model\n\n" || !requestSeen.Load() {
		t.Fatalf("run result: code=%d stdout=%q stderr=%q requestSeen=%t", code, stdout.String(), stderr.String(), requestSeen.Load())
	}
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), "test-key") || !strings.Contains(string(trace), "Authorization: <redacted>") ||
		!strings.Contains(string(trace), "### event: (unnamed)") || !strings.Contains(string(trace), "**HTTP 200**") {
		t.Fatalf("wire trace is missing traffic or leaked the provider key: %s", trace)
	}
}

func TestRunOneShotUsesScriptedMockProviderAndDefaultModel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mock.txt")
	if err := os.WriteFile(path, []byte("text mock answer\nunknown directive\nend-turn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("GAX_MOCK_SCRIPT", path)
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=mock", "say hello"}, strings.NewReader(""), false, &stdout, &stderr)
	if code != 0 || stdout.String() != "mock answer\n" ||
		stderr.String() != "gax: mock · mock-model\n\ngax mock: unknown directive: unknown directive\n" {
		t.Fatalf("mock one-shot result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunJSONRecordsInternalProviderWhenNoSessionIsZero(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "mock.txt")
	if err := os.WriteFile(scriptPath, []byte("text recorded mock answer\nend-turn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_NO_SESSION", "0")
	t.Setenv("GAX_MOCK_SCRIPT", scriptPath)
	var stdout, stderr strings.Builder
	code := Run([]string{"--json", "--provider=mock", "say hello"}, strings.NewReader(""), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("recorded JSON run: code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("JSON stream is incomplete: %q", stdout.String())
	}
	var header struct {
		Type string  `json:"type"`
		ID   *string `json:"id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Type      string  `json:"type"`
		SessionID *string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatal(err)
	}
	if header.Type != "session" || header.ID == nil || *header.ID == "" ||
		result.Type != "result" || result.SessionID == nil || *result.SessionID != *header.ID {
		t.Fatalf("recorded session identity was lost: header=%+v result=%+v stdout=%q", header, result, stdout.String())
	}
}

func TestRunOneShotBannerAnnouncesRecordedSessionID(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "mock.txt")
	if err := os.WriteFile(scriptPath, []byte("text recorded mock answer\nend-turn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_NO_SESSION", "0")
	t.Setenv("GAX_MOCK_SCRIPT", scriptPath)
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=mock", "say hello"}, strings.NewReader(""), false, &stdout, &stderr)
	if code != 0 || stdout.String() != "recorded mock answer\n" {
		t.Fatalf("recorded one-shot output: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	firstLine := strings.SplitN(stderr.String(), "\n", 2)[0]
	if !strings.Contains(firstLine, " · session ") {
		t.Fatalf("start banner omitted the resumable session id: stderr=%q", stderr.String())
	}
}

func TestOneShotPrintsContextStatsAfterAnswer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_NO_SESSION", "on")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1536,\"completion_tokens\":12}}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")

	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "question"}, strings.NewReader(""), false, &stdout, &stderr)
	if code != 0 || stdout.String() != "answer\n" {
		t.Fatalf("one-shot output: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "gax: openai-compatible · test-model\n\n") ||
		!strings.Contains(stderr.String(), "context 1.5k") {
		t.Fatalf("one-shot exit stats are missing or precede the answer: %q", stderr.String())
	}
}

func stripANSISequences(value string) string {
	var plain strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) && (value[index] < 0x40 || value[index] > 0x7e) {
				index++
			}
			if index < len(value) {
				index++
			}
			continue
		}
		plain.WriteByte(value[index])
		index++
	}
	return plain.String()
}

func TestRunRejectsNestedInvocationAtSubagentDepthLimit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_SUBAGENT_DEPTH", "3")
	var stdout, stderr strings.Builder
	if code := Run([]string{"--version"}, strings.NewReader(""), false, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "gax ") {
		t.Fatalf("version action should exit before the nested-run guard: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(nil, strings.NewReader(""), false, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "subagent depth limit (3) reached") {
		t.Fatalf("nested run at max depth = code %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunOneShotSignalChild(t *testing.T) {
	mode := os.Getenv("GAX_SIGNAL_CHILD")
	if mode == "" {
		return
	}
	var stdout, stderr bytes.Buffer
	prompt := "pause after request"
	if mode == "escalate" {
		prompt = "run the long command"
	}
	code := Run([]string{"--json", "--provider=openai-compatible", "--model=test-model", prompt},
		strings.NewReader(""), false, &stdout, &stderr)
	_, _ = os.Stdout.Write(stdout.Bytes())
	_, _ = os.Stderr.Write(stderr.Bytes())
	os.Exit(code)
}

func waitForTestFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", path)
		}
	}
}

func waitForStableFile(t *testing.T, path string, interval time.Duration) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat heartbeat file: %v", err)
	}
	time.Sleep(interval)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat heartbeat file after child termination: %v", err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("Bash process group kept running after escalation: heartbeat grew from %d to %d bytes", before.Size(), after.Size())
	}
}

func TestRunExportsLiveSelectionToBashChild(t *testing.T) {
	var requests atomic.Int32
	var toolResultRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			arguments, _ := json.Marshal(map[string]string{
				"command": "printf 'provider=%s\\nmodel=%s\\neffort=%s\\npreset=%s\\ndepth=%s\\nagent=%s' \"$GAX_PROVIDER\" \"$GAX_MODEL\" \"$GAX_EFFORT\" \"$GAX_PRESET\" \"$GAX_SUBAGENT_DEPTH\" \"$AI_AGENT\"",
			})
			payload, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"tool_calls": []any{map[string]any{
						"index": 0, "id": "call_env", "type": "function",
						"function": map[string]any{"name": "bash", "arguments": string(arguments)},
					}}},
					"finish_reason": "tool_calls",
				}},
			})
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
			return
		}
		body, _ := io.ReadAll(r.Body)
		toolResultRequest = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "gax")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"catalog":{"models":{"openai-compatible":{"test-model":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("GAX_PRESET", "")
	t.Setenv("GAX_SUBAGENT_DEPTH", "0")
	t.Setenv("AI_AGENT", "parent-agent")
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=openai-compatible", "--model=test-model", "--effort=high", "run tool"},
		strings.NewReader(""), false, &stdout, &stderr)
	if code != 0 || stdout.String() != "done\n" || stderr.String() != "gax: openai-compatible · test-model · high\n\n" {
		t.Fatalf("one-shot tool run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"provider=openai-compatible", "model=test-model", "effort=high", "preset=", "depth=1", "agent=gax"} {
		if !strings.Contains(toolResultRequest, want) {
			t.Errorf("next provider request did not contain child environment value %q: %s", want, toolResultRequest)
		}
	}
}

func TestModelsForPickerUsesVersionOrderOnlyWhenConfigured(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{
			name:   "auto sorts by provider default",
			config: `{}`,
			want:   []string{"gpt-5.6", "gpt-5", "gpt-5-2025-08-07", "gpt-5-mini"},
		},
		{
			name:   "auto keeps provider order when provider disables sorting",
			config: `{"providers":{"openai":{"sort_models":"off"}}}`,
			want:   []string{"gpt-5-mini", "gpt-5-2025-08-07", "gpt-5", "gpt-5.6"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := config.New(config.DefaultSettings())
			store.SetEnvironment(nil)
			if err := store.LoadConfig([]byte(test.config)); err != nil {
				t.Fatal(err)
			}
			providers := registry.New(store)
			provider, err := runtime.NewResolver(store, providers).Resolve("openai")
			if err != nil {
				t.Fatal(err)
			}
			models := []runtime.ModelInfo{
				{ID: "gpt-5-mini", Description: "variant"},
				{ID: "gpt-5-2025-08-07", Description: "snapshot"},
				{ID: "gpt-5", Description: "base"},
				{ID: "gpt-5.6", Description: "version"},
			}
			got := modelsForPicker(provider, models)
			ids := make([]string, len(got))
			for index, model := range got {
				ids[index] = model.ID
			}
			if !reflect.DeepEqual(ids, test.want) {
				t.Fatalf("picker model IDs = %v, want %v", ids, test.want)
			}
			if got[0].Description == "" {
				t.Fatal("sorting discarded model metadata")
			}
			if models[0].ID != "gpt-5-mini" {
				t.Fatalf("sorting mutated API order in place: %v", models)
			}
		})
	}
}

func TestModelPickerMarksModelsWithoutToolSupport(t *testing.T) {
	item := modelPickerItem(nil, runtime.ModelInfo{ID: "vendor/model", Tools: catalog.SupportNo}, true)
	if item.Label != "vendor/model" || !item.Dim || item.Detail != "no tool calling" || !item.Current {
		t.Fatalf("unsupported-tool model picker row = %+v", item)
	}
	unknown := modelPickerItem(nil, runtime.ModelInfo{ID: "vendor/unknown", Tools: catalog.SupportUnknown}, false)
	if unknown.Dim || unknown.Detail != "" {
		t.Fatalf("unknown tool support should keep the model row neutral: %+v", unknown)
	}
}

func TestModelPickerDescriptionUsesResolvedModelMetadata(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openai":{"gpt-5":{"limit":{"context":200000},"cost":{"input":1,"cache_read":0.25,"output":5},"modalities":{"input":["text"]}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	item := modelPickerItem(provider, runtime.ModelInfo{
		ID: "gpt-5", Description: "Primary model", Tools: catalog.SupportYes, MaxContext: 300000,
	}, true)
	want := "200k context (up to 300k) · no images · $1 in / $0.25 cached / $5 out per Mtok\nPrimary model"
	if item.Description != want || item.Dim || !item.Current {
		t.Fatalf("model picker description = %+v, want description %q", item, want)
	}
}

func TestRunOneShotWritesConfiguredPlainTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"logged answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	path := filepath.Join(t.TempDir(), "transcript.txt")
	if err := os.WriteFile(path, []byte("stale transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAX_TRANSCRIPT", path)
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=openai-compatible", "--model=test-model", "say", "hello"},
		strings.NewReader(""), true, &stdout, &stderr)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(data), "system prompt") || !strings.Contains(string(data), "say hello") ||
		!strings.Contains(string(data), "logged answer") || strings.Contains(string(data), "stale transcript") ||
		strings.Contains(string(data), "\x1b[") {
		t.Fatalf("one-shot transcript: code=%d data=%q stdout=%q stderr=%q", code, data, stdout.String(), stderr.String())
	}
}

func TestRunDiscoversLlamaModelBeforeOneShotRequest(t *testing.T) {
	var modelRequests atomic.Int32
	requestModel := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"loaded-local-model"}]}`)
		case "/props":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"default_generation_settings":{"n_ctx":65536},"modalities":{"vision":true}}`)
		case "/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode completion body: %v", err)
			}
			requestModel <- body.Model
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"local answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_LLAMACPP_BASE_URL", server.URL)
	t.Setenv("GAX_MODEL", "")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=llamacpp", "say", "hello"}, strings.NewReader(""), true, &stdout, &stderr)
	var gotModel string
	select {
	case gotModel = <-requestModel:
	default:
	}
	if code != 0 || stdout.String() != "local answer\n" ||
		stderr.String() != "gax: llama.cpp · loaded-local-model\n\n" ||
		modelRequests.Load() != 1 || gotModel != "loaded-local-model" {
		t.Fatalf("llama discovery flow: code=%d modelsRequests=%d requestModel=%q stdout=%q stderr=%q",
			code, modelRequests.Load(), gotModel, stdout.String(), stderr.String())
	}
}

func TestPresetSaveOmitsDiscoveredLlamaModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected request while saving discovered selection: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/props" {
			_, _ = io.WriteString(w, `{"default_generation_settings":{"n_ctx":32768},"modalities":{"vision":false}}`)
			return
		}
		if r.URL.Path != "/models" {
			t.Errorf("unexpected request while saving discovered selection: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"rotating-local-model"}]}`)
	}))
	defer server.Close()
	configRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_PROVIDER", "llamacpp")
	t.Setenv("GAX_LLAMACPP_BASE_URL", server.URL)
	t.Setenv("GAX_MODEL", "")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/preset-save local teal\n/session\n"), false, &stdout, &stderr)
	configData, readErr := os.ReadFile(filepath.Join(configRoot, "gax", "config.json"))
	var root map[string]json.RawMessage
	if readErr == nil {
		readErr = json.Unmarshal(configData, &root)
	}
	var presets map[string]map[string]json.RawMessage
	if readErr == nil {
		readErr = json.Unmarshal(root["presets"], &presets)
	}
	if code != 0 || readErr != nil || len(presets["local"]) == 0 || len(presets["local"]["model"]) != 0 ||
		!strings.Contains(stdout.String(), "saved preset 'local'") || !strings.Contains(stdout.String(), "[local]") ||
		!strings.Contains(stdout.String(), "rotating-local-model") || stderr.Len() != 0 {
		t.Fatalf("discovered model preset save: code=%d preset=%s readErr=%v stdout=%q stderr=%q",
			code, configData, readErr, stdout.String(), stderr.String())
	}
}

func TestRunPersistsTaskExitNotesBeforeShuttingDownTools(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("GAX_NO_SESSION", "off")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_BASH_BACKGROUND_YIELD", "5ms")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\",\\\"background\\\":true,\\\"name\\\":\\\"exit-job\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--provider=openai-compatible", "--model=test-model", "start"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 ||
		!strings.HasPrefix(stderr.String(), "gax: openai-compatible · test-model · session ") ||
		!strings.Contains(stderr.String(), "resume with: gax --resume=") {
		t.Fatalf("run result: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	entries, err := session.List(cwd, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("session entries = %+v, err=%v", entries, err)
	}
	loaded, err := session.LoadAll(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range loaded.Items {
		if item.Kind == conversation.KindUser && item.Origin == conversation.OriginTaskNote &&
			item.Text != nil && strings.Contains(*item.Text, "[task exit-job killed at exit]") {
			return
		}
	}
	t.Fatalf("session did not persist the task exit note: %+v", loaded.Items)
}

func TestRunTaskExitNoteDoesNotCommitPendingSessionSelection(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_NO_SESSION", "off")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_BASH_BACKGROUND_YIELD", "5ms")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\",\\\"background\\\":true,\\\"name\\\":\\\"selection-job\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=initial-model"},
		strings.NewReader("start\n/model next-model\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 {
		t.Fatalf("REPL run: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	entries, err := session.List(cwd, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("session entries = %+v, err=%v", entries, err)
	}
	loaded, err := session.LoadAll(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Model == nil || *loaded.Meta.Model != "initial-model" {
		t.Fatalf("task exit note committed the pending next-session selection: metadata=%+v", loaded.Meta)
	}
	for _, item := range loaded.Items {
		if item.Kind == conversation.KindUser && item.Origin == conversation.OriginTaskNote &&
			item.Text != nil && strings.Contains(*item.Text, "[task selection-job killed at exit]") {
			return
		}
	}
	t.Fatalf("session did not persist the task exit note: %+v", loaded.Items)
}

func TestRunOneShotStillRequiresAModel(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_MODEL", "")
	t.Setenv("GAX_PRESET", "")
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "-p"}, strings.NewReader("hello"), false, &stdout, &stderr)
	want := "gax: no model selected for provider 'openai-compatible' — pass --model or set GAX_MODEL\n"
	if code != 1 || stderr.String() != want || stdout.Len() != 0 {
		t.Fatalf("one-shot without a model should remain an error: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunOneShotEmitsProviderWarningsBeforeMissingModelError(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(configHome, "gax"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "gax", "config.json"),
		[]byte(`{"providers":{"openai":{"api":"openai-completions"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_MODEL", "")
	t.Setenv("GAX_PRESET", "")
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("OPENAI_API_KEY", "test-key")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai", "-p"}, strings.NewReader("hello"), false, &stdout, &stderr)
	want := "gax: provider 'openai': api is pinned to openai-responses — use model_apis for per-model protocols, or a custom provider\n\n" +
		"gax: no model selected for provider 'openai' — pass --model or set GAX_MODEL\n"
	if code != 1 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("one-shot provider warning order: code=%d stdout=%q stderr=%q, want stderr=%q",
			code, stdout.String(), stderr.String(), want)
	}
}

func TestRunCopyCopiesLatestAssistantResponse(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX clipboard fixture; never modify the host Windows clipboard in tests")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"latest answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_NO_SESSION", "on")
	unsetEnvForAppTest(t, "SSH_TTY")
	unsetEnvForAppTest(t, "SSH_CONNECTION")
	clipboardDir := t.TempDir()
	clipboardOutput := filepath.Join(t.TempDir(), "copied.txt")
	clipboardHelper := filepath.Join(clipboardDir, "pbcopy")
	if err := os.WriteFile(clipboardHelper, []byte("#!/bin/sh\n/bin/cat > \"$GAX_TEST_CLIPBOARD\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAX_TEST_CLIPBOARD", clipboardOutput)
	t.Setenv("PATH", clipboardDir)
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"}, strings.NewReader("question\n/copy\n"), false, &stdout, &stderr)
	copied, readErr := os.ReadFile(clipboardOutput)
	if code != 0 || readErr != nil || string(copied) != "latest answer" ||
		!strings.Contains(stdout.String(), "copied 13 bytes to clipboard") || stderr.Len() != 0 {
		t.Fatalf("copy flow: code=%d stdout=%q stderr=%q clipboard=%q readErr=%v", code, stdout.String(), stderr.String(), copied, readErr)
	}
}

func unsetEnvForAppTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestRunCopyWithoutAssistantResponsePrintsNote(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/copy\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "no assistant response to copy") || stderr.Len() != 0 {
		t.Fatalf("empty copy flow: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunLoginAndLogoutCommandArgumentHandling(t *testing.T) {
	for _, test := range []struct {
		name       string
		line       string
		managed    bool
		wantStdout string
		wantStderr string
	}{
		{name: "login needs provider", line: "/login\n", wantStdout: "/login needs a provider name when not interactive"},
		{name: "login unknown provider", line: "/login unknown\n", wantStdout: "no login flow for 'unknown' — gax manages logins for: codex"},
		{name: "logout no managed credentials", line: "/logout\n", wantStdout: "no gax-managed logins to remove"},
		{name: "logout needs provider", line: "/logout\n", managed: true, wantStdout: "/logout needs a provider name when not interactive"},
		{name: "logout unknown provider", line: "/logout openai\n", wantStdout: "no login flow for 'openai' — gax manages logins for: codex"},
		{name: "logout codex not managed", line: "/logout codex\n", wantStdout: "no gax-managed login for Codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			stateHome := filepath.Join(t.TempDir(), "state")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
			t.Setenv("XDG_STATE_HOME", stateHome)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
			t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
			t.Setenv("GAX_OPENAI_API_KEY", "test-key")
			t.Setenv("GAX_NO_SESSION", "on")
			if test.managed {
				path := filepath.Join(stateHome, "gax", "auth.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"codex":{"access_token":"access","refresh_token":"refresh","id_token":"id","account_id":"account"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr strings.Builder
			code := Run([]string{"--provider=openai-compatible", "--model=test-model"}, strings.NewReader(test.line), false, &stdout, &stderr)
			if code != 0 || !strings.Contains(stdout.String(), test.wantStdout) ||
				(test.wantStderr != "" && !strings.Contains(stderr.String(), test.wantStderr)) ||
				(test.wantStderr == "" && stderr.Len() != 0) {
				t.Fatalf("command %q: code=%d stdout=%q stderr=%q", test.line, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunREPLSelectionErrorsUseDisplayChannel(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	for _, test := range []struct {
		line string
		want string
	}{
		{line: "/provider openai", want: "openai is unavailable — OPENAI_API_KEY not set"},
		{line: "/provider missing", want: "unknown provider 'missing' — /provider lists them"},
		{line: "/effort bogus", want: "test-model doesn't take effort 'bogus'"},
		{line: "/preset missing", want: "unknown preset 'missing' (define a presets.missing block in config.json)"},
		{line: "/config unknown", want: "unknown setting 'unknown' — /config lists them"},
		{line: "/resume missing", want: "/resume takes no arguments."},
		{line: "/preset-save bad:name", want: "'bad:name' can't be a preset name"},
		{line: "/preset-save valid nope", want: "unknown tint 'nope' (expected teal|violet|rose|sage)"},
	} {
		t.Run(test.line, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
				strings.NewReader(test.line+"\n"), false, &stdout, &stderr)
			if code != 0 || !strings.Contains(stdout.String(), test.want) || stderr.Len() != 0 {
				t.Fatalf("slash command %q: code=%d stdout=%q stderr=%q", test.line, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunREPLSelectsSelectionByArgument(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	for _, test := range []struct {
		line string
		want string
	}{
		{line: "/model target-model", want: "switched to openai-compatible · target-model"},
		{line: "/effort high", want: "switched to openai-compatible · test-model · high"},
		{line: "/provider openai-compatible", want: "already using openai-compatible — /model switches its model"},
	} {
		t.Run(test.line, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
				strings.NewReader(test.line+"\n"), false, &stdout, &stderr)
			if code != 0 || !strings.Contains(stdout.String(), test.want) || stderr.Len() != 0 {
				t.Fatalf("slash command %q: code=%d stdout=%q stderr=%q", test.line, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunAllowsLoginCommandsWhenExplicitProviderHasNoCredentials(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	for _, key := range []string{
		"GAX_PROVIDER", "GAX_MODEL", "GAX_OPENAI_BASE_URL", "GAX_OPENAI_API_KEY", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OPENCODE_API_KEY",
		"GAX_ANTHROPIC_BASE_URL", "GAX_OPENAI_COMPATIBLE_BASE_URL", "GAX_ANTHROPIC_COMPATIBLE_BASE_URL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=codex"}, strings.NewReader("/login\nhello\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "/login needs a provider name when not interactive") ||
		!strings.Contains(stdout.String(), "no provider selected — use /provider to choose one, then resend") {
		t.Fatalf("providerless command flow: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunAllowsLoginCommandsWhenNoProviderIsAvailable(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	for _, key := range []string{
		"GAX_PROVIDER", "GAX_MODEL", "GAX_OPENAI_BASE_URL", "GAX_OPENAI_API_KEY", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY", "OPENCODE_API_KEY",
		"GAX_ANTHROPIC_BASE_URL", "GAX_OPENAI_COMPATIBLE_BASE_URL", "GAX_ANTHROPIC_COMPATIBLE_BASE_URL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("GAX_LLAMACPP_PORT", "1")
	t.Setenv("GAX_OLLAMA_PORT", "1")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/login\nhello\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "/login needs a provider name when not interactive") ||
		!strings.Contains(stdout.String(), "no provider selected — use /provider to choose one, then resend") {
		t.Fatalf("providerless command flow: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunUsesCodexProviderDefaultsWhenModelAndEffortAreUnset(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_NO_SESSION", "on")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"),
		[]byte("model = \"gpt-5.6\"\nmodel_reasoning_effort = \"high\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateRoot, "gax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"codex":{"access_token":"access","refresh_token":"refresh","id_token":"id","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/session\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "codex · gpt-5.6 · high") || stderr.Len() != 0 {
		t.Fatalf("Codex defaults were not applied: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSelectProviderProbesLocalProvidersBeforeAutoSelection(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("probe path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("providers.llamacpp.base_url", &server.URL)
	providers := registry.New(store)
	provider, err := selectProvider(store, providers, runtime.NewResolver(store, providers))
	if err != nil || provider == nil || provider.Definition.ID != "llamacpp" || !provider.Availability.Ready() {
		t.Fatalf("auto-selection did not probe local provider: provider=%+v err=%v", provider, err)
	}
}

func TestSelectExplicitProviderMatchesConstructionSemantics(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	providers := registry.New(store)
	resolver := runtime.NewResolver(store, providers)

	store.SetOverride("provider", stringPtr("openai"))
	provider, err := selectProvider(store, providers, resolver)
	if err != nil || provider == nil || provider.Availability.State != runtime.AvailabilityMissingCredential {
		t.Fatalf("explicit provider with a missing API key should still construct: provider=%+v err=%v", provider, err)
	}

	store.SetOverride("provider", stringPtr("openai-compatible"))
	provider, err = selectProvider(store, providers, resolver)
	want := "provider 'openai-compatible': no base_url (set GAX_OPENAI_BASE_URL, or providers.openai-compatible.base_url in config.json)"
	if provider != nil || err == nil || err.Error() != want {
		t.Fatalf("unconfigured provider diagnostic = provider=%+v err=%v, want %q", provider, err, want)
	}

	store.SetOverride("provider", stringPtr("missing"))
	provider, err = selectProvider(store, providers, resolver)
	want = "unknown provider 'missing' (supported: codex openai anthropic openrouter opencode-go deepseek llamacpp ollama openai-compatible anthropic-compatible)"
	if provider != nil || err == nil || err.Error() != want {
		t.Fatalf("unknown provider diagnostic = provider=%+v err=%v, want %q", provider, err, want)
	}
}

func TestSelectExplicitCodexReportsMissingLoginDirectly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("provider", stringPtr("codex"))
	providers := registry.New(store)
	provider, err := selectProvider(store, providers, runtime.NewResolver(store, providers))
	want := "no ChatGPT login found — run /login, or log in with the codex CLI (" + filepath.Join(home, ".codex", "auth.json") + ")"
	if provider != nil || err == nil || err.Error() != want {
		t.Fatalf("missing Codex login diagnostic = provider=%+v err=%v, want %q", provider, err, want)
	}
}

func TestLlamaDiscoveryFailureUsesRecordedDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("providers.llamacpp.base_url", stringPtr(server.URL+"/v1"))
	providers := registry.New(store)
	provider, err := runtime.NewResolver(store, providers).Resolve("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = discoverProviderModel(provider, store, transport.DefaultClient, nil)
	want := "llama.cpp: failed to auto-discover model from " + server.URL + "/v1/models\ngax: is llama-server running? (set GAX_MODEL to skip probing, or adjust GAX_LLAMACPP_PORT / GAX_LLAMACPP_BASE_URL)"
	if err == nil || err.Error() != want {
		t.Fatalf("llama.cpp discovery diagnostic = %v, want %q", err, want)
	}
}

func TestRunUnknownPresetUsesRecordedDiagnostic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr strings.Builder
	code := Run([]string{"--preset=missing"}, strings.NewReader(""), false, &stdout, &stderr)
	want := "gax: unknown preset 'missing' (define a presets.missing block in config.json)\n"
	if code != 1 || stderr.String() != want {
		t.Fatalf("unknown preset diagnostic: code=%d stderr=%q, want code=1 stderr=%q", code, stderr.String(), want)
	}
}

func TestRunMissingResumeUsesRecordedDiagnostic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr strings.Builder
	code := Run([]string{"--resume=missing"}, strings.NewReader(""), false, &stdout, &stderr)
	want := "gax: no session matching 'missing'\n"
	if code != 1 || stderr.String() != want {
		t.Fatalf("missing resume diagnostic: code=%d stderr=%q, want code=1 stderr=%q", code, stderr.String(), want)
	}
}

func TestRunInteractiveEOFLeavesTrailingSeparator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAX_THEME", "off")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai", "--model=test-model"}, strings.NewReader(""), false, &stdout, &stderr)
	trailingNewlines := len(stdout.String()) - len(strings.TrimRight(stdout.String(), "\n"))
	if code != 0 || trailingNewlines != 3 || stderr.Len() != 0 {
		t.Fatalf("interactive EOF output: code=%d stdout=%q stderr=%q, trailing newlines=%d want 3",
			code, stdout.String(), stderr.String(), trailingNewlines)
	}
}

func TestRunEmptyREPLEOFDoesNotCreateResumableSession(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_THEME", "off")
	t.Setenv("GAX_NO_SESSION", "off")
	var stdout, stderr strings.Builder
	if code := Run(nil, strings.NewReader(""), false, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("empty REPL: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "resume with: gax --resume=") {
		t.Fatalf("empty REPL advertised a resumable session: %q", stdout.String())
	}
	files, err := filepath.Glob(filepath.Join(stateRoot, "gax", "sessions", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("empty REPL created session files: %v", files)
	}
}

func TestRunREPLEOFPrintsResumeHint(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("hello\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "resume with: gax --resume=") || stderr.Len() != 0 {
		t.Fatalf("REPL resume hint: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunTasksListsAndKillsBackgroundCommands(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\",\\\"background\\\":true,\\\"name\\\":\\\"listed-job\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_BASH_TIMEOUT_GRACE", "0")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("question\n/tasks\n/tasks kill all\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 || !strings.Contains(stdout.String(), "listed-job") ||
		!strings.Contains(stdout.String(), "running ·") || !strings.Contains(stdout.String(), "sleep 30") ||
		!strings.Contains(stdout.String(), "stopped 1 task") || stderr.Len() != 0 {
		t.Fatalf("task command flow: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
}

func TestRunTasksReportsDisabledTasks(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_TASKS", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/tasks\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "background tasks are disabled (no_tasks)") || stderr.Len() != 0 {
		t.Fatalf("disabled task list: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunTasksUsageErrorsAreDisplayedOnStdout(t *testing.T) {
	for _, input := range []struct {
		argument string
		want     string
	}{
		{argument: "bogus", want: "usage: /tasks [kill <id>... | kill all]\n"},
		{argument: "kill", want: "usage: /tasks kill <id>... | kill all\n"},
	} {
		t.Run(input.argument, func(t *testing.T) {
			var stdout, stderr strings.Builder
			runTasksCommand(slash.Parse("/tasks "+input.argument), nil, false, nil, &stdout, &stderr)
			if stdout.String() != input.want || stderr.Len() != 0 {
				t.Fatalf("tasks usage output: stdout=%q stderr=%q, want stdout=%q and empty stderr", stdout.String(), stderr.String(), input.want)
			}
		})
	}
}

func TestRunCompactUsesFocusAndReplacesHistoryWithSummarySeed(t *testing.T) {
	var requests atomic.Int32
	var compactionRequest atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		if requestNumber == 2 {
			compactionRequest.Store(strings.Contains(string(body), "Additional focus for this summary:\\nkeep path names"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		answer := "ordinary answer"
		if requestNumber == 2 {
			answer = "## Goal\n- Keep the path names"
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("question\n/compact keep path names\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 || !compactionRequest.Load() ||
		!strings.Contains(stdout.String(), "ordinary answer") || !strings.Contains(stdout.String(), "conversation compacted") ||
		strings.Contains(stdout.String(), "Keep the path names") || stderr.Len() != 0 {
		t.Fatalf("compact command flow: code=%d requests=%d compactRequest=%t stdout=%q stderr=%q", code, requests.Load(), compactionRequest.Load(), stdout.String(), stderr.String())
	}
}

func TestRunCompactResyncsEffortAfterModelMetadataArrives(t *testing.T) {
	modelsStarted := make(chan struct{})
	releaseModels := make(chan struct{})
	var releaseOnce sync.Once
	releaseMetadata := func() { releaseOnce.Do(func() { close(releaseModels) }) }
	defer releaseMetadata()
	requestEffort := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			close(modelsStarted)
			<-releaseModels
			_, _ = io.WriteString(w, `{"data":[{"id":"test-model","capabilities":{"effort":{"supported":true,"low":{"supported":true}}}}]}`)
		case "/messages":
			var body struct {
				OutputConfig struct {
					Effort string `json:"effort"`
				} `json:"output_config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode compaction request: %v", err)
			}
			requestEffort <- body.OutputConfig.Effort
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"test-model\",\"usage\":{\"input_tokens\":2}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"summary\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			t.Errorf("unexpected Anthropic request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("GAX_ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("GAX_ANTHROPIC_API_KEY", "test-key")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(os.LookupEnv)
	store.SetOverride("effort", stringPtr("high"))
	providers := registry.New(store)
	provider, err := runtime.NewResolver(store, providers).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if !provider.StartModelProbe(context.Background(), "test-model", transport.DefaultClient) {
		t.Fatal("model metadata probe did not start")
	}
	select {
	case <-modelsStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("model metadata probe did not reach the test server")
	}

	state := agent.NewSession()
	state.ProviderID = stringPtr("anthropic-compatible")
	state.Model = stringPtr("test-model")
	state.Effort = stringPtr("high")
	state.Items = []conversation.Item{{Kind: conversation.KindUser, Text: stringPtr("continue this work")}}
	log, err := session.NewLog(t.TempDir(), session.Selection{
		Provider: state.ProviderID, Model: state.Model, Effort: state.Effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(state.Items); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	done := make(chan struct{})
	go func() {
		runCompactCommand(context.Background(), slash.Parse("/compact"), provider, state.Model,
			state, log.ID(), log, store, strings.NewReader(""), false, &stdout, &stderr)
		close(done)
	}()
	select {
	case effort := <-requestEffort:
		releaseMetadata()
		<-done
		t.Fatalf("compaction request used effort %q before model metadata arrived", effort)
	case <-time.After(100 * time.Millisecond):
		releaseMetadata()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("compaction did not finish after model metadata arrived")
	}
	select {
	case effort := <-requestEffort:
		if effort != "low" {
			t.Fatalf("compaction request effort = %q, want metadata-supported low", effort)
		}
	case <-time.After(time.Second):
		t.Fatal("compaction did not send a request")
	}
	if state.Effort == nil || *state.Effort != "low" {
		t.Fatalf("session effort after compaction = %v, want low", state.Effort)
	}
	loaded, err := session.LoadAll(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Effort == nil || *loaded.Meta.Effort != "low" {
		t.Fatalf("recorded effort after compaction = %v, want low", loaded.Meta.Effort)
	}
}

func TestPresetSelectionStartsModelMetadataRefresh(t *testing.T) {
	modelProbeStarted := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/models" && request.Method == http.MethodGet {
			startedOnce.Do(func() { close(modelProbeStarted) })
			<-request.Context().Done()
			return
		}
		request.Body.Close()
		t.Errorf("unexpected request while applying preset: %s %s", request.Method, request.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("GAX_ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("GAX_ANTHROPIC_API_KEY", "test-key")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")
	// Applying a preset persists it to state.json, so the state tier has to stay in the sandbox.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(os.LookupEnv)
	if err := store.LoadConfig([]byte(`{"presets":{"review":{"provider":"anthropic-compatible","model":"test-model"}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := registry.New(store)
	resolver := runtime.NewResolver(store, providers)
	state := agent.NewSession()
	selected, _, applied := runPresetCommand(context.Background(), slash.Parsed{Argument: "review", HasArgument: true}, resolver,
		nil, nil, state, nil, store, strings.NewReader(""), false, agent.PromptOptions{}, io.Discard, io.Discard)
	if !applied || selected == nil {
		t.Fatal("preset was not applied")
	}
	defer selected.Close()
	select {
	case <-modelProbeStarted:
	case <-time.After(time.Second):
		t.Fatal("preset selection did not start a provider model metadata refresh")
	}
}

func TestRunAutomaticallyCompactsAtConfiguredContextThreshold(t *testing.T) {
	var requests atomic.Int32
	var sawCompactionRequest atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		if requestNumber == 2 {
			sawCompactionRequest.Store(strings.Contains(string(body), "Summarize the conversation so far"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ordinary answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	home := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"catalog":{"models":{"openai-compatible":{"test-model":{"limit":{"context":1000}}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(home, "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_COMPACT_AUTO", "on")
	t.Setenv("GAX_COMPACT_THRESHOLD", "1")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"}, strings.NewReader("question\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 || !sawCompactionRequest.Load() ||
		!strings.Contains(stdout.String(), "ordinary answer\n\n\x1b[2m") ||
		!strings.HasSuffix(stdout.String(), "\x1b[0m\n\n\n") || stderr.Len() != 0 {
		t.Fatalf("auto-compact flow: code=%d requests=%d compactionRequest=%t stdout=%q stderr=%q", code, requests.Load(), sawCompactionRequest.Load(), stdout.String(), stderr.String())
	}
}

func TestRunDefersAutoCompactionAndContinuesAtEmptySubmit(t *testing.T) {
	var requests atomic.Int32
	var sawDeferredSummary atomic.Bool
	var continuedRequestSent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestNumber {
		case 1:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"echo seam\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
		case 2:
			// The at-tool-seam compaction attempt has no summary, so its debt must survive.
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case 3:
			sawDeferredSummary.Store(strings.Contains(string(body), "Summarize the conversation so far") &&
				strings.Contains(string(body), "first question") && !strings.Contains(string(body), "second question"))
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary of first\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case 4:
			continuedRequestSent.Store(strings.Contains(string(body), "summary of first") && !strings.Contains(string(body), "first question"))
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected provider request %d", requestNumber)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	configRoot := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configBody := `{"catalog":{"models":{"openai-compatible":{"test-model":{"limit":{"context":1000}}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_COMPACT_AUTO", "on")
	t.Setenv("GAX_COMPACT_THRESHOLD", "1")
	t.Setenv("GAX_MAX_TURNS", "1")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("first question\n\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 4 || !sawDeferredSummary.Load() || !continuedRequestSent.Load() ||
		!strings.Contains(stdout.String(), "continued answer") || stderr.Len() != 0 {
		t.Fatalf("deferred compaction flow: code=%d requests=%d summary=%t secondPrompt=%t stdout=%q stderr=%q",
			code, requests.Load(), sawDeferredSummary.Load(), continuedRequestSent.Load(), stdout.String(), stderr.String())
	}
}

func TestAssistantAnswerUsesMarkdownOnlyForTerminals(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	var ttyOutput strings.Builder
	if err := writeAssistantAnswerWithTerminal(&ttyOutput, "## Heading\n", store, 80, true); err != nil {
		t.Fatal(err)
	}
	if got, want := ttyOutput.String(), "\x1b[1mHeading\x1b[22m\n"; got != want {
		t.Fatalf("TTY answer = %q, want %q", got, want)
	}
	var pipeOutput strings.Builder
	if err := writeAssistantAnswerWithTerminal(&pipeOutput, "## Heading\n", store, 0, false); err != nil {
		t.Fatal(err)
	}
	if got, want := pipeOutput.String(), "## Heading\n"; got != want {
		t.Fatalf("piped answer = %q, want raw %q", got, want)
	}
}

func TestEditorDisplayColumnsFollowConfigChanges(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if got := editorDisplayColumns(store, 111); got != 100 {
		t.Fatalf("automatic editor width = %d, want capped width 100", got)
	}
	store.SetOverride("display_width", stringPtr("terminal"))
	if got := editorDisplayColumns(store, 111); got != 111 {
		t.Fatalf("terminal editor width = %d, want 111", got)
	}
	store.SetOverride("display_width", stringPtr("60"))
	if got := editorDisplayColumns(store, 160); got != 60 {
		t.Fatalf("exact editor width = %d, want 60", got)
	}
	if got := editorDisplayColumns(store, 40); got != 40 {
		t.Fatalf("editor width beyond physical terminal = %d, want clamped width 40", got)
	}
}

func TestMarkdownDisplayColumnsReservePhysicalEdge(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if got := markdownDisplayColumns(store, 80); got != 79 {
		t.Fatalf("automatic Markdown width = %d, want terminal edge reserve 79", got)
	}
	store.SetOverride("display_width", stringPtr("terminal"))
	if got := markdownDisplayColumns(store, 111); got != 110 {
		t.Fatalf("terminal Markdown width = %d, want terminal edge reserve 110", got)
	}
	store.SetOverride("display_width", stringPtr("120"))
	if got := markdownDisplayColumns(store, 80); got != 79 {
		t.Fatalf("wide configured Markdown width = %d, want physical edge reserve 79", got)
	}
	if got := markdownDisplayColumns(store, 15); got != 14 {
		t.Fatalf("narrow Markdown width = %d, want physical edge reserve 14", got)
	}
}

func TestHelpWrapsRowsAtConfiguredDisplayWidth(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetOverride("display_width", stringPtr("30"))
	var output strings.Builder
	printHelp(&output, store)
	if !strings.Contains(output.String(), "--resume[=ID]") || !strings.Contains(output.String(), "barebones chat") || !strings.Contains(output.String(), "README.md") {
		t.Fatalf("help omits expected usage text: %q", output.String())
	}
	if !strings.Contains(output.String(), "\n    Non-interactive mode. Runs\n") {
		t.Fatalf("stacked option descriptions should start four cells in: %q", output.String())
	}
	for _, row := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if cells := len([]rune(row)); cells > 30 {
			t.Fatalf("help row is %d cells at display_width=30: %q", cells, row)
		}
	}
}

func TestHelpStylesTTYOutputAndKeepsPipedOutputPlain(t *testing.T) {
	previous := terminaltheme.Current()
	t.Cleanup(func() { terminaltheme.Configure(previous.Name(), previous.TintName()) })
	terminaltheme.Configure("ansi", "teal")
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })

	var styled strings.Builder
	printHelpWithStyle(&styled, store, true)
	for _, want := range []string{
		"\x1b[1mgax " + version.Value + "\x1b[0m",
		"\x1b[1musage:\x1b[0m",
		"\x1b[1moptions\x1b[0m",
		"  \x1b[36m-p, --print\x1b[0m",
	} {
		if !strings.Contains(styled.String(), want) {
			t.Errorf("styled help omits %q: %q", want, styled.String())
		}
	}

	var plain strings.Builder
	printHelpWithStyle(&plain, store, false)
	if strings.ContainsRune(plain.String(), '\x1b') {
		t.Fatalf("piped help contains terminal styling: %q", plain.String())
	}
	if !strings.Contains(plain.String(), "gax "+version.Value+" — ") || !strings.Contains(plain.String(), "  -p, --print") {
		t.Fatalf("plain help lost its title or option labels: %q", plain.String())
	}
}

func TestNoteLineUsesDimStylingOnlyForTerminalOutput(t *testing.T) {
	var styled, plain strings.Builder
	writeNoteLineWithStyle(&styled, "setting = value (config)", true)
	writeNoteLineWithStyle(&plain, "setting = value (config)", false)
	if got, want := styled.String(), "\x1b[2msetting = value (config)\x1b[0m\n"; got != want {
		t.Fatalf("styled note = %q, want %q", got, want)
	}
	if got, want := plain.String(), "setting = value (config)\n"; got != want {
		t.Fatalf("plain note = %q, want %q", got, want)
	}
}

func TestTurnStatsLineWrapsAtSegmentBoundariesAndShowsEstimatedSpend(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetOverride("display_width", stringPtr("20"))
	state := &agent.Session{Items: []conversation.Item{{
		Kind:  conversation.KindTurnUsage,
		Usage: &conversation.Usage{Cost: conversation.Float64(0.012), CostEstimated: true},
	}}}
	var output strings.Builder
	if err := writeTurnStatsLineWithStyle(&output, state, nil, 1536, 1250, store, true); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[2m1s · context 1.5k\x1b[0m\n\x1b[2m~$0.012\x1b[0m\n"
	if output.String() != want {
		t.Fatalf("turn stats = %q, want %q", output.String(), want)
	}
}

func TestReplayRecentTurnShowsOnlyTheLatestVisibleTurn(t *testing.T) {
	state := &agent.Session{Items: []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("older prompt")},
		{Kind: conversation.KindAssistant, Text: conversation.String("older answer")},
		{Kind: conversation.KindTurnBoundary},
		{Kind: conversation.KindUser, Text: conversation.String("latest prompt")},
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("hidden reasoning")},
		{Kind: conversation.KindToolCall, CallID: conversation.String("latest-call"), ToolName: conversation.String("read"),
			Arguments: conversation.String(`{"path":"/tmp/latest.txt"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("latest-call"), Output: conversation.String("LATEST_SECRET_BODY\n")},
		{Kind: conversation.KindAssistant, Text: conversation.String("latest answer")},
		{Kind: conversation.KindTurnUsage, Usage: &conversation.Usage{ElapsedMS: conversation.Int64(100)}},
	}}
	var output strings.Builder
	if err := replayRecentTurn(&output, state, "resumed", nil, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	plain := stripANSISequences(output.String())
	for _, want := range []string{"resumed · 1 earlier message · ctrl-o for full history", "▌ latest prompt", "[read]", "latest.txt", "latest answer"} {
		if !strings.Contains(plain, want) {
			t.Errorf("replay omits %q: %q", want, plain)
		}
	}
	for _, unwanted := range []string{"older prompt", "older answer", "hidden reasoning", "LATEST_SECRET_BODY", "user:"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("replay includes %q: %q", unwanted, plain)
		}
	}
}

func TestSessionSummaryWrapsRowsAtConfiguredDisplayWidth(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetOverride("display_width", stringPtr("30"))
	var output strings.Builder
	printSessionSummaryWithStyle(&output, nil, nil, nil, nil, store, false)
	if !strings.HasPrefix(output.String(), "  session\n    not recorded\n") {
		t.Fatalf("narrow session summary did not stack label and value: %q", output.String())
	}
	for _, row := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if cells := len([]rune(row)); cells > 30 {
			t.Fatalf("session summary row is %d cells at display_width=30: %q", cells, row)
		}
	}
}

func TestSessionSummaryPreservesRowStylingWhenRedirected(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })

	var redirected strings.Builder
	printSessionSummary(&redirected, nil, nil, nil, nil, store)
	if !strings.Contains(redirected.String(), "  \x1b[2msession\x1b[0m") ||
		!strings.Contains(redirected.String(), "\x1b[2mnot recorded\x1b[0m") {
		t.Fatalf("redirected session summary did not preserve dim styling: %q", redirected.String())
	}

	var plain strings.Builder
	printSessionSummaryWithStyle(&plain, nil, nil, nil, nil, store, false)
	if strings.ContainsRune(plain.String(), '\x1b') {
		t.Fatalf("piped session summary contains terminal styling: %q", plain.String())
	}
}

func TestSessionSummaryKeepsModelAndEffortTogetherOnNarrowRows(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetOverride("display_width", stringPtr("25"))
	state := &agent.Session{
		ProviderID: stringPtr("provider-name"),
		Model:      stringPtr("model-name"),
		Effort:     stringPtr("high"),
	}
	var output strings.Builder
	printSessionSummaryWithStyle(&output, state, nil, nil, nil, store, false)
	if !strings.Contains(output.String(), "  provider\n    provider-name\n    model-name · high\n") {
		t.Fatalf("narrow session summary did not keep model and effort together: %q", output.String())
	}
}

func TestSlashHelpWrapsRowsAtConfiguredDisplayWidth(t *testing.T) {
	var output strings.Builder
	printSlashHelpWithStyle(&output, 30, false)
	if !strings.Contains(output.String(), "ctrl-t") || !strings.Contains(output.String(), "transcript in $PAGER") ||
		!strings.Contains(output.String(), "ctrl-o") || !strings.Contains(output.String(), "view the conversation in") {
		t.Fatalf("slash help omitted a history or transcript shortcut: %q", output.String())
	}
	for _, row := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if cells := len([]rune(row)); cells > 30 {
			t.Fatalf("slash help row is %d cells at display_width=30: %q", cells, row)
		}
	}
}

func TestSlashHelpStylesRowsEvenWhenRedirected(t *testing.T) {
	previous := terminaltheme.Current()
	t.Cleanup(func() { terminaltheme.Configure(previous.Name(), previous.TintName()) })
	terminaltheme.Configure("ansi", "teal")

	var redirected strings.Builder
	printSlashHelp(&redirected, 80)
	for _, want := range []string{
		"\x1b[1mcommands\x1b[0m",
		"\x1b[36m/new\x1b[0m",
		"\x1b[2m\x1b[36m/clear\x1b[0m",
		"\x1b[2malias for /new\x1b[0m",
		"\x1b[1mshortcuts\x1b[0m",
	} {
		if !strings.Contains(redirected.String(), want) {
			t.Errorf("redirected slash help omits style %q: %q", want, redirected.String())
		}
	}
}

func TestRunInteractiveLoopAcceptsPromptsUntilEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("hello\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "reply\n\n\x1b[2m") ||
		!strings.HasSuffix(stdout.String(), "\x1b[0m\n\n\n") || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "gax") || !strings.Contains(stdout.String(), "openai-compatible · test-model") {
		t.Fatalf("interactive result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLConsumesSlashCommands(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	// /model persists its selection to state.json, so the state tier has to stay in the sandbox.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("/help\n/nonesuch\n/model fast\n/resume\nhello\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 1 || !strings.Contains(stdout.String(), "commands") ||
		!strings.Contains(stdout.String(), "/new") || !strings.Contains(stdout.String(), "start a fresh conversation [preset]") ||
		!strings.Contains(stdout.String(), "switch provider, then model and effort [id]") ||
		!strings.Contains(stdout.String(), "switched to openai-compatible · fast") || !strings.Contains(stdout.String(), "reply\n") {
		t.Fatalf("REPL slash dispatch: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "unknown command: /nonesuch") || stderr.Len() != 0 {
		t.Errorf("REPL diagnostic channel: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunREPLModelListingFailureUsesDisplayChannel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL+"/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("/model\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "listing openai-compatible models failed (HTTP 503)") || stderr.Len() != 0 {
		t.Fatalf("model listing failure: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLPickerCommandsStayQuietWithoutTTY(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock"}, strings.NewReader("/provider\n/config\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("non-TTY picker commands: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, choice := range []string{"codex\n", "openai\n", "preset = unset"} {
		if strings.Contains(stdout.String(), choice) {
			t.Errorf("non-TTY picker command printed %q: %q", choice, stdout.String())
		}
	}
}

func TestRunREPLMockModelCommandExplainsExplicitModelSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock"}, strings.NewReader("/model\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "mock can't list models — set one with GAX_MODEL or in config") {
		t.Fatalf("mock model command: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLHistoryEmptyNotesUseStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock"}, strings.NewReader("/undo\n/fork\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "nothing to undo yet") ||
		!strings.Contains(stdout.String(), "nothing to undo yet\n\n\nnothing to fork yet") ||
		!strings.Contains(stdout.String(), "nothing to fork yet") {
		t.Fatalf("empty history commands: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLBlockSeparatorsMatchRecordedOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock"}, strings.NewReader("/provider\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.HasSuffix(stdout.String(), "\n\n\n\n") {
		t.Fatalf("non-TTY picker separator: code=%d stdout tail=%q stderr=%q", code, stdout.String()[max(0, stdout.Len()-80):], stderr.String())
	}
}

func TestRunREPLResumePickerSeparatorMatchesRecordedOutput(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("/resume\n"), false, &stdout, &stderr)
	if code != 0 || !strings.HasSuffix(stdout.String(), "\n\n\n\n\n") || stderr.Len() != 0 {
		t.Fatalf("non-TTY resume picker separator: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLNewBannerSpacingMatchesRecordedOutput(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("/new\n"), false, &stdout, &stderr)
	want := "try /help\x1b[22m\n\n\n▌ \x1b[1mgax\x1b[22m"
	if code != 0 || !strings.Contains(stdout.String(), want) || stderr.Len() != 0 {
		t.Fatalf("non-TTY /new banner spacing: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCompactWithoutHistoryUsesRecordedStyle(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("/compact\n"), false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "\x1b[2m── nothing to compact ──\x1b[0m") || stderr.Len() != 0 {
		t.Fatalf("non-TTY /compact output: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLRetainsConfiguredEffortUntilModelMetadataArrives(t *testing.T) {
	var modelRequests atomic.Int32
	requestEffort := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			modelRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"test-model","capabilities":{"effort":{"supported":true,"turbo":{"supported":true}}}}]}`)
		case "/messages":
			var body struct {
				OutputConfig struct {
					Effort string `json:"effort"`
				} `json:"output_config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			requestEffort <- body.OutputConfig.Effort
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"test-model\",\"usage\":{\"input_tokens\":2}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"reply\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			t.Errorf("unexpected Anthropic request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_PROVIDER", "anthropic-compatible")
	t.Setenv("GAX_ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("GAX_ANTHROPIC_API_KEY", "test-key")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_EFFORT", "turbo")
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("GAX_CATALOG_URL", "")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("hello\n/session\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || modelRequests.Load() == 0 {
		t.Fatalf("late custom effort run: code=%d modelRequests=%d stdout=%q stderr=%q", code, modelRequests.Load(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "anthropic-compatible · test-model · turbo") {
		t.Fatalf("session summary omitted the late-resolved custom effort: %q", stdout.String())
	}
	select {
	case effort := <-requestEffort:
		if effort != "turbo" {
			t.Fatalf("request effort = %q, want configured custom effort turbo; stdout=%q stderr=%q", effort, stdout.String(), stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive a request with the configured effort")
	}
}

func TestRunREPLConfigCommandReadsValidatesAndUpdatesEditableSettings(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/config theme\n/config theme LIGHT\n/config theme invalid\n/config provider\n/config provider openai-compatible\n/config theme default\n/config theme\n"), false, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config commands: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"theme = auto (default)", "theme = light (run)", "change it with /provider"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("config output omits %q: %q", want, stdout.String())
		}
	}
	for _, want := range []string{"invalid value 'invalid' for theme", "'provider' can't be changed from /config"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("config diagnostic omits %q: %q", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("config command wrote diagnostics to stderr: %q", stderr.String())
	}
}

func TestRunREPLPresetAppliesLiveSelectionAndPersistsItsName(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"review":{"description":"Review changes","provider":"openai-compatible","model":"review-model","system_prompt":"review-only system directive"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "initial-model")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var requestBody = make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		requestBody <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"review reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	// A unique Tab completion leaves a trailing space before Enter.
	code := Run(nil, strings.NewReader("/preset review \nreview this\n/session\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("preset command: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var body string
	select {
	case body = <-requestBody:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the preset request")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(decoded["model"], &model); err != nil || model != "review-model" {
		t.Fatalf("preset request model = %q, err=%v; body=%s", model, err, body)
	}
	if !strings.Contains(body, "review-only system directive") {
		t.Fatalf("preset system prompt was not applied to the request: %s", body)
	}
	if !strings.Contains(stdout.String(), "preset") || !strings.Contains(stdout.String(), "review") {
		t.Fatalf("session summary did not reflect active preset: %q", stdout.String())
	}
	stateData, err := os.ReadFile(filepath.Join(stateRoot, "gax", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]string
	if err := json.Unmarshal(stateData, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["preset"] != "review" {
		t.Fatalf("active preset was not persisted: %s", stateData)
	}
}

func TestRunREPLPresetDropsUnsupportedEffort(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"high":{"provider":"mock","model":"mock-model","effort":"high"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock", "--model=mock-model"}, strings.NewReader("/preset high\n/session\n"), false, &stdout, &stderr)
	plain := stripANSISequences(stdout.String())
	if code != 0 || stderr.Len() != 0 || strings.Contains(plain, "mock-model · high") {
		t.Fatalf("unsupported preset effort remained selected: code=%d stdout=%q stderr=%q", code, plain, stderr.String())
	}
}

func TestRunREPLPresetUsesProviderDefaultAndDisplaysPresetErrors(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"broken":{"provider":"missing-provider","model":"other-model"},"default-model":{"provider":"mock"},"no-credential":{"provider":"openai","model":"gpt-model"},"unconfigured":{"provider":"openai-compatible","model":"other-model"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("OPENAI_API_KEY", "")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock", "--model=test-model"},
		strings.NewReader("/preset broken\n/session\n/preset unconfigured\n/preset no-credential\n/session\n/preset default-model\n/session\n"), false, &stdout, &stderr)
	wantDiagnostic := "gax: provider 'openai-compatible': no base_url (set GAX_OPENAI_BASE_URL, or providers.openai-compatible.base_url in config.json)\n"
	if code != 0 || stderr.String() != wantDiagnostic {
		t.Fatalf("preset error/default flow: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	plainOutput := stripANSISequences(stdout.String())
	for _, want := range []string{
		"preset 'broken': unknown provider 'missing-provider'",
		"mock · test-model",
		"openai · gpt-model",
		"[default-model]",
		"mock · mock-model",
	} {
		if !strings.Contains(plainOutput, want) {
			t.Errorf("preset output omits %q: %q", want, plainOutput)
		}
	}
}

func TestRunREPLPresetDiscoveryFailureUsesRecordedDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"local":{"provider":"llamacpp"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GAX_LLAMACPP_BASE_URL", server.URL+"/v1")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=mock", "--model=test-model"},
		strings.NewReader("/preset local\n"), false, &stdout, &stderr)
	wantDiagnostic := "gax: llama.cpp: failed to auto-discover model from " + server.URL +
		"/v1/models\ngax: is llama-server running? (set GAX_MODEL to skip probing, or adjust GAX_LLAMACPP_PORT / GAX_LLAMACPP_BASE_URL)\n"
	if code != 0 || stderr.String() != wantDiagnostic || strings.Contains(stdout.String(), "local ·") {
		t.Fatalf("preset discovery failure: code=%d stdout=%q stderr=%q, want stderr=%q",
			code, stdout.String(), stderr.String(), wantDiagnostic)
	}
}

func TestLoadPresetCoercesBooleanAndNumberScalars(t *testing.T) {
	store := config.New(config.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"presets":{"legacy":{"provider":true,"model":1.23456789,"effort":false}}}`)); err != nil {
		t.Fatal(err)
	}
	values, err := loadPreset(store, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if values["provider"] != "1" || values["model"] != "1.23457" || values["effort"] != "0" {
		t.Fatalf("preset scalar values = %#v, want provider=1 model=1.23457 effort=0", values)
	}
}

func TestLoadPresetTreatsNonObjectDefinitionAsUnknown(t *testing.T) {
	store := config.New(config.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"presets":{"draft":"unfinished"}}`)); err != nil {
		t.Fatal(err)
	}
	_, err := loadPreset(store, "draft")
	want := "unknown preset 'draft' (define a presets.draft block in config.json)"
	if err == nil || err.Error() != want {
		t.Fatalf("loadPreset error = %v, want %q", err, want)
	}
}

func TestLoadPresetRejectsUnreadablePromptFile(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	store := config.New(config.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"presets":{"broken":{"provider":"openai-compatible","model":"m","system_prompt":"@missing.txt"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPreset(store, "broken"); err == nil || !strings.Contains(err.Error(), "couldn't read prompt file") {
		t.Fatalf("loadPreset error = %v, want unreadable prompt file", err)
	}
}

func TestLoadPresetReportsInvalidMembersInConfigOrder(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"presets":{"broken":{"first_bad":true,"second_bad":false,"third_bad":1,"fourth_bad":"x"}}}`)); err != nil {
		t.Fatal(err)
	}
	want := "preset 'broken': 'first_bad' is not presettable (allowed: provider, model, effort, system_prompt, system_prompt_append, tint); endpoint settings belong in a providers.<name> block, context/recording in the --bare/--no-session flags"
	for attempt := 0; attempt < 40; attempt++ {
		_, err := loadPreset(store, "broken")
		if err == nil || err.Error() != want {
			t.Fatalf("loadPreset error on attempt %d = %v, want %q", attempt+1, err, want)
		}
	}
}

func TestRunREPLPresetSavePersistsAndActivatesLiveSelection(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"),
		[]byte(`{"keep":"value","presets":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "live-model")
	t.Setenv("GAX_EFFORT", "high")
	t.Setenv("GAX_SYSTEM_PROMPT", "live prompt override")
	t.Setenv("GAX_OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/preset-save review ROSE\n/session\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("save preset: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(configRoot, "gax", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Keep    string                       `json:"keep"`
		Presets map[string]map[string]string `json:"presets"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	preset := decoded.Presets["review"]
	if decoded.Keep != "value" || preset["provider"] != "openai-compatible" || preset["model"] != "live-model" ||
		preset["effort"] != "high" || preset["system_prompt"] != "live prompt override" || preset["tint"] != "rose" {
		t.Fatalf("saved definition did not capture the live selection: %s", data)
	}
	plainOutput := stripANSISequences(stdout.String())
	if !strings.Contains(plainOutput, "saved preset 'review'") || !strings.Contains(plainOutput, "[review]") ||
		!strings.Contains(plainOutput, "preset        review") {
		t.Fatalf("saved preset was not activated: %q", stdout.String())
	}
	stateData, err := os.ReadFile(filepath.Join(stateRoot, "gax", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(stateData, &state); err != nil || state["preset"] != "review" {
		t.Fatalf("active preset was not persisted: state=%s err=%v", stateData, err)
	}
}

func TestRunREPLNewWithPresetStartsFreshConversation(t *testing.T) {
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"review":{"provider":"openai-compatible","model":"review-model","system_prompt":"review-only system directive"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "initial-model")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	requestBodies := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		requestBodies <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("old conversation\n/new review\nnew conversation\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("new with preset: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Count(stdout.String(), "[review]") != 1 {
		t.Fatalf("/new preset should print one fresh preset banner: %q", stdout.String())
	}
	var first, second string
	select {
	case first = <-requestBodies:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the first request")
	}
	select {
	case second = <-requestBodies:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the request after /new")
	}
	if !strings.Contains(first, "old conversation") {
		t.Fatalf("first request omitted old conversation: %s", first)
	}
	if strings.Contains(second, "old conversation") || strings.Contains(second, "reply") {
		t.Fatalf("/new retained prior conversation context: %s", second)
	}
	if !strings.Contains(second, "review-only system directive") || !strings.Contains(second, "new conversation") {
		t.Fatalf("new preset request omitted new prompt or conversation: %s", second)
	}
}

func TestRunProviderUsageReportsUnsupportedProviders(t *testing.T) {
	store := config.New(config.DefaultSettings())
	openAI, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runProviderUsage(context.Background(), openAI, &transport.Client{}, 100, nil, false, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "/usage is not supported by the openai provider") || stderr.Len() != 0 {
		t.Fatalf("unsupported provider usage dispatch: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunProviderUsageDispatchesDeepSeek(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/balance" ||
			r.Header.Get("Authorization") != "Bearer deepseek-secret" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("DeepSeek usage request mismatch: method=%s path=%q headers=%v", r.Method, r.URL.Path, r.Header)
		}
		requests.Add(1)
		_, _ = io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50","granted_balance":"2.00"}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "deepseek-secret", name == "DEEPSEEK_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = server.URL
	var stdout, stderr strings.Builder
	runProviderUsage(context.Background(), provider, &transport.Client{HTTP: server.Client()}, 100, nil, false, &stdout, &stderr)
	if requests.Load() != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "deepseek\n  balance $12.50 · $2.00 granted") ||
		strings.Contains(stdout.String(), "deepseek-secret") {
		t.Fatalf("DeepSeek usage dispatch: requests=%d stdout=%q stderr=%q", requests.Load(), stdout.String(), stderr.String())
	}
}

func TestRunProviderUsageDispatchesOpenRouter(t *testing.T) {
	var keyCalls, creditCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer openrouter-secret" ||
			r.Header.Get("Accept") != "application/json" {
			t.Errorf("OpenRouter usage request mismatch: method=%s path=%q headers=%v", r.Method, r.URL.Path, r.Header)
		}
		switch r.URL.Path {
		case "/key":
			keyCalls.Add(1)
			_, _ = io.WriteString(w, `{"data":{"label":"primary","usage":0.00421,"limit":2,"limit_remaining":1.5}}`)
		case "/credits":
			creditCalls.Add(1)
			_, _ = io.WriteString(w, `{"data":{"total_credits":10,"total_usage":3}}`)
		default:
			t.Errorf("unexpected OpenRouter usage path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "OPENROUTER_API_KEY" {
			return "openrouter-secret", true
		}
		return "", false
	})
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = server.URL
	var stdout, stderr strings.Builder
	runProviderUsage(context.Background(), provider, &transport.Client{HTTP: server.Client()}, 100, nil, false, &stdout, &stderr)
	for _, want := range []string{"openrouter · primary", "  spent   $0.0042", "  limit   $2.00 · $1.50 remaining", "  credits $7.00 of $10.00 remaining"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("OpenRouter usage output omits %q: %q", want, stdout.String())
		}
	}
	if keyCalls.Load() != 1 || creditCalls.Load() != 1 || stderr.Len() != 0 || strings.Contains(stdout.String(), "openrouter-secret") {
		t.Fatalf("OpenRouter usage: key=%d credits=%d stdout=%q stderr=%q", keyCalls.Load(), creditCalls.Load(), stdout.String(), stderr.String())
	}
}

func TestRunProviderUsageKeepsOpenCodeGoBearerAuthWithAnthropicModelDialect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" || r.Header.Get("Authorization") != "Bearer opencode-secret" || r.Header.Get("x-api-key") != "" {
			t.Errorf("OpenCode Go usage auth should remain Bearer: path=%q headers=%v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"usage":{"weekly":{"percent":8,"resetsAt":"2099-08-24T00:00:00Z"}}}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetOverride("providers.opencode-go.api", stringPtr("anthropic-messages"))
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "OPENCODE_API_KEY" {
			return "opencode-secret", true
		}
		return "", false
	})
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Wire != runtime.WireAnthropic {
		t.Fatalf("provider wire = %q, want Anthropic metadata dialect", provider.Wire)
	}
	provider.BaseURL = server.URL
	var stdout, stderr strings.Builder
	runProviderUsage(context.Background(), provider, &transport.Client{HTTP: server.Client()}, 100, nil, false, &stdout, &stderr)
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "opencode-go") {
		t.Fatalf("OpenCode Go usage under Anthropic dialect: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunREPLUsageQueriesOpenCodeGo(t *testing.T) {
	previousHTTP := transport.DefaultClient.HTTP
	transport.DefaultClient.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://opencode.ai/zen/go/v1/usage" ||
			request.Header.Get("Authorization") != "Bearer opencode-test-key" || request.Header.Get("Accept") != "application/json" ||
			request.Header.Get("x-opencode-client") != "gax" {
			t.Errorf("OpenCode Go usage request mismatch: method=%s url=%q headers=%v", request.Method, request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"usage":{"weekly":{"percent":54,"resetsAt":"2099-08-24T00:00:00Z"}}}`)), Request: request,
		}, nil
	})}
	defer func() { transport.DefaultClient.HTTP = previousHTTP }()
	t.Setenv("GAX_PROVIDER", "opencode-go")
	t.Setenv("OPENCODE_API_KEY", "opencode-test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/usage\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "opencode-go\n  weekly ") ||
		!strings.Contains(stdout.String(), "  54% used · resets ") || strings.Contains(stdout.String(), "opencode-test-key") {
		t.Fatalf("REPL OpenCode Go /usage: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunREPLUsageQueriesCodexUsingCodexCLIAuth(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"codex-access-secret","account_id":"codex-account","id_token":"header.eyJlbWFpbCI6ImNvZGV4QGV4YW1wbGUuY29tIn0.signature"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousHTTP := transport.DefaultClient.HTTP
	transport.DefaultClient.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://chatgpt.com/backend-api/wham/usage" ||
			request.Header.Get("Authorization") != "Bearer codex-access-secret" ||
			request.Header.Get("chatgpt-account-id") != "codex-account" || request.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("Codex usage request mismatch: method=%s url=%q headers=%v", request.Method, request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":13,"reset_at":1800000000,"limit_window_seconds":86400}}}`)), Request: request,
		}, nil
	})}
	defer func() { transport.DefaultClient.HTTP = previousHTTP }()
	t.Setenv("HOME", home)
	t.Setenv("GAX_PROVIDER", "codex")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run(nil, strings.NewReader("/usage\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "codex · codex@example.com · plus") ||
		!strings.Contains(stdout.String(), "daily ") || strings.Contains(stdout.String(), "codex-access-secret") {
		t.Fatalf("REPL Codex /usage: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestRunREPLSessionShowsRecordedUsageSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":400}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("hello\nsecond prompt\n/undo 1\n/session\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("REPL /session: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"session", "provider", "openai-compatible", "test-model", "1 · 1 undone", "requests", "2", "tokens", "in 4k", "out 800", "time worked"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("/session output omits %q: %q", want, stdout.String())
		}
	}
	entries, err := session.List(cwd, 30)
	if err != nil || len(entries) != 1 || entries[0].ID == nil || !strings.Contains(stdout.String(), *entries[0].ID) {
		t.Fatalf("/session id does not match the recorded session: entries=%+v err=%v output=%q", entries, err, stdout.String())
	}
	requestCount := ""
	for _, line := range strings.Split(stripANSISequences(stdout.String()), "\n") {
		if strings.HasPrefix(line, "  requests") {
			requestCount = strings.TrimSpace(strings.TrimPrefix(line, "  requests"))
		}
	}
	if requestCount != "2" {
		t.Fatalf("/session request count = %q, output=%q", requestCount, stdout.String())
	}
	if strings.Contains(stderr.String(), "not available") {
		t.Fatalf("/session still reported unsupported: %q", stderr.String())
	}
}

func TestSessionSummaryResolvesEffortFromModelMetadata(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("catalog.url", stringPtr(""))
	store.SetOverride("effort", stringPtr("max"))
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openai-compatible":{"test-model":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	state := &agent.Session{
		ProviderID: stringPtr("openai-compatible"),
		Model:      stringPtr("test-model"),
	}
	var output strings.Builder
	printSessionSummaryWithStyle(&output, state, provider, resolver, nil, store, false)
	if !strings.Contains(output.String(), "openai-compatible · test-model · high") {
		t.Fatalf("session summary did not refresh resolved effort: %q", output.String())
	}
}

func TestRunREPLNewStartsFreshRecordedConversation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if request == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"sleep 30\",\"background\":true,\"name\":\"new-job\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	t.Setenv("GAX_BASH_BACKGROUND_YIELD", "5ms")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("first request\n/new\nsecond request\n"), false, &stdout, &stderr)
	if code != 0 || requests.Load() != 3 || stderr.Len() != 0 {
		t.Fatalf("REPL /new: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	if strings.Count(stdout.String(), "ctrl-d quit") != 2 {
		t.Fatalf("startup and /new should each print one banner: %q", stdout.String())
	}
	entries, err := session.List(cwd, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("session count after /new = %d, want 2", len(entries))
	}
	byPrompt := make(map[string]int)
	oldConversationHasTaskNote := false
	for _, entry := range entries {
		loaded, err := session.LoadAll(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		var prompts []string
		for _, item := range loaded.Items {
			if item.Kind == conversation.KindUser && item.Text != nil {
				if item.Origin == conversation.OriginTaskNote {
					if strings.Contains(*item.Text, "[task new-job killed at exit]") {
						oldConversationHasTaskNote = true
					}
					continue
				}
				prompts = append(prompts, *item.Text)
			}
		}
		if len(prompts) != 1 {
			t.Fatalf("session %v has prompts %v, want exactly one", entry.ID, prompts)
		}
		byPrompt[prompts[0]]++
	}
	if byPrompt["first request"] != 1 || byPrompt["second request"] != 1 {
		t.Fatalf("session prompts after /new = %v", byPrompt)
	}
	if !oldConversationHasTaskNote {
		t.Fatal("/new did not persist the old conversation's task exit note")
	}
}

func TestRunREPLUndoRemovesLiveTailAndRetiresItInSessionLog(t *testing.T) {
	requests := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("first request\nsecond request\n/undo 1\nthird request\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("REPL /undo: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	firstBody, secondBody, thirdBody := <-requests, <-requests, <-requests
	if !strings.Contains(firstBody, "first request") || !strings.Contains(secondBody, "second request") ||
		!strings.Contains(thirdBody, "first request") || strings.Contains(thirdBody, "second request") ||
		!strings.Contains(thirdBody, "third request") {
		t.Fatalf("provider histories after undo are wrong:\nfirst=%s\nsecond=%s\nthird=%s", firstBody, secondBody, thirdBody)
	}
	entries, err := session.List(cwd, 30)
	if err != nil || len(entries) != 1 {
		t.Fatalf("session list after undo: entries=%d err=%v", len(entries), err)
	}
	loaded, err := session.LoadAll(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var live, retired []string
	for _, item := range loaded.Items {
		if item.Kind == conversation.KindUser && item.Text != nil {
			live = append(live, *item.Text)
		}
	}
	for _, item := range loaded.Retired {
		if item.Kind == conversation.KindUser && item.Text != nil {
			retired = append(retired, *item.Text)
		}
	}
	if strings.Join(live, ",") != "first request,third request" || strings.Join(retired, ",") != "second request" {
		t.Fatalf("session history after undo: live=%v retired=%v", live, retired)
	}
}

func TestRunREPLForkCreatesInheritedBranchAndKeepsSource(t *testing.T) {
	requests := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	var stdout, stderr strings.Builder
	code := Run([]string{"--provider=openai-compatible", "--model=test-model"},
		strings.NewReader("first request\nsecond request\n/fork 1\nthird request\n"), false, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("REPL /fork: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	firstBody, secondBody, thirdBody := <-requests, <-requests, <-requests
	if !strings.Contains(firstBody, "first request") || !strings.Contains(secondBody, "second request") ||
		!strings.Contains(thirdBody, "first request") || strings.Contains(thirdBody, "second request") ||
		!strings.Contains(thirdBody, "third request") {
		t.Fatalf("provider histories after fork are wrong:\nfirst=%s\nsecond=%s\nthird=%s", firstBody, secondBody, thirdBody)
	}
	entries, err := session.List(cwd, 30)
	if err != nil || len(entries) != 2 {
		t.Fatalf("session list after fork: entries=%d err=%v", len(entries), err)
	}
	var source, branch *session.Loaded
	for _, entry := range entries {
		loaded, err := session.LoadAll(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Meta.ForkedFrom == nil {
			copy := loaded
			source = &copy
		} else {
			copy := loaded
			branch = &copy
		}
	}
	if source == nil || branch == nil {
		t.Fatalf("could not distinguish source and branch sessions: %+v", entries)
	}
	if got := typedPrompts(source.Items); strings.Join(got, ",") != "first request,second request" {
		t.Errorf("source prompts changed: %v", got)
	}
	if got := typedPrompts(branch.Items); strings.Join(got, ",") != "first request,third request" {
		t.Errorf("branch prompts = %v", got)
	}
	if len(branch.Retired) != 0 {
		t.Errorf("fork retained retired source history: %+v", branch.Retired)
	}
	for _, item := range branch.Items {
		if item.Kind == conversation.KindUser && item.Text != nil && *item.Text == "first request" && !item.Inherited {
			t.Error("copied prompt is not marked inherited")
		}
	}
}

func TestHandleForkZeroClonesTheCurrentTip(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	provider, model := "openai-compatible", "model"
	selection := session.Selection{Provider: &provider, Model: &model}
	log, err := session.NewLog(cwd, selection)
	if err != nil {
		t.Fatal(err)
	}
	state := agent.NewSession()
	state.ProviderID = &provider
	state.Model = &model
	state.AddUser("current tip")
	state.WorkedMS = 500
	state.LastUserTurnMS = 200
	if err := log.Append(state.Items); err != nil {
		t.Fatal(err)
	}
	oldID := log.ID()
	var stdout, stderr strings.Builder
	forked, err := handleFork(slash.Parse("/fork 0"), state, log, selection, &stdout, &stderr, nil, false)
	if err != nil || forked == nil || stderr.Len() != 0 {
		t.Fatalf("fork current tip: log=%v err=%v stdout=%q stderr=%q", forked, err, stdout.String(), stderr.String())
	}
	defer forked.Close()
	if len(state.Items) != 2 || !state.Items[1].Inherited || state.WorkedMS != 0 || state.LastUserTurnMS != -1 || len(state.Retired) != 0 {
		t.Fatalf("forked live state = %+v", state)
	}
	loaded, err := session.LoadAll(forked.Path())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.ID == nil || *loaded.Meta.ID == *oldID || loaded.Meta.ForkedFrom == nil || *loaded.Meta.ForkedFrom != *oldID ||
		len(loaded.Items) != 2 || !loaded.Items[1].Inherited {
		t.Fatalf("forked session = %+v", loaded)
	}
}

func TestHandleForkRequiresSessionRecording(t *testing.T) {
	provider, model := "openai-compatible", "model"
	state := agent.NewSession()
	state.ProviderID = &provider
	state.Model = &model
	state.AddUser("keep source")
	before := len(state.Items)
	var stdout, stderr strings.Builder
	forked, err := handleFork(slash.Parse("/fork 0"), state, nil,
		session.Selection{Provider: &provider, Model: &model}, &stdout, &stderr, nil, false)
	if err != nil || forked != nil || len(state.Items) != before || len(state.Retired) != 0 ||
		!strings.Contains(stderr.String(), "needs session recording") {
		t.Fatalf("fork without recording changed state: log=%v err=%v state=%+v stdout=%q stderr=%q", forked, err, state, stdout.String(), stderr.String())
	}
}

func typedPrompts(items []conversation.Item) []string {
	var prompts []string
	for _, item := range items {
		if conversation.IsTypedPrompt(item) && item.Text != nil {
			prompts = append(prompts, *item.Text)
		}
	}
	return prompts
}

func TestTypedUserTurnCountSkipsNonPromptUserItems(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("seed"), Origin: conversation.OriginCompactSeed},
		{Kind: conversation.KindUser, Text: conversation.String("continue"), Origin: conversation.OriginContinuation},
		{Kind: conversation.KindUser, Text: conversation.String("typed prompt")},
	}
	if got := typedUserTurnCount(items); got != 1 {
		t.Fatalf("typed user turn count = %d, want 1", got)
	}
}

func TestHandleUndoRejectsOutOfRangeTurnCountWithoutMutation(t *testing.T) {
	state := agent.NewSession()
	state.AddUser("keep me")
	before := len(state.Items)
	parsed := slash.Parse("/undo 0")
	var stdout, stderr strings.Builder
	handleUndo(parsed, state, nil, &stdout, &stderr, nil, false)
	if len(state.Items) != before || len(state.Retired) != 0 || stdout.String() != "/undo takes a number of turns between 1 and 1\n" || stderr.Len() != 0 {
		t.Fatalf("invalid undo changed state or lacked error: live=%+v retired=%+v stdout=%q stderr=%q", state.Items, state.Retired, stdout.String(), stderr.String())
	}
}

func TestHandleForkRejectsOutOfRangeTurnCountWithoutMutation(t *testing.T) {
	provider, model := "openai-compatible", "model"
	state := agent.NewSession()
	state.ProviderID = &provider
	state.Model = &model
	state.AddUser("keep me")
	before := len(state.Items)
	var stdout, stderr strings.Builder
	forked, err := handleFork(slash.Parse("/fork 2"), state, nil,
		session.Selection{Provider: &provider, Model: &model}, &stdout, &stderr, nil, false)
	if err != nil || forked != nil || len(state.Items) != before || len(state.Retired) != 0 ||
		stdout.String() != "/fork takes a number of turns between 0 and 1\n" || stderr.Len() != 0 {
		t.Fatalf("invalid fork changed state or lacked error: fork=%v err=%v live=%+v retired=%+v stdout=%q stderr=%q", forked, err, state.Items, state.Retired, stdout.String(), stderr.String())
	}
}

func TestRunJSONEmitsHeaderNewItemsAndResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":10,\"cost\":0.003}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "on")
	var stdout, stderr strings.Builder
	code := Run([]string{"--json", "--provider=openai-compatible", "--model=test-model", "hello"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON run: code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 5 {
		t.Fatalf("JSON stream omitted session items or result: %q", stdout.String())
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil || string(header["type"]) != `"session"` {
		t.Fatalf("JSON session header = %s, err=%v", lines[0], err)
	}
	var sawUser, sawAnswer, sawResult bool
	for _, line := range lines[1:] {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON record %q: %v", line, err)
		}
		if string(record["kind"]) == `"user"` {
			sawUser = true
		}
		if string(record["kind"]) == `"assistant"` {
			var text string
			_ = json.Unmarshal(record["text"], &text)
			if text == "answer" {
				sawAnswer = true
			}
		}
		if string(record["type"]) == `"result"` {
			decoder := json.NewDecoder(strings.NewReader(line))
			if _, err := decoder.Token(); err != nil {
				t.Fatal(err)
			}
			var keys []string
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, keyToken.(string))
				var value json.RawMessage
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
			}
			wantKeys := []string{"type", "outcome", "text", "turns", "elapsed_ms", "context_tokens", "cost"}
			if !reflect.DeepEqual(keys, wantKeys) {
				t.Fatalf("JSON result field order = %v, want %v", keys, wantKeys)
			}
			var text, outcome string
			_ = json.Unmarshal(record["text"], &text)
			_ = json.Unmarshal(record["outcome"], &outcome)
			if text == "answer" && outcome == "complete" {
				var cost float64
				_ = json.Unmarshal(record["cost"], &cost)
				if cost != 0.003 || record["cost_estimated"] != nil {
					t.Fatalf("JSON result cost = %s, estimated=%s; want exact $0.003", record["cost"], record["cost_estimated"])
				}
				sawResult = true
			}
		}
	}
	if !sawUser || !sawAnswer || !sawResult {
		t.Fatalf("JSON stream records missing: user=%t answer=%t result=%t (%q)", sawUser, sawAnswer, sawResult, stdout.String())
	}
}

func TestRunOneShotElapsedExcludesStartupModelProbe(t *testing.T) {
	const probeDelay = 600 * time.Millisecond
	const streamDelay = 120 * time.Millisecond
	var probeRequests atomic.Int32
	var streamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			probeRequests.Add(1)
			time.Sleep(probeDelay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"probe-model","type":"model","display_name":"probe-model","context_window":16384,"max_tokens":2048}],"has_more":false}`)
		case http.MethodPost:
			streamRequests.Add(1)
			time.Sleep(streamDelay)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture stop"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	configDir := filepath.Join(root, "config", "gax")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := `{"providers":{"openai-compatible":{"api":"anthropic-messages","metadata_api":"anthropic"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_KEEP_AWAKE", "off")
	t.Setenv("GAX_NO_SESSION", "off")

	var stdout, stderr strings.Builder
	code := Run([]string{"--json", "--provider=openai-compatible", "--model=probe-model", "hello"},
		strings.NewReader(""), false, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 || probeRequests.Load() != 1 || streamRequests.Load() != 1 {
		t.Fatalf("run: code=%d probeRequests=%d streamRequests=%d stderr=%q stdout=%q",
			code, probeRequests.Load(), streamRequests.Load(), stderr.String(), stdout.String())
	}
	var elapsedMS int64 = -1
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var record struct {
			Type      string `json:"type"`
			ElapsedMS int64  `json:"elapsed_ms"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON record %q: %v", line, err)
		}
		if record.Type == "result" {
			elapsedMS = record.ElapsedMS
		}
	}
	if elapsedMS < 80 || elapsedMS >= 300 {
		t.Fatalf("JSON result elapsed_ms = %d, want the 120 ms provider request but not the 600 ms startup model probe", elapsedMS)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := session.List(cwd, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("recorded sessions = %d, err=%v; want one run", len(entries), err)
	}
	loaded, err := session.LoadAll(entries[0].Path)
	if err != nil || loaded.LastUserTurnMS == nil || *loaded.LastUserTurnMS < 80 || *loaded.LastUserTurnMS >= 300 {
		var lastTurn any
		if loaded.LastUserTurnMS != nil {
			lastTurn = *loaded.LastUserTurnMS
		}
		t.Fatalf("recorded user-turn elapsed = %v, err=%v; want the provider request without the startup probe", lastTurn, err)
	}
}

func TestJSONRecordsLeaveHTMLCharactersUnescaped(t *testing.T) {
	var stdout strings.Builder
	stream := jsonRun{writer: &stdout}
	if err := stream.record(map[string]string{"text": "<tag> & value"}); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "{\"text\":\"<tag> & value\"}\n"; got != want {
		t.Fatalf("JSON record = %q, want %q", got, want)
	}
}

func TestRunJSONResultMarksCatalogCostAsEstimated(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	configDir := filepath.Join(configRoot, "gax")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := `{"catalog":{"models":{"openai-compatible":{"test-model":{"cost":{"input":2,"output":8}}}}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	code := Run([]string{"--json", "--provider=openai-compatible", "--model=test-model", "hello"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON run: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var result map[string]json.RawMessage
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON record %q: %v", line, err)
		}
		if string(record["type"]) == `"result"` {
			result = record
		}
	}
	var cost float64
	var estimated bool
	if result == nil || json.Unmarshal(result["cost"], &cost) != nil ||
		json.Unmarshal(result["cost_estimated"], &estimated) != nil || math.Abs(cost-0.00208) > 1e-12 || !estimated {
		t.Fatalf("JSON result should report estimated cost $0.00208: %s", stdout.String())
	}
}

func TestRunJSONEmitsTaskExitNoteBeforeResult(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GAX_NO_SESSION", "on")
	t.Setenv("GAX_BASH_BACKGROUND_YIELD", "5ms")
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\",\\\"background\\\":true,\\\"name\\\":\\\"json-exit-job\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	var stdout, stderr strings.Builder
	code := Run([]string{"--json", "--provider=openai-compatible", "--model=test-model", "start"},
		strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || requests.Load() != 2 || stderr.Len() != 0 {
		t.Fatalf("JSON run: code=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	noteIndex, resultIndex := -1, -1
	for index, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON record %q: %v", line, err)
		}
		var kind, origin, text, recordType string
		_ = json.Unmarshal(record["kind"], &kind)
		_ = json.Unmarshal(record["origin"], &origin)
		_ = json.Unmarshal(record["text"], &text)
		_ = json.Unmarshal(record["type"], &recordType)
		if kind == "user" && origin == string(conversation.OriginTaskNote) && strings.Contains(text, "[task json-exit-job killed at exit]") {
			noteIndex = index
		}
		if recordType == "result" {
			resultIndex = index
		}
	}
	if noteIndex < 0 || resultIndex <= noteIndex || resultIndex != len(lines)-1 {
		t.Fatalf("task exit note must precede the final result record: note=%d result=%d records=%q", noteIndex, resultIndex, stdout.String())
	}
}

func TestRunVersionDoesNotRequireProviderConfiguration(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := Run([]string{"--version"}, strings.NewReader(""), true, &stdout, &stderr); code != 0 {
		t.Fatalf("version exit code = %d (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "gax ") || stderr.Len() != 0 {
		t.Fatalf("version output = %q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunHelpDescribesJSONLAndResumeOptions(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := Run([]string{"--help"}, strings.NewReader(""), true, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"JSON records", "--continue", "--resume[=ID]", "--preset=NAME"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help output omits %q: %s", want, stdout.String())
		}
	}
}

func TestRunContinuesAfterMalformedConfigAndStateFiles(t *testing.T) {
	configRoot, stateRoot := t.TempDir(), t.TempDir()
	configDir, stateDir := filepath.Join(configRoot, "gax"), filepath.Join(stateRoot, "gax")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath, statePath := filepath.Join(configDir, "config.json"), filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(configPath, []byte(`{"unfinished":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)

	var stdout, stderr strings.Builder
	if code := Run([]string{"--help"}, strings.NewReader(""), true, &stdout, &stderr); code != 0 {
		t.Fatalf("malformed config/state stopped startup: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"ignoring malformed config at " + configPath + " (expected a JSON object)",
		"ignoring malformed state at " + statePath + " (expected a JSON object)",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("startup warning omits %q: %q", want, stderr.String())
		}
	}
}

func TestLoadStoreRejectsOversizedConfigWithoutOverwriting(t *testing.T) {
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "gax")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.json")
	original := make([]byte, config.MaxTierFileBytes+1)
	copy(original, `{"theme":"light"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var stderr strings.Builder
	store, err := loadStore(&stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistConfig("theme", stringPtr("dark")); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("oversized config write error = %v, want refusal", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("oversized config changed: size=%d err=%v", len(got), err)
	}
	if want := "ignoring config at " + path + ": larger than the 1 MiB limit"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("oversized config warning = %q, want %q", stderr.String(), want)
	}
}

func TestJSONRecordRejectsShortWrite(t *testing.T) {
	stream := &jsonRun{writer: shortWriter{}}
	if err := stream.record(map[string]string{"type": "session"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("record short write error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestApplySelectionSuppressesConfigPresetForExplicitSelection(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"preset":"default","presets":{"default":{"provider":"openai-compatible","model":"preset-model","system_prompt":"preset prompt"}}}`)); err != nil {
		t.Fatal(err)
	}
	model := "cli-model"
	if err := applySelection(store, cli.Options{Selection: cli.Selection{Model: &model}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.StringNonempty("model"); got == nil || *got != model {
		t.Fatalf("model = %v, want %q", got, model)
	}
	if got := store.String("provider"); got != nil {
		t.Fatalf("config preset should not supply provider for an explicit model: %q", *got)
	}
	if got := store.String("system_prompt"); got != nil {
		t.Fatalf("suppressed preset system prompt = %q", *got)
	}
}

func TestApplySelectionLayersExplicitModelOverNamedPreset(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"presets":{"review":{"provider":"openai-compatible","model":"preset-model","system_prompt":"review prompt"}}}`)); err != nil {
		t.Fatal(err)
	}
	preset, model := "review", "cli-model"
	if err := applySelection(store, cli.Options{Selection: cli.Selection{Preset: &preset, Model: &model}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.StringNonempty("model"); got == nil || *got != model {
		t.Fatalf("model = %v, want explicit %q", got, model)
	}
	if got := store.StringNonempty("provider"); got == nil || *got != "openai-compatible" {
		t.Fatalf("provider = %v, want preset provider", got)
	}
	if got := store.String("system_prompt"); got == nil || *got != "review prompt" {
		t.Fatalf("system_prompt = %v, want preset prompt", got)
	}
}

func TestApplySelectionSupportsDottedPresetNames(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"presets":{"review.fast":{"provider":"openai-compatible","model":"quick"}}}`)); err != nil {
		t.Fatal(err)
	}
	preset := "review.fast"
	if err := applySelection(store, cli.Options{Selection: cli.Selection{Preset: &preset}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.StringNonempty("model"); got == nil || *got != "quick" {
		t.Fatalf("dotted preset model = %v", got)
	}
}

func TestDottedPresetConfigLookupFallsThroughUnrelatedStatePresetBlock(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"presets":{"review.fast":{"description":"Review changes","tint":"rose","provider":"openai-compatible","model":"quick"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"presets":{"daily":{"provider":"mock"}}}`)); err != nil {
		t.Fatal(err)
	}

	values, err := loadPreset(store, "review.fast")
	if err != nil {
		t.Fatalf("loadPreset did not find config definition beneath unrelated state presets: %v", err)
	}
	if values["model"] != "quick" || savedPresetDescription("review.fast", true, store) == nil ||
		*savedPresetDescription("review.fast", true, store) != "Review changes" ||
		savedPresetTint("review.fast", store) == nil || *savedPresetTint("review.fast", store) != "rose" {
		t.Fatalf("dotted preset fields were not resolved literally: values=%v description=%v tint=%v",
			values, savedPresetDescription("review.fast", true, store), savedPresetTint("review.fast", store))
	}
	if got := validPresetNames(store); !reflect.DeepEqual(got, []string{"review.fast", "daily"}) {
		t.Fatalf("valid preset names = %v, want config and state definitions", got)
	}
}

func TestApplySelectionWarnsAndClearsInvalidConfigPreset(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"preset":"broken","presets":{"broken":{"provider":"openai-compatible","command":"rm"}}}`)); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	if err := applySelection(store, cli.Options{}, func(message string) { warnings = append(warnings, message) }); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "starting without it") {
		t.Fatalf("invalid config preset warnings = %v", warnings)
	}
	if got := store.String("preset"); got == nil || *got != "" {
		t.Fatalf("invalid preset remained active: %v", got)
	}
}

func TestLoadResumeRestoresPresetConversationTier(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	provider, model, preset := "openai-compatible", "saved-model", "saved"
	log, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &model, Preset: &preset})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	id := log.ID()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"presets":{"saved":{"provider":"openai-compatible","model":"preset-model","system_prompt":"restored prompt"}}}`)); err != nil {
		t.Fatal(err)
	}
	prefix := (*id)[:8]
	loaded, _, err := loadResume(cli.Options{ResumeMode: cli.ResumeSelect, ResumeID: &prefix}, store)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("resume returned no session")
	}
	if got := store.StringNonempty("model"); got == nil || *got != "preset-model" {
		t.Fatalf("restored model = %v, want preset model", got)
	}
	if got := store.String("system_prompt"); got == nil || *got != "restored prompt" {
		t.Fatalf("restored system prompt = %v", got)
	}
}

func TestLoadResumeClearsPreviousPresetPromptWhenTargetHasNoPreset(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	provider, firstModel, preset := "openai-compatible", "first-model", "review"
	firstLog, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &firstModel, Preset: &preset})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstLog.Begin(); err != nil {
		t.Fatal(err)
	}
	firstPath := firstLog.Path()
	if err := firstLog.Close(); err != nil {
		t.Fatal(err)
	}
	secondModel := "second-model"
	secondLog, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &secondModel})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondLog.Begin(); err != nil {
		t.Fatal(err)
	}
	secondPath := secondLog.Path()
	if err := secondLog.Close(); err != nil {
		t.Fatal(err)
	}

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"system_prompt":"base prompt","presets":{"review":{"provider":"openai-compatible","system_prompt":"preset prompt"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadResumePath(firstPath, cli.Options{}, store); err != nil {
		t.Fatal(err)
	}
	if got := store.String("system_prompt"); got == nil || *got != "preset prompt" {
		t.Fatalf("first resume prompt = %v, want preset prompt", got)
	}
	if _, _, err := loadResumePath(secondPath, cli.Options{}, store); err != nil {
		t.Fatal(err)
	}
	if got := store.String("system_prompt"); got == nil || *got != "base prompt" {
		t.Fatalf("second resume inherited stale preset prompt: got %v, want base prompt", got)
	}
	if got := currentPreset(store); got != nil {
		t.Fatalf("second resume retained preset %q", *got)
	}
}

func TestLoadResumeInvalidPresetWarnsInteractivelyAndFailsOneShot(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	provider, model, preset := "openai-compatible", "saved-model", "missing"
	log, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &model, Preset: &preset})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	path := log.Path()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	store.SetConversation("system_prompt", stringPtr("stale prompt"))
	if err := store.LoadConfig([]byte(`{"system_prompt":"base prompt"}`)); err != nil {
		t.Fatal(err)
	}
	loaded, gotPath, err := loadResumePath(path, cli.Options{}, store)
	var warning *resumePresetWarning
	if !errors.As(err, &warning) {
		t.Fatalf("interactive restore error = %v, want resume preset warning", err)
	}
	if loaded == nil || gotPath != path || warning.Error() != "unknown preset 'missing' (define a presets.missing block in config.json) — resuming without it" {
		t.Fatalf("interactive restore = loaded:%v path:%q warning:%q", loaded != nil, gotPath, warning)
	}
	if got := store.StringNonempty("model"); got == nil || *got != model {
		t.Fatalf("base session model was not retained after preset failure: %v", got)
	}
	if got := store.String("system_prompt"); got == nil || *got != "base prompt" {
		t.Fatalf("stale preset prompt survived preset failure: %v", got)
	}

	_, _, err = loadResumePath(path, cli.Options{OneShot: true}, store)
	wantFatal := "unknown preset 'missing' (define a presets.missing block in config.json) (recorded by this session; pass --preset or --model to run it differently)"
	if err == nil || err.Error() != wantFatal {
		t.Fatalf("one-shot restore error = %v, want %q", err, wantFatal)
	}
}

func TestLoadResumeLatestWithoutSessionsStartsFreshInteractively(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	loaded, path, err := loadResume(cli.Options{ResumeMode: cli.ResumeLatest}, store)
	if err != nil || loaded != nil || path != "" {
		t.Fatalf("resume latest with no sessions: loaded=%v path=%q err=%v", loaded, path, err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func TestRunResumesNamedSessionWithContinuation(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	home := filepath.Join(t.TempDir(), "home")
	stateRoot := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("GAX_NO_SESSION", "on")
	provider, model := "openai-compatible", "resumed-model"
	log, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append([]conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("earlier request")}}); err != nil {
		t.Fatal(err)
	}
	id := log.ID()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	gotPrompt := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPrompt <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	var stdout, stderr strings.Builder
	code := Run([]string{"-p", "--resume=" + *id}, strings.NewReader(""), true, &stdout, &stderr)
	if code != 0 || stdout.String() != "continued\n" ||
		stderr.String() != "gax: openai-compatible · resumed-model (resumed)\n\n" {
		t.Fatalf("resume result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	request := <-gotPrompt
	if !strings.Contains(request, "earlier request") || !strings.Contains(request, "[continue]") || !strings.Contains(request, model) {
		t.Fatalf("resume request did not restore prior context and selection: %s", request)
	}
}

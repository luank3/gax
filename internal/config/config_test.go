package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreResolutionAndSources(t *testing.T) {
	store := New([]Setting{
		{Key: "provider", EnvVar: "GAX_PROVIDER", KeepEmpty: true},
		{Key: "model", EnvVar: "GAX_MODEL", KeepEmpty: true},
		{Key: "flag", EnvVar: "GAX_FLAG", Kind: KindBool, Default: "false"},
	})
	env := map[string]string{"GAX_MODEL": "environment-model", "GAX_FLAG": "true"}
	store.SetEnvironment(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if err := store.LoadConfig([]byte(`{"model":"file-model"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"model":"state-model"}`)); err != nil {
		t.Fatal(err)
	}
	if got := store.String("model"); got == nil || *got != "environment-model" || store.Source("model") != "env" {
		t.Fatalf("environment should override state and config, got %v from %s", got, store.Source("model"))
	}
	store.SetOverride("model", stringPtr("run-model"))
	if got := store.String("model"); got == nil || *got != "run-model" || store.Source("model") != "run" {
		t.Fatalf("run override should win, got %v from %s", got, store.Source("model"))
	}
	store.SetOverride("model", nil)
	if got := store.String("flag"); got == nil || *got != "true" || !store.Bool("flag") {
		t.Fatalf("boolean environment value did not resolve: %v", got)
	}
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if got := store.String("model"); got == nil || *got != "state-model" || store.Source("model") != "state" {
		t.Fatalf("state should override config after environment is removed, got %v", got)
	}
}

func TestStoreNestedDottedScalarsAndDefaultSentinel(t *testing.T) {
	store := New([]Setting{{Key: "providers.demo.port", Default: "8080"}, {Key: "count", Default: "5"}, {Key: "ratio"}})
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"port":7070}},"providers.demo.port":"6060","count":true,"ratio":1.23456789}`)); err != nil {
		t.Fatal(err)
	}
	if got := store.String("providers.demo.port"); got == nil || *got != "6060" {
		t.Fatalf("flat dotted key should win nested spelling, got %v", got)
	}
	if got := store.String("count"); got == nil || *got != "1" {
		t.Fatalf("boolean scalar coercion changed: %v", got)
	}
	if got := store.String("ratio"); got == nil || *got != "1.23457" {
		t.Fatalf("real scalar formatting should follow printf %%g: %v", got)
	}
	store.SetOverride("providers.demo.port", stringPtr("(default)"))
	if got := store.String("providers.demo.port"); got == nil || *got != "8080" || store.Source("providers.demo.port") != "run" {
		t.Fatalf("default sentinel should stop at the run tier, got %v source=%s", got, store.Source("providers.demo.port"))
	}
}

func TestProviderBoundSelectionAndEmptyValues(t *testing.T) {
	store := New([]Setting{
		{Key: "provider", KeepEmpty: true},
		{Key: "model", KeepEmpty: true},
		{Key: "effort", KeepEmpty: true},
		{Key: "prompt", KeepEmpty: true},
		{Key: "ordinary"},
	})
	if err := store.LoadConfig([]byte(`{"provider":"openai","model":"openai-model","effort":"high","prompt":"file-prompt","ordinary":"file-value"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"provider":"llama.cpp","model":"local-model","effort":"low"}`)); err != nil {
		t.Fatal(err)
	}
	store.SetConversation("provider", stringPtr("openai"))
	if got := store.String("model"); got == nil || *got != "openai-model" {
		t.Fatalf("selection bound to another provider leaked: %v", got)
	}
	store.SetConversation("provider", stringPtr("llamacpp"))
	if got := store.String("model"); got == nil || *got != "local-model" {
		t.Fatalf("canonical provider alias should restore its bound selection: %v", got)
	}
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "PROMPT" {
			return "", true
		}
		if key == "ORDINARY" {
			return "", true
		}
		return "", false
	})
	store.settings["prompt"] = Setting{Key: "prompt", EnvVar: "PROMPT", KeepEmpty: true}
	store.settings["ordinary"] = Setting{Key: "ordinary", EnvVar: "ORDINARY"}
	if got := store.String("prompt"); got == nil || *got != "" {
		t.Fatalf("meaningful empty environment value should be preserved: %v", got)
	}
	if got := store.String("ordinary"); got == nil || *got != "file-value" {
		t.Fatalf("empty non-meaningful environment value should fall through: %v", got)
	}
}

func TestInvalidLoadClearsOnlyThatTier(t *testing.T) {
	store := New([]Setting{{Key: "value", Default: "default"}})
	if err := store.LoadConfig([]byte(`{"value":"file"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadConfig([]byte(`[1,2]`)); err == nil {
		t.Fatal("array root should be rejected")
	}
	if got := store.String("value"); got == nil || *got != "default" {
		t.Fatalf("invalid config load must clear the config tier, got %v", got)
	}
	if err := store.LoadState([]byte(`{ invalid`)); err == nil {
		t.Fatal("malformed JSON should be rejected")
	}
}

func TestTypedParsingUsesBoundsAndUpstreamUnits(t *testing.T) {
	store := New([]Setting{
		{Key: "count", Kind: KindInt, Default: "4", Min: 1, Max: 10},
		{Key: "size", Kind: KindSize, Default: "50k"},
		{Key: "tokens", Kind: KindTokens, Default: "32k"},
		{Key: "delay", Kind: KindDuration, Default: "1s", Min: 1},
	})
	if err := store.LoadConfig([]byte(`{"count":"20","size":"2M","tokens":"64k","delay":"250ms"}`)); err != nil {
		t.Fatal(err)
	}
	if store.Int("count") != 4 || store.Size("size") != 2*1024*1024 || store.Tokens("tokens") != 64000 || store.Duration("delay") != 250*time.Millisecond {
		t.Fatalf("typed values or units differ: int=%d size=%d tokens=%d duration=%s", store.Int("count"), store.Size("size"), store.Tokens("tokens"), store.Duration("delay"))
	}
	if err := store.LoadConfig([]byte(`{"delay":"0"}`)); err != nil {
		t.Fatal(err)
	}
	if store.Duration("delay") != time.Second {
		t.Fatalf("out-of-range duration should fall back to default: %s", store.Duration("delay"))
	}
}

func TestIntParsingMatchesUpstreamStrtolAndCIntBounds(t *testing.T) {
	setting := Setting{Key: "count", Kind: KindInt, Default: "4"}
	store := New([]Setting{setting})
	for _, test := range []struct {
		value string
		want  int
		valid bool
	}{
		{value: "\n\v+6", want: 6, valid: true},
		{value: "6 ", want: 4, valid: false},
		{value: "2147483648", want: 4, valid: false},
		{value: "-1", want: 4, valid: false},
	} {
		t.Run(test.value, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]string{"count": test.value})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.LoadConfig(encoded); err != nil {
				t.Fatal(err)
			}
			if got := store.Int("count"); got != test.want {
				t.Errorf("Int(count) = %d, want %d", got, test.want)
			}
			if got := setting.Valid(test.value); got != test.valid {
				t.Errorf("Setting.Valid(%q) = %t, want %t", test.value, got, test.valid)
			}
		})
	}
}

func TestDurationKeepsMillisecondPrecisionBeyondTimeDurationRange(t *testing.T) {
	store := New([]Setting{{Key: "delay", Kind: KindDuration, Default: "1s"}})
	const milliseconds = int64(9223372036855)
	if err := store.LoadConfig([]byte(`{"delay":"9223372036855ms"}`)); err != nil {
		t.Fatal(err)
	}
	if got := store.DurationMS("delay"); got != milliseconds {
		t.Fatalf("DurationMS(delay) = %d, want %d", got, milliseconds)
	}
	if got := store.Duration("delay"); got != time.Duration(1<<63-1) {
		t.Fatalf("Duration(delay) = %s, want a saturated maximum duration", got)
	}
}

func TestSettingValidationAndDefaultRegistry(t *testing.T) {
	settings := DefaultSettings()
	if len(settings) != 68 {
		t.Fatalf("default registry size changed: got %d, want 68", len(settings))
	}
	if settings[0].Key != "preset" || settings[1].Key != "provider" || settings[len(settings)-1].Key != "providers.mock.script" {
		t.Fatalf("setting registry order changed: first=%q,%q last=%q", settings[0].Key, settings[1].Key, settings[len(settings)-1].Key)
	}
	var markdown, apiKey, contextLimit Setting
	for _, setting := range settings {
		switch setting.Key {
		case "markdown":
			markdown = setting
		case "providers.openai-compatible.api_key":
			apiKey = setting
		case "context_limit":
			contextLimit = setting
		}
	}
	if !markdown.Valid("yes") || markdown.Valid("maybe") {
		t.Fatal("boolean choices should accept boolean aliases only")
	}
	if !apiKey.Secret || !contextLimit.Editable || contextLimit.Kind != KindTokens {
		t.Fatalf("registry metadata was lost: api_key=%+v context_limit=%+v", apiKey, contextLimit)
	}
	if (Setting{Key: "bytes", Kind: KindSize}).Valid("nonsense") || (Setting{Key: "tokens", Kind: KindTokens}).Valid("0") {
		t.Fatal("size and token validators must reject non-positive or invalid values")
	}
	if !(Setting{Key: "bytes", Kind: KindSize}).Valid("\n+2k") || !(Setting{Key: "delay", Kind: KindDuration}).Valid("\v250MS \t") {
		t.Fatal("numeric parsers should accept strtol-style signs and trailing whitespace")
	}
	if (Setting{Key: "delay", Kind: KindDuration}).Valid("1mſ") ||
		(Setting{Key: "theme", Kind: KindString, Choices: "dark|light"}).Valid("darK") {
		t.Fatal("ASCII unit and choice comparisons accepted Unicode case-fold lookalikes")
	}
	store := New([]Setting{{Key: "empty-default", HasDefault: true}})
	if got := store.Default("empty-default"); got == nil || *got != "" {
		t.Fatalf("explicit empty default was lost: %v", got)
	}
}

func TestOpenAIReasoningRequiredIsNotARegisteredSetting(t *testing.T) {
	for _, candidate := range DefaultSettings() {
		if candidate.Key == "providers.openai-compatible.reasoning_required" || candidate.EnvVar == "GAX_OPENAI_REASONING_REQUIRED" {
			t.Fatalf("unsupported reasoning_required setting registered: %+v", candidate)
		}
	}
}

func TestOpenAIReasoningRoundtripUsesUpstreamEnvironmentAlias(t *testing.T) {
	const key = "providers.openai-compatible.reasoning_roundtrip"
	const envVar = "GAX_REASONING_ROUNDTRIP"
	const incorrectEnvVar = "GAX_OPENAI_REASONING_ROUNDTRIP"
	var setting Setting
	for _, candidate := range DefaultSettings() {
		if candidate.Key == key {
			setting = candidate
			break
		}
	}
	wantDescription := "Replay reasoning text to the model (off/on, or a field name)"
	if setting.Key == "" || setting.EnvVar != envVar || setting.Description != wantDescription || !setting.KeepEmpty {
		t.Fatalf("reasoning_roundtrip setting metadata = %+v", setting)
	}

	store := New(DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "reasoning_content", name == envVar
	})
	if got := store.String(key); got == nil || *got != "reasoning_content" {
		t.Fatalf("%s should set the provider reasoning field, got %v", envVar, got)
	}

	incorrectStore := New(DefaultSettings())
	incorrectStore.SetEnvironment(func(name string) (string, bool) {
		return "reasoning_content", name == incorrectEnvVar
	})
	if got := incorrectStore.String(key); got != nil {
		t.Fatalf("non-upstream alias %s unexpectedly set the provider reasoning field to %q", incorrectEnvVar, *got)
	}
}

func TestObjectKeysIncludeNestedAndFlatChildrenInStableOrder(t *testing.T) {
	store := New(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"z":{"api":"chat"},"a":{"api":"responses"}},"providers.m":{"api":"chat"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"providers":{"a":{"name":"A"},"b":{}}}`)); err != nil {
		t.Fatal(err)
	}
	got := store.ObjectKeys("providers")
	want := []string{"z", "a", "m", "b"}
	if len(got) != len(want) {
		t.Fatalf("object key union differs: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("object key order differs: got=%v want=%v", got, want)
		}
	}
}

func TestPersistStateAtomicallyWritesNestedKeysAndUpdatesStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	store := New([]Setting{{Key: "theme"}})
	store.SetEnvironment(nil)
	if err := store.LoadState([]byte(`{"other":"kept","providers.gateway.api":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistState("providers.gateway.api", stringPtr("chat")); err != nil {
		t.Fatal(err)
	}
	if got := store.String("providers.gateway.api"); got == nil || *got != "chat" {
		t.Fatalf("in-memory state value = %v, want chat", got)
	}
	path := filepath.Join(root, "gax", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("persisted state should end in a newline: %q", data)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["providers.gateway.api"]; exists {
		t.Fatal("flat dotted spelling shadows the nested value after persistence")
	}
	var providers map[string]map[string]string
	if err := json.Unmarshal(decoded["providers"], &providers); err != nil {
		t.Fatal(err)
	}
	if providers["gateway"]["api"] != "chat" || string(decoded["other"]) != `"kept"` {
		t.Fatalf("persisted state lost nested update or unrelated value: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode = %v, err=%v; want 0600", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || !reflect.DeepEqual(names(entries), []string{"state.json"}) {
		t.Fatalf("atomic write left unexpected files: entries=%v err=%v", names(entries), err)
	}
}

func TestPersistDoesNotChangeExistingDirectoryPermissions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	directory := filepath.Join(root, "gax")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0710); err != nil {
		t.Fatal(err)
	}
	store := New(nil)
	store.SetEnvironment(nil)
	if err := store.LoadState([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistState("theme", stringPtr("dark")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0710 {
		t.Fatalf("existing state directory mode = %04o, want 0710", got)
	}
}

func TestPersistSelectionPreservesSameProviderDefaultsAndRollsBackFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	store := New(DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadState([]byte(`{"provider":"codex","model":"old-model","effort":"high","preset":"saved","other":"keep"}`)); err != nil {
		t.Fatal(err)
	}
	model, effort := "new-model", "medium"
	if err := store.PersistSelection("codex", &model, &effort); err != nil {
		t.Fatal(err)
	}
	low := "low"
	if err := store.PersistSelection("codex", nil, &low); err != nil {
		t.Fatal(err)
	}
	if got := store.String("model"); got == nil || *got != "new-model" {
		t.Fatalf("same-provider unpicked model = %v, want new-model", got)
	}
	if got := store.String("effort"); got == nil || *got != "low" {
		t.Fatalf("same-provider picked effort = %v, want low", got)
	}
	if err := store.PersistSelection("mock", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.String("provider"); got == nil || *got != "mock" {
		t.Fatalf("provider after switch = %v, want mock", got)
	}
	if store.String("model") != nil || store.String("effort") != nil || store.String("preset") != nil {
		t.Fatalf("provider switch did not clear bound selections and preset: model=%v effort=%v preset=%v",
			store.String("model"), store.String("effort"), store.String("preset"))
	}

	before, err := os.ReadFile(filepath.Join(root, "gax", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	blockedRoot := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blockedRoot)
	if err := store.PersistSelection("other", nil, nil); err == nil {
		t.Fatal("persist selection unexpectedly succeeded below a regular file")
	}
	if got := store.String("provider"); got == nil || *got != "mock" {
		t.Fatalf("failed write changed in-memory provider: %v", got)
	}
	after, err := os.ReadFile(filepath.Join(root, "gax", "state.json"))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed write modified persisted state: before=%s after=%s err=%v", before, after, err)
	}
}

func TestPersistConfigRefusesUnusableFilesAndKeepsOtherValues(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := New(nil)
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"name":"kept","providers":{"demo":{"api":"chat"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistConfig("providers.demo.api", stringPtr("responses")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gax", "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(before, &decoded); err != nil {
		t.Fatal(err)
	}
	providers := decoded["providers"].(map[string]any)
	demo := providers["demo"].(map[string]any)
	if demo["api"] != "responses" || decoded["name"] != "kept" {
		t.Fatalf("config persistence lost nested update or unrelated value: %s", before)
	}
	if err := store.LoadConfig([]byte(`{ invalid`)); err == nil {
		t.Fatal("malformed config unexpectedly loaded")
	}
	if err := store.PersistConfig("name", stringPtr("replacement")); err == nil {
		t.Fatal("persistence should refuse an unusable config file")
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refused persistence changed config file: before=%s after=%s err=%v", before, after, err)
	}
}

func TestPersistPresetAddsOrderedDefinitionWithoutLosingConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := New(nil)
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"name":"kept","presets":{"other":{"provider":"mock"}},"tail":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPreset("review", Preset{
		Description: stringPtr("Review changes"), Tint: stringPtr("violet"), Provider: "mock",
		Model: stringPtr("review-model"), Effort: stringPtr("high"),
		SystemPrompt: stringPtr("review prompt"),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gax", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var presets map[string]map[string]string
	if err := json.Unmarshal(decoded["presets"], &presets); err != nil {
		t.Fatal(err)
	}
	if presets["review"]["description"] != "Review changes" || presets["review"]["tint"] != "violet" ||
		presets["review"]["provider"] != "mock" || presets["review"]["model"] != "review-model" ||
		presets["review"]["effort"] != "high" || presets["review"]["system_prompt"] != "review prompt" ||
		decoded["name"] == nil || decoded["tail"] == nil {
		t.Fatalf("saved preset or surrounding config was lost: %s", data)
	}
	text := string(data)
	if review := strings.Index(text, `"review"`); review >= 0 {
		text = text[review:]
	}
	ordered := []string{`"description"`, `"tint"`, `"provider"`, `"model"`, `"effort"`, `"system_prompt"`}
	last := -1
	for _, key := range ordered {
		index := strings.Index(text, key)
		if index <= last {
			t.Fatalf("preset fields are not in stable order %v: %s", ordered, text)
		}
		last = index
	}
}

func TestPersistPresetRefusesStateDefinitionAndInvalidPresetBlock(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := New(nil)
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"presets":{"review":{"provider":"old"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"presets":{"review":{"provider":"state"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPreset("review", Preset{Provider: "mock"}); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("state-defined preset error = %v", err)
	}
	if err := store.LoadState(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadConfig([]byte(`{"presets":"not-an-object"}`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gax", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"presets":"not-an-object"}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPreset("review", Preset{Provider: "mock"}); err == nil || !strings.Contains(err.Error(), "presets") {
		t.Fatalf("invalid presets block error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed preset save modified config: before=%s after=%s err=%v", before, after, err)
	}
}

func TestPersistPresetValidatesNameAndTintBeforeWriting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := New(nil)
	store.SetEnvironment(nil)

	for _, test := range []struct {
		name   string
		preset Preset
		want   string
	}{
		{name: "bad/name", preset: Preset{Provider: "mock"}, want: "can't be a preset name"},
		{name: "valid", preset: Preset{Provider: "mock", Tint: stringPtr("ultraviolet")}, want: "unknown tint"},
	} {
		if err := store.PersistPreset(test.name, test.preset); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("PersistPreset(%q) error = %v, want %q", test.name, err, test.want)
		}
	}
	path := filepath.Join(root, "gax", "config.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid preset created or changed config file: stat error = %v", err)
	}
}

func TestPersistPresetRejectsUnreadablePromptFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	store := New(nil)
	store.SetEnvironment(nil)

	missingPrompt := "@" + filepath.Join(t.TempDir(), "missing-prompt.md")
	for _, test := range []struct {
		name   string
		preset Preset
	}{
		{name: "base prompt", preset: Preset{Provider: "mock", SystemPrompt: stringPtr(missingPrompt)}},
		{name: "appended prompt", preset: Preset{Provider: "mock", SystemPromptAppend: stringPtr(missingPrompt)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.PersistPreset("review", test.preset); err == nil || !strings.Contains(err.Error(), "prompt file") {
				t.Fatalf("PersistPreset error = %v, want unreadable prompt-file error", err)
			}
		})
	}
	path := filepath.Join(root, "gax", "config.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid prompt preset created or changed config file: stat error = %v", err)
	}
}

func TestValidPresetName(t *testing.T) {
	for _, test := range []struct {
		name string
		want bool
	}{
		{"review", true}, {"review.fast_2", true}, {"2", true},
		{"", false}, {".hidden", false}, {"bad/name", false}, {strings.Repeat("a", 64), false},
	} {
		if got := ValidPresetName(test.name); got != test.want {
			t.Errorf("ValidPresetName(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func names(entries []os.DirEntry) []string {
	result := make([]string, len(entries))
	for i, entry := range entries {
		result[i] = entry.Name()
	}
	return result
}

func stringPtr(value string) *string { return &value }

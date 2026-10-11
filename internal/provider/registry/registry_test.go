package registry_test

import (
	"reflect"
	"testing"

	baseconfig "github.com/luank3/gax/internal/config"
	providerregistry "github.com/luank3/gax/internal/provider/registry"
)

func TestShippedProviderOrderDefaultAndAlias(t *testing.T) {
	registry := providerregistry.New(baseconfig.New(baseconfig.DefaultSettings()))
	all := registry.All()
	want := []string{
		"codex", "openai", "anthropic", "openrouter", "opencode-go", "deepseek",
		"llamacpp", "ollama", "openai-compatible", "anthropic-compatible",
	}
	got := make([]string, len(all))
	for index, definition := range all {
		got[index] = definition.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shipped provider order changed: got=%v want=%v", got, want)
	}
	if registry.Default() == nil || registry.Default().ID != "codex" {
		t.Fatalf("default provider should be highest-priority visible def: %+v", registry.Default())
	}
	deepseek, ok := registry.Find("deepseek")
	if !ok || deepseek.API != "" || deepseek.BaseURL != "https://api.deepseek.com" || !deepseek.Pinned ||
		deepseek.APIKeyEnv != "DEEPSEEK_API_KEY" || deepseek.CatalogID != "deepseek" ||
		deepseek.ReasoningRoundtrip != "reasoning_content" || !deepseek.ReasoningRequired ||
		deepseek.Hooks&(providerregistry.HookParseModel|providerregistry.HookQueryUsage) != providerregistry.HookParseModel|providerregistry.HookQueryUsage {
		t.Fatalf("DeepSeek shipped definition or hooks missing: %+v found=%t", deepseek, ok)
	}
	if _, ok := registry.Find("openference"); ok {
		t.Fatal("Openference should not be a shipped provider")
	}
	if _, ok := registry.Find("opencode-zen"); ok {
		t.Fatal("the retired OpenCode Zen def should not be a shipped provider")
	}
	if _, ok := registry.Find("agnes"); ok {
		t.Fatal("the retired Agnes AI def should not be a shipped provider")
	}
	ollama, ok := registry.Find("ollama")
	wantOllamaHint := "ollama's context window may be too small for the prompt — restart `ollama serve` with a larger OLLAMA_CONTEXT_LENGTH (e.g. 16384), or raise num_ctx on the model"
	if !ok || ollama.ReasoningRoundtrip != "" || ollama.LengthHint != wantOllamaHint {
		t.Fatalf("Ollama reasoning replay or context guidance changed: %+v found=%t", ollama, ok)
	}
	llama, ok := registry.Find("llama.cpp")
	if !ok || llama.ID != "llamacpp" || registry.DisplayName(llama) != "llama.cpp" {
		t.Fatalf("llama.cpp alias did not resolve to canonical definition: %+v found=%t", llama, ok)
	}
	mock, ok := registry.Find("mock")
	if !ok || !mock.Internal || !mock.NoEfforts {
		t.Fatalf("internal mock provider definition = %+v found=%t, want directly addressable without effort levels", mock, ok)
	}
}

func TestConfigOnlyProvidersAppendAndShippedBlocksOverlay(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"providers":{"zeta":{"display_name":"Zeta Gateway","catalog_id":"groq"},"openai":{"display_name":"Ignored override"},"bad.name":{"base_url":"https://bad.example"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState([]byte(`{"providers":{"alpha":{},"llamacpp":{"display_name":"Local server"}}}`)); err != nil {
		t.Fatal(err)
	}
	registry := providerregistry.New(store)
	all := registry.All()
	if all[len(all)-2].ID != "zeta" || all[len(all)-1].ID != "alpha" {
		t.Fatalf("config-only defs should append in file then state key order: %v", definitionIDs(all))
	}
	if _, ok := registry.Find("bad.name"); ok {
		t.Fatal("provider name containing a dot should not be registered")
	}
	if registry.Warnings() == nil {
		t.Fatal("invalid dotted provider should produce a warning")
	}
	zeta, ok := registry.Find("zeta")
	if !ok || zeta.BaseURL != "" || registry.DisplayName(zeta) != "Zeta Gateway" {
		t.Fatalf("custom provider definition or configured display name is wrong: %+v", zeta)
	}
	if got := registry.CatalogID(zeta); got == nil || *got != "groq" {
		t.Fatalf("custom catalog identity did not resolve: %v", got)
	}
	openAI, _ := registry.Find("openai")
	if registry.DisplayName(openAI) != "Ignored override" || openAI.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("shipped definition should retain defaults while allowing display overrides: %+v name=%q", openAI, registry.DisplayName(openAI))
	}
	llama, _ := registry.Find("llamacpp")
	if registry.DisplayName(llama) != "Local server" {
		t.Fatalf("config should overlay shipped definitions by matching id: %q", registry.DisplayName(llama))
	}
	if got := registry.CatalogID(alphaDefinition(registry)); got != nil {
		t.Fatalf("config-only provider should not inherit a catalog identity: %v", got)
	}
}

func TestEmptyCatalogIdentityOptsOutOfShippedDefault(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"providers":{"openai":{"catalog_id":""}}}`)); err != nil {
		t.Fatal(err)
	}
	registry := providerregistry.New(store)
	openAI, _ := registry.Find("openai")
	if got := registry.CatalogID(openAI); got != nil {
		t.Fatalf("empty catalog id should opt out of shipped identity: %v", got)
	}
}

func definitionIDs(definitions []*providerregistry.Definition) []string {
	ids := make([]string, len(definitions))
	for index, definition := range definitions {
		ids[index] = definition.ID
	}
	return ids
}

func alphaDefinition(registry *providerregistry.Registry) *providerregistry.Definition {
	definition, _ := registry.Find("alpha")
	return definition
}

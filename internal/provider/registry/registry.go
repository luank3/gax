// Package registry contains the shipped provider definitions and config-defined data providers.
package registry

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/luank3/gax/internal/config"
	providerconfig "github.com/luank3/gax/internal/provider/config"
	"github.com/luank3/gax/internal/version"
)

type Hook uint32

const (
	HookParseModel Hook = 1 << iota
	HookProbeModel
	HookListModels
	HookQueryUsage
	HookModelLabel
	HookAuthSource
	HookLoadDefaults
	HookDiscover
	HookConstruct
	HookPrepareAvailability
)

type Definition struct {
	ID                 string
	DisplayName        string
	Internal           bool
	API                string
	BaseURL            string
	Port               int
	Pinned             bool
	APIKeyEnv          string
	CatalogID          string
	MetadataAPI        string
	SendCacheKey       bool
	Cache              string
	RequestCost        bool
	ReasoningFormat    string
	ReasoningRoundtrip string
	ThinkingMode       string
	ReasoningRequired  bool
	StrictSignatures   bool
	LengthHint         string
	NoEfforts          bool
	ExtraBody          json.RawMessage
	ExtraHeaders       json.RawMessage
	Probe              bool
	UnconfiguredReason string
	Hooks              Hook
}

type Registry struct {
	config    *config.Store
	providers *providerconfig.Config
	byID      map[string]*Definition
	visible   []*Definition
	warnings  []string
}

var shipped = []Definition{
	{
		ID: "codex", API: "openai-responses", BaseURL: "https://chatgpt.com/backend-api/codex", Pinned: true,
		CatalogID: "openai", SendCacheKey: true,
		ExtraBody:    json.RawMessage(`{"text":{"verbosity":"low"}}`),
		ExtraHeaders: json.RawMessage(`{"originator":"codex_cli_rs","User-Agent":"codex_cli_rs/0.144.1 gax/` + version.Value + `"}`),
		Hooks:        HookProbeModel | HookListModels | HookQueryUsage | HookAuthSource | HookLoadDefaults | HookPrepareAvailability,
	},
	{ID: "openai", API: "openai-responses", BaseURL: "https://api.openai.com/v1", Pinned: true, APIKeyEnv: "OPENAI_API_KEY", CatalogID: "openai", SendCacheKey: true},
	{ID: "anthropic", API: "anthropic-messages", BaseURL: "https://api.anthropic.com/v1", Pinned: true, APIKeyEnv: "ANTHROPIC_API_KEY", CatalogID: "anthropic", ThinkingMode: "prefer-adaptive", StrictSignatures: true},
	{
		ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Pinned: true, APIKeyEnv: "OPENROUTER_API_KEY",
		SendCacheKey: true, Cache: "auto", RequestCost: true, ReasoningFormat: "nested",
		ExtraHeaders: json.RawMessage(`{"X-Title":"gax","HTTP-Referer":"https://github.com/luank3/gax","X-OpenRouter-Categories":"cli-agent","x-session-id":"{session_id}"}`),
		Hooks:        HookParseModel | HookProbeModel | HookQueryUsage,
	},
	{ID: "opencode-zen", API: "catalog", BaseURL: "https://opencode.ai/zen/v1", APIKeyEnv: "OPENCODE_API_KEY", CatalogID: "opencode", MetadataAPI: "openai", ExtraHeaders: openCodeHeaders()},
	{ID: "opencode-go", API: "catalog", BaseURL: "https://opencode.ai/zen/go/v1", APIKeyEnv: "OPENCODE_API_KEY", CatalogID: "opencode-go", MetadataAPI: "openai", ExtraHeaders: openCodeHeaders(), Hooks: HookQueryUsage},
	{
		ID: "deepseek", BaseURL: "https://api.deepseek.com", Pinned: true,
		APIKeyEnv: "DEEPSEEK_API_KEY", CatalogID: "deepseek",
		ReasoningRoundtrip: "reasoning_content", ReasoningRequired: true,
		Hooks: HookParseModel | HookQueryUsage,
	},
	{ID: "agnes", DisplayName: "Agnes AI", API: "openai-completions", BaseURL: "https://apihub.agnes-ai.com/v1", Pinned: true, APIKeyEnv: "AGNES_API_KEY"},
	{
		ID: "llamacpp", DisplayName: "llama.cpp", BaseURL: "http://127.0.0.1:{port}/v1",
		ReasoningRoundtrip: "reasoning_content", ExtraBody: json.RawMessage(`{"return_progress":true}`),
		LengthHint: "llama-server's context is full — restart it with a larger -c / --ctx-size", NoEfforts: true,
		Hooks: HookParseModel | HookProbeModel | HookModelLabel | HookDiscover | HookPrepareAvailability,
	},
	{
		ID: "ollama", BaseURL: "http://127.0.0.1:{port}/v1", Port: 11434,
		LengthHint: "ollama's context window may be too small for the prompt — restart `ollama serve` with a larger OLLAMA_CONTEXT_LENGTH (e.g. 16384), or raise num_ctx on the model",
		NoEfforts:  true, Probe: true,
	},
	{ID: "openai-compatible", UnconfiguredReason: "GAX_OPENAI_BASE_URL not set"},
	{ID: "anthropic-compatible", API: "anthropic-messages", UnconfiguredReason: "GAX_ANTHROPIC_BASE_URL not set"},
	{ID: "mock", Internal: true, NoEfforts: true, Hooks: HookConstruct},
}

func openCodeHeaders() json.RawMessage {
	return json.RawMessage(`{"x-opencode-session":"{session_id}","x-opencode-client":"gax"}`)
}

func New(store *config.Store) *Registry {
	registry := &Registry{
		config:    store,
		providers: providerconfig.New(store),
		byID:      make(map[string]*Definition),
	}
	for _, definition := range shipped {
		copy := definition
		copy.ExtraBody = append(json.RawMessage(nil), definition.ExtraBody...)
		copy.ExtraHeaders = append(json.RawMessage(nil), definition.ExtraHeaders...)
		registry.byID[copy.ID] = &copy
		if !copy.Internal {
			registry.visible = append(registry.visible, &copy)
		}
	}
	if store == nil {
		return registry
	}
	for _, name := range store.ObjectKeys("providers") {
		if strings.Contains(name, ".") {
			registry.warnings = append(registry.warnings, "ignoring custom provider "+name+": name cannot contain '.'")
			continue
		}
		if _, shipped := registry.byID[name]; shipped {
			continue
		}
		definition := &Definition{ID: name}
		registry.byID[name] = definition
		registry.visible = append(registry.visible, definition)
	}
	return registry
}

func CanonicalID(name string) string {
	if name == "llama.cpp" {
		return "llamacpp"
	}
	return name
}

func (registry *Registry) Find(name string) (*Definition, bool) {
	if registry == nil {
		return nil, false
	}
	definition, ok := registry.byID[CanonicalID(name)]
	return definition, ok
}

func (registry *Registry) All() []*Definition {
	if registry == nil {
		return nil
	}
	return append([]*Definition(nil), registry.visible...)
}

func (registry *Registry) Default() *Definition {
	if registry == nil || len(registry.visible) == 0 {
		return nil
	}
	return registry.visible[0]
}

func (registry *Registry) DisplayName(definition *Definition) string {
	if definition == nil {
		return ""
	}
	if registry != nil && registry.config != nil {
		if value := registry.config.StringNonempty("providers." + definition.ID + ".display_name"); value != nil {
			return *value
		}
	}
	if definition.DisplayName != "" {
		return definition.DisplayName
	}
	return definition.ID
}

// CatalogID returns nil when a config value explicitly opts out with an empty string.
func (registry *Registry) CatalogID(definition *Definition) *string {
	if definition == nil {
		return nil
	}
	if registry != nil && registry.config != nil {
		if value := registry.config.String("providers." + definition.ID + ".catalog_id"); value != nil {
			if *value == "" {
				return nil
			}
			return value
		}
	}
	if definition.CatalogID == "" {
		return nil
	}
	value := definition.CatalogID
	return &value
}

func (registry *Registry) Warnings() []string {
	if registry == nil {
		return nil
	}
	return append([]string(nil), registry.warnings...)
}

// ConfigValue resolves a provider field with its definition default when present.
func (registry *Registry) ConfigValue(definition *Definition, leaf string) *string {
	if definition == nil || registry == nil || registry.providers == nil {
		return nil
	}
	if value := registry.providers.String("providers."+definition.ID, leaf); value != nil {
		return value
	}
	if value := definitionDefault(definition, leaf); value != "" {
		return &value
	}
	return nil
}

func definitionDefault(definition *Definition, leaf string) string {
	switch leaf {
	case "api":
		return definition.API
	case "base_url":
		return definition.BaseURL
	case "port":
		if definition.Port > 0 {
			return strconv.Itoa(definition.Port)
		}
	case "api_key_env":
		return definition.APIKeyEnv
	case "catalog_id":
		return definition.CatalogID
	case "metadata_api":
		return definition.MetadataAPI
	case "cache":
		return definition.Cache
	case "reasoning_format":
		return definition.ReasoningFormat
	case "reasoning_roundtrip":
		return definition.ReasoningRoundtrip
	case "thinking_mode":
		return definition.ThinkingMode
	}
	return ""
}

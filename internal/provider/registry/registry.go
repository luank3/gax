// Package registry defines shipped providers and resolves runtime provider
// clients from configuration, environment credentials, and custom provider blocks.
package registry

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/anthropic"
	"github.com/luank3/gax/internal/provider/mock"
	"github.com/luank3/gax/internal/provider/openai"
	"github.com/luank3/gax/internal/transport"
)

// Def describes a shipped or config-defined provider entry.
type Def struct {
	ID           string
	DisplayName  string
	API          string // "openai-responses", "openai-completions", "anthropic-messages", "catalog", "mock"
	BaseURL      string
	APIKeyEnv    string
	CatalogID    string
	Pinned       bool
	Internal     bool
	SendCacheKey bool
	ExtraHeaders map[string]string
}

// Shipped lists built-in providers in autoselect priority order.
var Shipped = []Def{
	{
		ID: "codex", DisplayName: "Codex (ChatGPT)", API: "openai-responses",
		BaseURL: "https://chatgpt.com/backend-api/codex", CatalogID: "openai", Pinned: true, SendCacheKey: true,
	},
	{
		ID: "openai", DisplayName: "OpenAI", API: "openai-responses",
		BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", CatalogID: "openai", Pinned: true, SendCacheKey: true,
	},
	{
		ID: "anthropic", DisplayName: "Anthropic", API: "anthropic-messages",
		BaseURL: "https://api.anthropic.com/v1", APIKeyEnv: "ANTHROPIC_API_KEY", CatalogID: "anthropic", Pinned: true,
	},
	{
		ID: "openrouter", DisplayName: "OpenRouter", API: "openai-completions",
		BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", CatalogID: "openrouter", Pinned: true,
		ExtraHeaders: map[string]string{
			"HTTP-Referer":            "https://github.com/luank3/gax",
			"X-Title":                 "gax",
			"X-OpenRouter-Categories": "cli-agent",
		},
	},
	{
		ID: "opencode-zen", DisplayName: "OpenCode Zen", API: "catalog",
		BaseURL: "https://opencode.ai/zen/v1", APIKeyEnv: "OPENCODE_API_KEY", CatalogID: "opencode", Pinned: true,
		ExtraHeaders: map[string]string{"x-opencode-client": "gax"},
	},
	{
		ID: "opencode-go", DisplayName: "OpenCode Go", API: "catalog",
		BaseURL: "https://opencode.ai/zen/go/v1", APIKeyEnv: "OPENCODE_API_KEY", CatalogID: "opencode-go", Pinned: true,
		ExtraHeaders: map[string]string{"x-opencode-client": "gax"},
	},
	{
		ID: "deepseek", DisplayName: "DeepSeek", API: "openai-completions",
		BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY", CatalogID: "deepseek", Pinned: true,
	},
	{
		ID: "llamacpp", DisplayName: "llama.cpp", API: "openai-completions",
		BaseURL: "http://127.0.0.1:9931/v1",
	},
	{
		ID: "ollama", DisplayName: "Ollama", API: "openai-completions",
		BaseURL: "http://127.0.0.1:11434/v1",
	},
	{
		ID: "openai-compatible", DisplayName: "OpenAI-compatible", API: "openai-completions",
	},
	{
		ID: "anthropic-compatible", DisplayName: "Anthropic-compatible", API: "anthropic-messages",
	},
	{
		ID: "mock", DisplayName: "Mock", API: "mock", Internal: true,
	},
}

// AvailableProviders returns providers eligible for /provider and completion.
// Unconfigured openai-compatible / anthropic-compatible are omitted (hax v0.7.0).
func AvailableProviders(store *config.Store) []Def {
	var out []Def
	for _, d := range Shipped {
		if d.Internal {
			continue
		}
		if d.ID == "openai-compatible" && store.String("providers.openai-compatible.base_url") == "" {
			continue
		}
		if d.ID == "anthropic-compatible" && store.String("providers.anthropic-compatible.base_url") == "" {
			continue
		}
		out = append(out, d)
	}
	if custom := store.RawSection("providers"); custom != nil {
		for id := range custom {
			if isShipped(id) || strings.Contains(id, ".") {
				continue
			}
			out = append(out, Def{ID: id, DisplayName: id, API: "openai-completions"})
		}
	}
	return out
}

func isShipped(id string) bool {
	for _, d := range Shipped {
		if d.ID == id {
			return true
		}
	}
	return false
}

// Resolve builds a provider.Client for providerID using store and httpClient.
func Resolve(providerID, modelID, cwd string, store *config.Store, httpClient *transport.Client) (provider.Client, error) {
	if providerID == "mock" {
		return mock.New(store.String("providers.mock.script"), cwd)
	}
	var def *Def
	for i := range Shipped {
		if Shipped[i].ID == providerID {
			copyDef := Shipped[i]
			def = &copyDef
			break
		}
	}
	if def == nil {
		def = &Def{ID: providerID, DisplayName: providerID, API: "openai-completions"}
	}

	baseURL := def.BaseURL
	apiKey := ""
	if def.APIKeyEnv != "" {
		apiKey = os.Getenv(def.APIKeyEnv)
	}
	api := def.API

	// Apply providers.<id> config block overrides when not pinned.
	if rawProviders := store.RawSection("providers"); rawProviders != nil {
		if block, ok := rawProviders[providerID].(map[string]any); ok {
			if !def.Pinned {
				if u, ok := block["base_url"].(string); ok && u != "" {
					baseURL = u
				}
				if a, ok := block["api"].(string); ok && a != "" {
					api = a
				}
			}
			if envName, ok := block["api_key_env"].(string); ok && envName != "" {
				apiKey = os.Getenv(envName)
			}
			if rawKey, ok := block["api_key"].(string); ok && rawKey != "" {
				if expanded, ok := config.ExpandEnvValue(rawKey); ok {
					apiKey = expanded
				}
			}
			if modelAPIs, ok := block["model_apis"].(map[string]any); ok && modelID != "" {
				for pattern, target := range modelAPIs {
					if matched, _ := path.Match(pattern, modelID); matched {
						if s, ok := target.(string); ok && s != "" {
							api = s
							break
						}
					}
				}
			}
		}
	}

	if providerID == "llamacpp" {
		if u := store.String("providers.llamacpp.base_url"); u != "" {
			baseURL = u
		} else {
			port := store.Int("providers.llamacpp.port")
			if port <= 0 {
				port = 9931
			}
			baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)
		}
		if k := store.String("providers.llamacpp.api_key"); k != "" {
			apiKey = k
		}
	}
	if providerID == "openai-compatible" {
		baseURL = store.String("providers.openai-compatible.base_url")
		apiKey = store.String("providers.openai-compatible.api_key")
		if store.String("providers.openai-compatible.api") == "responses" {
			api = "openai-responses"
		}
	}
	if providerID == "anthropic-compatible" {
		baseURL = store.String("providers.anthropic-compatible.base_url")
		apiKey = store.String("providers.anthropic-compatible.api_key")
	}

	if baseURL == "" {
		return nil, fmt.Errorf("provider %q has no base_url configured", providerID)
	}

	idleTimeout := store.Duration("http.idle_timeout")
	switch api {
	case "anthropic-messages":
		return anthropic.NewClient(anthropic.Config{
			ProviderID:  providerID,
			BaseURL:     baseURL,
			APIKey:      apiKey,
			Version:     store.String("providers.anthropic-compatible.version"),
			HTTP:        httpClient,
			IdleTimeout: idleTimeout,
			Headers:     def.ExtraHeaders,
		}), nil
	case "openai-responses":
		return openai.NewResponsesClient(openai.Config{
			ProviderID:  providerID,
			BaseURL:     baseURL,
			APIKey:      apiKey,
			HTTP:        httpClient,
			IdleTimeout: idleTimeout,
			Headers:     def.ExtraHeaders,
		}), nil
	default:
		return openai.NewChatClient(openai.Config{
			ProviderID:  providerID,
			BaseURL:     baseURL,
			APIKey:      apiKey,
			HTTP:        httpClient,
			IdleTimeout: idleTimeout,
			Headers:     def.ExtraHeaders,
		}), nil
	}
}

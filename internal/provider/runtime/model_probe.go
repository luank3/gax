package runtime

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/registry"
)

func (provider *Provider) supportsModelProbe() bool {
	if provider == nil || provider.Definition == nil {
		return false
	}
	return provider.MetadataAPI == MetadataAnthropic || provider.Definition.Hooks&registry.HookProbeModel != 0
}

func modelProbeRequest(provider *Provider, model string) (endpoint string, timeout time.Duration,
	maxResponseBytes int64, ok bool) {
	if provider == nil || provider.Definition == nil {
		return "", 0, 0, false
	}
	const shortProbeTimeout = 5 * time.Second
	const largeResponseLimit = 32 << 20
	switch provider.Definition.ID {
	case "openrouter":
		if model == "" {
			return "", 0, 0, false
		}
		return "https://openrouter.ai/api/v1/models?q=" + escapeQueryValue(model),
			shortProbeTimeout, largeResponseLimit, true
	case "codex":
		auth := provider.CodexAuth()
		if auth == nil || auth.AccessToken == "" {
			return "", 0, 0, false
		}
		if codex.TokenExpiresWithin(auth.AccessToken, time.Minute) {
			provider.deferModelProbe()
			return "", 0, 0, false
		}
		return strings.TrimRight(provider.BaseURL, "/") + "/models?client_version=999.0.0",
			shortProbeTimeout, largeResponseLimit, true
	case "llamacpp":
		if model == "" {
			return "", 0, 0, false
		}
		parsed, err := url.Parse(provider.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", 0, 0, false
		}
		parsed.Path = "/props"
		parsed.RawPath = ""
		query := parsed.Query()
		query.Set("model", model)
		parsed.RawQuery = query.Encode()
		return parsed.String(), 120 * time.Second, 4 << 20, true
	default:
		baseURL := strings.TrimRight(provider.BaseURL, "/")
		if baseURL == "" {
			return "", 0, 0, false
		}
		switch provider.MetadataAPI {
		case MetadataAnthropic:
			return baseURL + "/models?limit=1000", shortProbeTimeout, largeResponseLimit, true
		default:
			return "", 0, 0, false
		}
	}
}

func escapeQueryValue(value string) string {
	const hex = "0123456789ABCDEF"
	var escaped strings.Builder
	escaped.Grow(len(value))
	for index := 0; index < len(value); index++ {
		byteValue := value[index]
		if byteValue >= 'A' && byteValue <= 'Z' || byteValue >= 'a' && byteValue <= 'z' ||
			byteValue >= '0' && byteValue <= '9' || byteValue == '-' || byteValue == '_' ||
			byteValue == '.' || byteValue == '~' {
			escaped.WriteByte(byteValue)
			continue
		}
		escaped.WriteByte('%')
		escaped.WriteByte(hex[byteValue>>4])
		escaped.WriteByte(hex[byteValue&0x0f])
	}
	return escaped.String()
}

func (provider *Provider) deferModelProbe() {
	if provider == nil || provider.probe == nil {
		return
	}
	provider.probe.mu.Lock()
	provider.probe.deferred = true
	provider.probe.mu.Unlock()
}

func modelProbeReportsSelection(provider *Provider) bool {
	if provider == nil || provider.Definition == nil {
		return false
	}
	return provider.MetadataAPI == MetadataAnthropic || provider.Definition.Hooks&registry.HookProbeModel != 0
}

// CanProbeSelectedModel reports whether a background probe can return metadata for a specific model.
func (provider *Provider) CanProbeSelectedModel() bool {
	return provider.supportsModelProbe() && modelProbeReportsSelection(provider)
}

func parseModelProbeResponse(provider *Provider, body []byte, model string) (ModelInfo, bool) {
	if provider == nil || provider.Definition == nil {
		return ModelInfo{}, false
	}
	switch provider.Definition.ID {
	case "openrouter":
		return parseProbeListing(body, "data", "id", model,
			func(entry json.RawMessage, id string) (ModelInfo, bool) {
				parsed, err := parseOpenRouterModel(entry, id)
				return parsed, err == nil
			})
	case "codex":
		return parseProbeListing(body, "models", "slug", model, parseCodexModel)
	case "llamacpp":
		return parseLlamaModelProperties(body, model)
	}
	if provider.MetadataAPI == MetadataAnthropic {
		return parseProbeListing(body, "data", "id", model, func(entry json.RawMessage, _ string) (ModelInfo, bool) {
			return parseAnthropicModel(entry)
		})
	}
	return ModelInfo{}, false
}

func parseProbeListing(body []byte, collection, idField, model string,
	parseEntry func(json.RawMessage, string) (ModelInfo, bool)) (ModelInfo, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return ModelInfo{}, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(envelope[collection], &entries) != nil || entries == nil {
		return ModelInfo{}, false
	}
	for _, entry := range entries {
		var object map[string]json.RawMessage
		if json.Unmarshal(entry, &object) != nil || object == nil {
			continue
		}
		id := rawString(object[idField])
		if id == model && parseEntry != nil {
			if parsed, ok := parseEntry(entry, id); ok {
				return parsed, true
			}
		}
	}
	return ModelInfo{}, false
}

func parseCodexModel(raw json.RawMessage, id string) (ModelInfo, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || rawString(object["slug"]) != id {
		return ModelInfo{}, false
	}
	model := ModelInfo{ID: id, ImageInput: catalog.SupportUnknown, Tools: catalog.SupportUnknown}
	var metadata struct {
		ContextWindow    int64 `json:"context_window"`
		MaxContextWindow int64 `json:"max_context_window"`
	}
	_ = json.Unmarshal(raw, &metadata)
	model.ContextWindow = metadata.ContextWindow
	model.MaxContext = metadata.MaxContextWindow
	if model.ContextWindow <= 0 {
		model.ContextWindow = metadata.MaxContextWindow
	}
	if modalities, ok := decodeStringArray(object["input_modalities"]); ok {
		model.ImageInput = catalog.SupportNo
		for _, modality := range modalities {
			if modality == "image" {
				model.ImageInput = catalog.SupportYes
				break
			}
		}
	}
	model.Description = rawString(object["description"])
	levels := bytes.TrimSpace(object["supported_reasoning_levels"])
	if len(levels) > 0 && levels[0] == '[' {
		var supported []json.RawMessage
		if json.Unmarshal(levels, &supported) == nil {
			model.EffortsKnown = true
			if len(supported) > 0 {
				model.Efforts = append(model.Efforts, "none")
			}
			for _, rawLevel := range supported {
				var effort string
				if json.Unmarshal(rawLevel, &effort) != nil {
					var level map[string]json.RawMessage
					if json.Unmarshal(rawLevel, &level) == nil {
						effort = rawString(level["effort"])
					}
				}
				if effort != "" && effort != "ultra" && !containsString(model.Efforts, effort) {
					model.Efforts = append(model.Efforts, effort)
				}
			}
		}
	}
	return model, true
}

func parseLlamaModelProperties(body []byte, id string) (ModelInfo, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return ModelInfo{}, false
	}
	model := ModelInfo{ID: id, ImageInput: catalog.SupportUnknown, Tools: catalog.SupportUnknown}
	var generation map[string]json.RawMessage
	_ = json.Unmarshal(object["default_generation_settings"], &generation)
	_ = json.Unmarshal(generation["n_ctx"], &model.ContextWindow)
	var modalities map[string]json.RawMessage
	_ = json.Unmarshal(object["modalities"], &modalities)
	var vision *bool
	if json.Unmarshal(modalities["vision"], &vision) == nil && vision != nil {
		model.ImageInput = catalog.SupportNo
		if *vision {
			model.ImageInput = catalog.SupportYes
		}
	}
	return model, true
}

func decodeStringArray(raw json.RawMessage) ([]string, bool) {
	var values []string
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

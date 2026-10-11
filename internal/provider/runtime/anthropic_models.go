package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/transport"
)

type listedModelCache struct {
	mu        sync.RWMutex
	models    map[string]ModelInfo
	listedIDs []string
}

const (
	anthropicModelPageSize  = 1000
	anthropicModelPageLimit = 50
)

func (provider *Provider) listAnthropicModels(ctx context.Context, client *transport.Client,
	tick func() bool) ([]ModelInfo, error) {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("provider %q has no base URL", provider.Definition.ID)
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	models := make([]ModelInfo, 0)
	afterID := ""
	sawEntry := false
	for pageNumber := 0; pageNumber < anthropicModelPageLimit; pageNumber++ {
		pageContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		url := fmt.Sprintf("%s/models?limit=%d", baseURL, anthropicModelPageSize)
		if afterID != "" {
			url += "&after_id=" + afterID
		}
		body, status, err := client.Get(pageContext, url, provider.MetadataHeaders(""), 10*time.Second, 32<<20, tick)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s", transport.FormatModelListError(providerDisplayName(provider), baseURL, provider.HasAPIKey(), status))
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || !json.Valid(trimmed) {
			return nil, fmt.Errorf("%s /models response is not valid JSON", providerDisplayName(provider))
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(trimmed, &envelope) != nil || envelope == nil {
			return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
		}
		data := bytes.TrimSpace(envelope["data"])
		var entries []json.RawMessage
		if !bytes.Equal(data, []byte("null")) {
			if len(data) == 0 || data[0] != '[' || json.Unmarshal(data, &entries) != nil || entries == nil {
				return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
			}
		}
		lastID := rawString(envelope["last_id"])
		if afterID != "" && lastID == afterID {
			break
		}
		if len(entries) > 0 {
			sawEntry = true
		}
		for _, raw := range entries {
			model, usable := parseAnthropicModel(raw)
			if usable {
				models = append(models, model)
			}
		}
		var hasMore bool
		_ = json.Unmarshal(envelope["has_more"], &hasMore)
		if !hasMore || !safeAnthropicCursor(lastID) {
			break
		}
		afterID = lastID
	}
	if sawEntry && len(models) == 0 {
		return nil, fmt.Errorf("%s /models response contains no usable model ids", providerDisplayName(provider))
	}
	provider.storeListedModels(models)
	return models, nil
}

func parseAnthropicModel(raw json.RawMessage) (ModelInfo, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return ModelInfo{}, false
	}
	id := modelID(object["id"])
	if id == "" {
		return ModelInfo{}, false
	}
	model := ModelInfo{ID: id, ImageInput: catalog.SupportUnknown, Tools: catalog.SupportUnknown}
	var metadata struct {
		MaxInputTokens int64 `json:"max_input_tokens"`
		MaxTokens      int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(raw, &metadata)
	if metadata.MaxInputTokens > 0 {
		model.ContextWindow = metadata.MaxInputTokens
	}
	if metadata.MaxTokens > 0 {
		model.MaxOutput = metadata.MaxTokens
	}
	var capabilities map[string]json.RawMessage
	_ = json.Unmarshal(object["capabilities"], &capabilities)
	var image map[string]json.RawMessage
	_ = json.Unmarshal(capabilities["image_input"], &image)
	var imageSupported *bool
	if json.Unmarshal(image["supported"], &imageSupported) == nil && imageSupported != nil {
		model.ImageInput = catalog.SupportNo
		if *imageSupported {
			model.ImageInput = catalog.SupportYes
		}
	}
	model.Efforts, model.EffortsKnown = parseAnthropicEfforts(capabilities["effort"])
	return model, true
}

func parseAnthropicEfforts(raw json.RawMessage) ([]string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	var supported *bool
	if json.Unmarshal(fields["supported"], &supported) == nil && supported != nil && !*supported {
		return nil, true
	}
	levels := make([]string, 0, 4)
	seen := make(map[string]bool)
	for _, level := range effortLadder {
		if anthropicEffortSupported(fields[level]) {
			levels = appendAnthropicEffort(levels, seen, level)
		}
	}
	for _, name := range orderedRawObjectKeys(raw) {
		if name == "supported" || isEffortLadderLevel(name) || !anthropicEffortSupported(fields[name]) {
			continue
		}
		levels = appendAnthropicEffort(levels, seen, name)
	}
	return levels, len(levels) > 0
}

func orderedRawObjectKeys(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	keys := make([]string, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return keys
		}
		name, ok := token.(string)
		if !ok {
			return keys
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return keys
		}
		keys = append(keys, name)
	}
	return keys
}

func anthropicEffortSupported(raw json.RawMessage) bool {
	var level map[string]json.RawMessage
	if json.Unmarshal(raw, &level) != nil || level == nil {
		return false
	}
	var supported *bool
	return json.Unmarshal(level["supported"], &supported) == nil && supported != nil && *supported
}

func appendAnthropicEffort(levels []string, seen map[string]bool, value string) []string {
	if value == "" || len(value) >= 16 || seen[value] || len(levels) >= 10 {
		return levels
	}
	seen[value] = true
	return append(levels, value)
}

func isEffortLadderLevel(value string) bool { return effortRank(value) >= 0 }

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func safeAnthropicCursor(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

package openrouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/luank3/gax/internal/catalog"
)

// Model contains the OpenRouter metadata used by model pickers and effort selection.
type Model struct {
	Description   string
	ContextWindow int64
	MaxOutput     int64
	ImageInput    catalog.Support
	Tools         catalog.Support
	Efforts       []string
	EffortsKnown  bool
	Rates         *catalog.Entry
}

// ParseModel reads OpenRouter's model catalog entry. Missing or malformed capability lists stay
// unknown; a valid list that omits a capability means the model does not support it.
func ParseModel(data []byte) (Model, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil || entry == nil {
		if err == nil {
			err = fmt.Errorf("expected a JSON object")
		}
		return Model{}, fmt.Errorf("decode OpenRouter model: %w", err)
	}
	model := Model{ImageInput: catalog.SupportUnknown, Tools: catalog.SupportUnknown}
	model.ContextWindow = positiveInteger(entry["context_length"])
	model.MaxOutput = positiveInteger(entry["max_output_tokens"])
	if model.MaxOutput == 0 {
		var topProvider map[string]json.RawMessage
		if json.Unmarshal(entry["top_provider"], &topProvider) == nil {
			model.MaxOutput = positiveInteger(topProvider["max_completion_tokens"])
		}
	}
	var architecture map[string]json.RawMessage
	if json.Unmarshal(entry["architecture"], &architecture) == nil {
		model.ImageInput = capabilityFromArray(architecture["input_modalities"], "image")
	}
	model.Tools = capabilityFromArray(entry["supported_parameters"], "tools")
	model.Rates = parsePricing(entry["pricing"])
	var reasoning map[string]json.RawMessage
	if json.Unmarshal(entry["reasoning"], &reasoning) == nil && reasoning != nil {
		if levels, ok := stringArray(reasoning["supported_efforts"]); ok {
			model.EffortsKnown = true
			model.Efforts = levels
		}
	} else if parameters, ok := stringArray(entry["supported_parameters"]); ok {
		model.EffortsKnown = true
		for _, parameter := range parameters {
			if parameter == "reasoning" || parameter == "reasoning_effort" {
				model.EffortsKnown = false
				break
			}
		}
	}
	var description string
	if json.Unmarshal(entry["description"], &description) == nil {
		if end := strings.IndexAny(description, "\r\n"); end >= 0 {
			description = description[:end]
		}
		lead := strings.TrimRight(description, " \t")
		model.Description = lead
	}
	return model, nil
}

func parsePricing(raw json.RawMessage) *catalog.Entry {
	var pricing map[string]json.RawMessage
	if json.Unmarshal(raw, &pricing) != nil || pricing == nil {
		return nil
	}
	entry := &catalog.Entry{
		CostInput: -1, CostOutput: -1, CostCacheRead: -1, CostCacheWrite: -1,
		CostCacheWrite1H: -1, ImageInput: catalog.SupportUnknown,
	}
	entry.CostInput = tokenRate(pricing["prompt"])
	entry.CostOutput = tokenRate(pricing["completion"])
	entry.CostCacheRead = tokenRate(pricing["input_cache_read"])
	entry.CostCacheWrite = tokenRate(pricing["input_cache_write"])
	entry.CostCacheWrite1H = tokenRate(pricing["input_cache_write_1h"])
	var overrides []json.RawMessage
	if json.Unmarshal(pricing["overrides"], &overrides) == nil && overrides != nil {
		for _, rawTier := range overrides {
			if len(entry.Tiers) >= catalog.MaxTiers {
				break
			}
			var override map[string]json.RawMessage
			if json.Unmarshal(rawTier, &override) != nil || override == nil {
				continue
			}
			threshold := positiveInteger(override["min_prompt_tokens"])
			if threshold == 0 {
				continue
			}
			entry.Tiers = append(entry.Tiers, catalog.Tier{
				ContextThreshold: threshold, CostInput: tokenRate(override["prompt"]),
				CostOutput: tokenRate(override["completion"]), CostCacheRead: tokenRate(override["input_cache_read"]),
				CostCacheWrite: tokenRate(override["input_cache_write"]), CostCacheWrite1H: tokenRate(override["input_cache_write_1h"]),
			})
		}
		entry.TiersDeclared = len(entry.Tiers) > 0
	}
	return entry
}

// OpenRouter prices are USD per token; runtime catalog entries store USD per million tokens.
func tokenRate(raw json.RawMessage) float64 {
	var text string
	if json.Unmarshal(raw, &text) != nil || text == "" {
		return -1
	}
	rate, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return -1
	}
	rate *= 1e6
	if math.IsInf(rate, 0) || math.IsNaN(rate) {
		return -1
	}
	return rate
}

func positiveInteger(raw json.RawMessage) int64 {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return 0
	}
	value, err := number.Int64()
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func capabilityFromArray(raw json.RawMessage, capability string) catalog.Support {
	values, ok := stringArray(raw)
	if !ok {
		return catalog.SupportUnknown
	}
	for _, value := range values {
		if value == capability {
			return catalog.SupportYes
		}
	}
	return catalog.SupportNo
}

func stringArray(raw json.RawMessage) ([]string, bool) {
	var values []json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		var text string
		if json.Unmarshal(value, &text) == nil {
			result = append(result, text)
		}
	}
	return result, true
}

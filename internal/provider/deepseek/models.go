package deepseek

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/catalog"
)

// Model contains the DeepSeek metadata used by model selection and effort handling.
type Model struct {
	Description   string
	ContextWindow int64
	MaxOutput     int64
	ImageInput    catalog.Support
	Efforts       []string
	EffortsKnown  bool
}

// ParseModel reads one DeepSeek /models entry. Missing capability lists stay unknown.
func ParseModel(data []byte) (Model, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil || entry == nil {
		if err == nil {
			err = fmt.Errorf("expected a JSON object")
		}
		return Model{}, fmt.Errorf("decode DeepSeek model: %w", err)
	}
	model := Model{ImageInput: catalog.SupportUnknown}
	model.ContextWindow = positiveInteger(entry["context_window"])
	model.MaxOutput = positiveInteger(entry["max_output_tokens"])
	model.ImageInput = capabilityFromArray(entry["input_modalities"], "image")

	var effort map[string]json.RawMessage
	if json.Unmarshal(entry["effort"], &effort) == nil && effort != nil {
		var levels []json.RawMessage
		if json.Unmarshal(effort["supported_levels"], &levels) == nil && levels != nil {
			model.EffortsKnown = true
			model.Efforts = append(model.Efforts, "none")
			for _, raw := range levels {
				var level string
				if json.Unmarshal(raw, &level) == nil && level != "" && !contains(model.Efforts, level) {
					model.Efforts = append(model.Efforts, level)
				}
			}
		}
	}
	if json.Unmarshal(entry["name"], &model.Description) != nil {
		model.Description = ""
	}
	return model, nil
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
	var values []json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		return catalog.SupportUnknown
	}
	for _, value := range values {
		var text string
		if json.Unmarshal(value, &text) == nil && text == capability {
			return catalog.SupportYes
		}
	}
	return catalog.SupportNo
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

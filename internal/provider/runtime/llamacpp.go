package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/transport"
)

// DiscoverModel reconciles llama.cpp's configured model with the server's current catalog.
// Discovered server choices are run-scoped because the loaded model can change between launches.
func (provider *Provider) DiscoverModel(ctx context.Context, client *transport.Client,
	configured string) (model string, discovered bool, warning string, err error) {
	if provider == nil || provider.Definition == nil || provider.Definition.ID != "llamacpp" {
		return configured, false, "", fmt.Errorf("provider does not support model discovery")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	models, listErr := provider.ListModels(ctx, client, nil)
	if listErr != nil {
		var fetchErr modelListFetchError
		if configured != "" && errors.As(listErr, &fetchErr) {
			return configured, false, "", nil
		}
		return "", false, "", listErr
	}
	if len(models) == 0 {
		return "", false, "llama.cpp: no models available — start llama-server with -m, -hf, or --models-dir", nil
	}
	firstModel := models[0].ID
	router := false
	runningID := ""
	runningCount := 0
	configuredID := ""
	for _, candidate := range models {
		if candidate.StatusObject {
			router = true
			if candidate.Status == "loaded" || candidate.Status == "loading" || candidate.Status == "sleeping" {
				runningID = candidate.ID
				runningCount++
			}
		}
		if configured != "" && configuredID == "" && llamaModelMatches(candidate, configured) {
			configuredID = candidate.ID
		}
	}
	if configured == "" {
		if !router {
			return firstModel, true, "", nil
		}
		if runningCount == 1 {
			return runningID, true, "", nil
		}
		return "", false, "", nil
	}
	if configuredID != "" {
		return configuredID, false, "", nil
	}
	if router {
		return "", false, fmt.Sprintf("llama.cpp: model '%s' is not in the router catalog — pick one with /model", llamaModelLabel(configured)), nil
	}
	return firstModel, true, LlamaModelWarning(configured, firstModel), nil
}

func parseLlamaModelDetails(raw json.RawMessage) (aliases []string, hasStatus bool, status string) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return nil, false, ""
	}
	var aliasValues []json.RawMessage
	if json.Unmarshal(entry["aliases"], &aliasValues) == nil {
		for _, rawAlias := range aliasValues {
			alias := modelID(rawAlias)
			if alias != "" {
				aliases = append(aliases, alias)
			}
		}
	}
	var statusObject map[string]json.RawMessage
	if json.Unmarshal(entry["status"], &statusObject) == nil && statusObject != nil {
		hasStatus = true
		status = rawString(statusObject["value"])
	}
	return aliases, hasStatus, status
}

func parseLlamaModelMetadata(raw json.RawMessage) (contextWindow int64, imageInput catalog.Support, description string) {
	imageInput = catalog.SupportUnknown
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return 0, imageInput, ""
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(entry["meta"], &metadata)
	var contextValue int64
	if json.Unmarshal(metadata["n_ctx"], &contextValue) == nil && contextValue > 0 {
		contextWindow = contextValue
	}
	var architecture map[string]json.RawMessage
	_ = json.Unmarshal(entry["architecture"], &architecture)
	var modalities []string
	if json.Unmarshal(architecture["input_modalities"], &modalities) == nil {
		imageInput = catalog.SupportNo
		for _, modality := range modalities {
			if modality == "image" {
				imageInput = catalog.SupportYes
				break
			}
		}
	}
	var status map[string]json.RawMessage
	_ = json.Unmarshal(entry["status"], &status)
	if status != nil {
		var failed bool
		_ = json.Unmarshal(status["failed"], &failed)
		var exitCode int64
		if failed {
			if json.Unmarshal(status["exit_code"], &exitCode) == nil {
				description = fmt.Sprintf("failed (exit %d)", exitCode)
			} else {
				description = "failed"
			}
		} else {
			state := rawString(status["value"])
			if state != "" && state != "unloaded" {
				description = state
			}
		}
	}
	return contextWindow, imageInput, description
}

func llamaModelMatches(model ModelInfo, configured string) bool {
	if model.ID == configured {
		return true
	}
	for _, alias := range model.Aliases {
		if alias == configured {
			return true
		}
	}
	return false
}

// LlamaModelWarning formats the configured and server model names for user-facing diagnostics.
func LlamaModelWarning(configured, served string) string {
	configuredLabel, servedLabel := llamaModelLabel(configured), llamaModelLabel(served)
	if configuredLabel == servedLabel {
		return fmt.Sprintf("llama.cpp: configured model is not served — using '%s'", servedLabel)
	}
	return fmt.Sprintf("llama.cpp: model '%s' is not served — using '%s'", configuredLabel, servedLabel)
}

func llamaModelLabel(model string) string {
	if len(model) <= len(".gguf") || !strings.EqualFold(model[len(model)-len(".gguf"):], ".gguf") {
		return model
	}
	filename := strings.LastIndexAny(model, "/\\")
	stem := model[filename+1 : len(model)-len(".gguf")]
	if stem == "" {
		return model
	}
	return stem
}

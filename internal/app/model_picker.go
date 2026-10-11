package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/provider/modelsort"
	"github.com/luank3/gax/internal/provider/runtime"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
)

func modelsForPicker(provider *runtime.Provider, models []runtime.ModelInfo) []runtime.ModelInfo {
	if provider == nil || !provider.SortModels() || len(models) < 2 {
		return models
	}
	ordered := append([]runtime.ModelInfo(nil), models...)
	sort.Slice(ordered, func(first, second int) bool {
		return modelsort.CompareIDs(ordered[first].ID, ordered[second].ID) < 0
	})
	return ordered
}

func modelPickerItem(provider *runtime.Provider, model runtime.ModelInfo, current bool) terminalpicker.Item {
	item := terminalpicker.Item{
		Label: terminalSafeText(model.ID), Description: modelPickerDescription(provider, model), Current: current,
	}
	if model.Tools == catalog.SupportNo {
		item.Dim = true
		item.Detail = "no tool calling"
	}
	return item
}

func modelPickerDescription(provider *runtime.Provider, model runtime.ModelInfo) string {
	metadata := catalog.Entry{ImageInput: catalog.SupportUnknown, CostInput: -1, CostOutput: -1,
		CostCacheRead: -1}
	if provider != nil {
		if resolved, found := provider.ModelMetadata(model.ID); found {
			metadata = resolved
		}
	}
	if metadata.ContextWindow <= 0 {
		metadata.ContextWindow = model.ContextWindow
	}
	if metadata.ImageInput == catalog.SupportUnknown {
		metadata.ImageInput = model.ImageInput
	}
	if model.Rates != nil {
		if metadata.CostInput < 0 {
			metadata.CostInput = model.Rates.CostInput
		}
		if metadata.CostOutput < 0 {
			metadata.CostOutput = model.Rates.CostOutput
		}
		if metadata.CostCacheRead < 0 {
			metadata.CostCacheRead = model.Rates.CostCacheRead
		}
	}

	segments := make([]string, 0, 3)
	if metadata.ContextWindow > 0 {
		contextDescription := formatTokens(metadata.ContextWindow) + " context"
		if model.MaxContext > metadata.ContextWindow {
			contextDescription += " (up to " + formatTokens(model.MaxContext) + ")"
		}
		segments = append(segments, contextDescription)
	}
	if metadata.ImageInput == catalog.SupportNo {
		segments = append(segments, "no images")
	}
	if metadata.CostInput >= 0 && metadata.CostOutput >= 0 {
		if metadata.CostInput == 0 && metadata.CostOutput == 0 {
			segments = append(segments, "free")
		} else if metadata.CostCacheRead >= 0 {
			segments = append(segments, fmt.Sprintf("$%.3g in / $%.3g cached / $%.3g out per Mtok",
				metadata.CostInput, metadata.CostCacheRead, metadata.CostOutput))
		} else {
			segments = append(segments, fmt.Sprintf("$%.3g in / $%.3g out per Mtok",
				metadata.CostInput, metadata.CostOutput))
		}
	}
	description := strings.Join(segments, " · ")
	if model.Description != "" {
		prose := make([]string, 0, 1)
		for _, line := range strings.Split(model.Description, "\n") {
			prose = append(prose, terminalSafeText(line))
		}
		proseDescription := strings.Join(prose, "\n")
		if description == "" {
			return proseDescription
		}
		return description + "\n" + proseDescription
	}
	return description
}

package agent

import (
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/runtime"
)

// AddTurnUsage appends one served provider attempt with exact charges, catalog estimates, and
// the identities needed to explain the request later. It returns false when neither usage nor
// elapsed time was reported.
func (state *Session) AddTurnUsage(provider *runtime.Provider, usage *conversation.Usage,
	elapsedMS int64, response *conversation.StreamResponse, origin conversation.Origin) bool {
	if state == nil {
		return false
	}
	if usage == nil {
		usage = &conversation.Usage{}
	}
	if !usageReported(usage) && elapsedMS < 0 {
		return false
	}
	footer := cloneUsage(usage)
	footer.ElapsedMS = conversation.Int64(elapsedMS)
	footer.UncachedInput = conversation.Int64(defaultUncachedInput(usage))
	if footer.Cost == nil || *footer.Cost < 0 {
		footer.CostTotal = nil
		footer.CostEstimated = false
	} else {
		footer.CostTotal = clonePointer(footer.Cost)
		footer.CostEstimated = false
	}

	model := valueString(state.Model)
	if (tokenReported(usage.InputTokens) || tokenReported(usage.OutputTokens)) && model != "" && provider != nil {
		entry, found := provider.ModelMetadata(model)
		if found {
			var split catalog.Split
			estimate := catalog.Price(entry, optionalInt64(usage.InputTokens), optionalInt64(usage.OutputTokens),
				optionalInt64(usage.CachedTokens), optionalInt64(usage.CacheWriteTokens),
				optionalInt64(usage.CacheWrite1HTokens), &split)
			if estimate >= 0 {
				footer.UncachedInput = conversation.Int64(split.UncachedInputTokens)
				footer.CostInput = conversation.Float64(split.CostInput)
				footer.CostCacheRead = conversation.Float64(split.CostCacheRead)
				footer.CostCacheWrite = conversation.Float64(split.CostCacheWrite)
				footer.CostOutput = conversation.Float64(split.CostOutput)
				if footer.CostTotal == nil {
					footer.CostTotal = conversation.Float64(estimate)
					footer.CostEstimated = true
				}
			}
		}
	}

	if provider != nil && provider.DisplayName != "" &&
		(state.ProviderID == nil || provider.DisplayName != *state.ProviderID) {
		footer.ProviderLabel = conversation.String(provider.DisplayName)
	}
	if state.ModelLabel != nil && *state.ModelLabel != "" && *state.ModelLabel != model {
		footer.ModelLabel = cloneString(state.ModelLabel)
	}
	if state.Effort != nil && *state.Effort != "" {
		footer.Effort = cloneString(state.Effort)
	}
	if response != nil {
		if response.Model != nil && *response.Model != "" && *response.Model != model {
			footer.ServedModel = cloneString(response.Model)
		}
		footer.Route = cloneString(response.Route)
		footer.ResponseID = cloneString(response.ID)
	}
	item := conversation.Item{Kind: conversation.KindTurnUsage, Usage: footer, Origin: origin}
	item.Provider = cloneString(state.ProviderID)
	if model != "" {
		item.Model = conversation.String(model)
	}
	state.Append(item)
	return true
}

func defaultUncachedInput(usage *conversation.Usage) int64 {
	input := nonnegative(optionalInt64(usage.InputTokens))
	cached := nonnegative(optionalInt64(usage.CachedTokens))
	written := nonnegative(optionalInt64(usage.CacheWriteTokens))
	uncached := input - cached - written
	if uncached < 0 {
		return 0
	}
	return uncached
}

func optionalInt64(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func tokenReported(tokens *int64) bool { return tokens != nil && *tokens >= 0 }

func nonnegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

package agent

import (
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/runtime"
)

const (
	maxToolBreakdown  = 8
	maxModelBreakdown = 8
)

// UsageTotals describes requests and provider usage recorded in conversation footers.
type UsageTotals struct {
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheWriteTokens    int64
	UncachedInputTokens int64
	Spend               float64
	SpendEstimated      bool
	Unpriced            bool
	CostInput           float64
	CostCacheRead       float64
	CostCacheWrite      float64
	CostOutput          float64
	SplitAvailable      bool
}

type ToolCount struct {
	Name  string
	Count int64
}

type ModelUsage struct {
	Provider string
	Model    string
	Totals   UsageTotals
}

type ModelRateLookup func(providerID, model string) (catalog.Entry, bool)

// SessionStats is derived from the recorded conversation so resumed, undone, and forked
// sessions report the same accounting for the same items.
type SessionStats struct {
	UserTurns          int64
	UndoneUserTurns    int64
	ToolCalls          int64
	Tools              []ToolCount
	WorkedMS           int64
	ContextTokens      int64
	Total              UsageTotals
	Models             []ModelUsage
	InheritedUserTurns int64
	Inherited          UsageTotals
}

// CollectStats separates the live conversation from its accounting history. Retired turns
// remain in the request totals, while inherited fork context is reported separately because
// its usage was paid for by the source session.
func CollectStats(state *Session) SessionStats {
	return CollectStatsWithRateLookup(state, nil)
}

// CollectStatsWithProvider also resolves prices for older usage footers whose catalog rates were
// unavailable when the request was first recorded.
func CollectStatsWithProvider(state *Session, provider *runtime.Provider) SessionStats {
	if provider == nil || provider.Definition == nil {
		return CollectStatsWithRateLookup(state, nil)
	}
	return CollectStatsWithRateLookup(state, func(providerID, model string) (catalog.Entry, bool) {
		if providerID != provider.Definition.ID {
			return catalog.Entry{}, false
		}
		return provider.ModelMetadata(model)
	})
}

// CollectStatsWithRateLookup lets callers resolve catalog rates for every provider represented
// in a session, including providers no longer selected by the current run.
func CollectStatsWithRateLookup(state *Session, lookup ModelRateLookup) SessionStats {
	stats := SessionStats{ContextTokens: -1}
	if state == nil {
		return stats
	}
	stats.WorkedMS = state.WorkedMS
	stats.ContextTokens = state.LastContextTokens()
	for _, item := range state.Items {
		collectStatsItem(&stats, item, true, lookup)
	}
	for _, item := range state.Retired {
		collectStatsItem(&stats, item, false, lookup)
	}
	return stats
}

func collectStatsItem(stats *SessionStats, item conversation.Item, live bool, lookup ModelRateLookup) {
	if item.Inherited {
		if !live {
			return
		}
		if conversation.IsTypedPrompt(item) {
			stats.InheritedUserTurns++
		} else if item.Kind == conversation.KindTurnUsage && item.Usage != nil {
			accountUsage(&stats.Inherited, item, lookup)
		}
		return
	}

	if conversation.IsTypedPrompt(item) {
		if live {
			stats.UserTurns++
		} else {
			stats.UndoneUserTurns++
		}
		return
	}
	if item.Kind == conversation.KindToolCall {
		if live {
			stats.ToolCalls++
			if item.ToolName != nil {
				for index := range stats.Tools {
					if stats.Tools[index].Name == *item.ToolName {
						stats.Tools[index].Count++
						return
					}
				}
				if len(stats.Tools) < maxToolBreakdown {
					stats.Tools = append(stats.Tools, ToolCount{Name: *item.ToolName, Count: 1})
				}
			}
		}
		return
	}
	if item.Kind != conversation.KindTurnUsage || item.Usage == nil {
		return
	}
	accountUsage(&stats.Total, item, lookup)
	providerLabel := firstNonempty(item.Usage.ProviderLabel, item.Provider)
	model := firstNonempty(item.Usage.ModelLabel, item.Model)
	for index := range stats.Models {
		if stats.Models[index].Provider == providerLabel && stats.Models[index].Model == model {
			accountUsage(&stats.Models[index].Totals, item, lookup)
			return
		}
	}
	if len(stats.Models) >= maxModelBreakdown {
		return
	}
	usage := ModelUsage{Provider: providerLabel, Model: model}
	accountUsage(&usage.Totals, item, lookup)
	stats.Models = append(stats.Models, usage)
}

func accountUsage(totals *UsageTotals, item conversation.Item, lookup ModelRateLookup) {
	usage := item.Usage
	if usage == nil {
		return
	}
	totals.Requests++
	totals.InputTokens += positive(usage.InputTokens)
	totals.OutputTokens += positive(usage.OutputTokens)
	totals.CachedTokens += positive(usage.CachedTokens)
	totals.CacheWriteTokens += positive(usage.CacheWriteTokens)
	if usage.UncachedInput != nil && *usage.UncachedInput > 0 {
		totals.UncachedInputTokens += *usage.UncachedInput
	}

	cost := nonnegativeFloat(usage.CostTotal)
	if cost == nil {
		cost = nonnegativeFloat(usage.Cost)
	}
	reportedTokens := usage.InputTokens != nil && *usage.InputTokens >= 0 ||
		usage.OutputTokens != nil && *usage.OutputTokens >= 0

	splitKnown := nonnegativeFloat(usage.CostInput) != nil && nonnegativeFloat(usage.CostOutput) != nil
	var pricedSplit *catalog.Split
	var estimatedCost *float64
	if (cost == nil || !splitKnown) && reportedTokens && lookup != nil && item.Provider != nil && *item.Provider != "" &&
		item.Model != nil && *item.Model != "" {
		if entry, found := lookup(*item.Provider, *item.Model); found {
			var split catalog.Split
			price := catalog.Price(entry,
				optionalTokenCount(usage.InputTokens), optionalTokenCount(usage.OutputTokens),
				optionalTokenCount(usage.CachedTokens), optionalTokenCount(usage.CacheWriteTokens),
				optionalTokenCount(usage.CacheWrite1HTokens), &split)
			if price >= 0 {
				pricedSplit = &split
				splitKnown = true
				if cost == nil {
					estimatedCost = &price
				}
			}
		}
	}
	if cost == nil && estimatedCost == nil && !reportedTokens {
		return
	}
	if cost != nil && *cost >= 0 {
		totals.Spend += *cost
		if usage.CostEstimated {
			totals.SpendEstimated = true
		}
	} else if estimatedCost != nil {
		totals.Spend += *estimatedCost
		totals.SpendEstimated = true
	} else {
		totals.SpendEstimated = true
		totals.Unpriced = true
	}
	if pricedSplit != nil {
		totals.SplitAvailable = true
		totals.CostInput += pricedSplit.CostInput
		totals.CostCacheRead += pricedSplit.CostCacheRead
		totals.CostCacheWrite += pricedSplit.CostCacheWrite
		totals.CostOutput += pricedSplit.CostOutput
	} else if splitKnown {
		totals.SplitAvailable = true
		totals.CostInput += *nonnegativeFloat(usage.CostInput)
		totals.CostCacheRead += valueOrZero(usage.CostCacheRead)
		totals.CostCacheWrite += valueOrZero(usage.CostCacheWrite)
		totals.CostOutput += *nonnegativeFloat(usage.CostOutput)
	}
}

func positive(value *int64) int64 {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func optionalTokenCount(value *int64) int64 {
	if value == nil || *value < 0 {
		return -1
	}
	return *value
}

func nonnegativeFloat(value *float64) *float64 {
	if value == nil || *value < 0 {
		return nil
	}
	return value
}

func valueOrZero(value *float64) float64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func firstNonempty(primary, fallback *string) string {
	if primary != nil && *primary != "" {
		return *primary
	}
	if fallback != nil {
		return *fallback
	}
	return ""
}

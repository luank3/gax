package agent_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
)

func TestCollectStatsSeparatesLiveUndoneAndInheritedAccounting(t *testing.T) {
	state := &agent.Session{
		WorkedMS: 1234,
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("inherited prompt"), Inherited: true},
			usageItem("provider-a", "model-a", 20, 4, 0, 0, 20, 0.1, true, true),
			{Kind: conversation.KindUser, Text: conversation.String("live prompt")},
			{Kind: conversation.KindUser, Text: conversation.String("continue"), Origin: conversation.OriginContinuation},
			{Kind: conversation.KindToolCall, ToolName: conversation.String("bash")},
			{Kind: conversation.KindToolCall, ToolName: conversation.String("bash")},
			{Kind: conversation.KindToolCall, ToolName: conversation.String("write"), Inherited: true},
			usageItem("provider-a", "model-a", 100, 20, 30, 10, 60, 0.02, false, false),
		},
		Retired: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("undone prompt")},
			{Kind: conversation.KindToolCall, ToolName: conversation.String("bash")},
			usageItem("provider-a", "model-old", 200, 40, 0, 0, 150, 0.04, false, false),
			{Kind: conversation.KindUser, Text: conversation.String("retired inherited prompt"), Inherited: true},
			usageItem("provider-a", "model-a", 50, 5, 0, 0, 50, 0.5, false, true),
		},
	}

	got := agent.CollectStats(state)
	if got.UserTurns != 1 || got.UndoneUserTurns != 1 {
		t.Errorf("live and undone turns = %d and %d, want 1 and 1", got.UserTurns, got.UndoneUserTurns)
	}
	if got.ToolCalls != 2 || len(got.Tools) != 1 || got.Tools[0].Name != "bash" || got.Tools[0].Count != 2 {
		t.Errorf("live tool calls = %d, breakdown = %+v", got.ToolCalls, got.Tools)
	}
	if got.WorkedMS != 1234 || got.ContextTokens != 120 {
		t.Errorf("worked/context = %d/%d, want 1234/120", got.WorkedMS, got.ContextTokens)
	}
	wantTotal := agent.UsageTotals{
		Requests: 2, InputTokens: 300, OutputTokens: 60, CachedTokens: 30,
		CacheWriteTokens: 10, UncachedInputTokens: 210, Spend: 0.06,
	}
	if got.Total != wantTotal {
		t.Errorf("session totals = %+v, want %+v", got.Total, wantTotal)
	}
	if len(got.Models) != 2 || got.Models[0].Provider != "provider-a" || got.Models[0].Model != "model-a" ||
		got.Models[0].Totals.Requests != 1 || got.Models[1].Model != "model-old" || got.Models[1].Totals.Requests != 1 {
		t.Errorf("usage model breakdown = %+v", got.Models)
	}
	if got.InheritedUserTurns != 1 || got.Inherited.Requests != 1 || got.Inherited.InputTokens != 20 ||
		math.Abs(got.Inherited.Spend-0.1) > 1e-12 || !got.Inherited.SpendEstimated {
		t.Errorf("inherited totals = %+v, turns=%d", got.Inherited, got.InheritedUserTurns)
	}
}

func TestCollectStatsCountsDurationOnlyRequestWithoutInventingSpend(t *testing.T) {
	state := &agent.Session{Items: []conversation.Item{
		{Kind: conversation.KindTurnUsage, Usage: &conversation.Usage{ElapsedMS: conversation.Int64(12)}},
	}}
	got := agent.CollectStats(state)
	if got.Total.Requests != 1 || got.Total.Spend != 0 || got.Total.SpendEstimated || got.Total.Unpriced {
		t.Fatalf("duration-only request accounting = %+v", got.Total)
	}
}

func TestCollectStatsCapsToolAndModelBreakdownsButKeepsTotals(t *testing.T) {
	items := make([]conversation.Item, 0, 22)
	for index := 0; index < 10; index++ {
		name := fmt.Sprintf("tool-%d", index)
		model := fmt.Sprintf("model-%d", index)
		items = append(items,
			conversation.Item{Kind: conversation.KindToolCall, ToolName: conversation.String(name)},
			usageItem("provider-a", model, 1, 0, 0, 0, 0, 0, false, false),
		)
	}
	items = append(items,
		conversation.Item{Kind: conversation.KindToolCall, ToolName: conversation.String("tool-0")},
		usageItem("provider-a", "model-0", 1, 0, 0, 0, 0, 0, false, false),
	)

	got := agent.CollectStats(&agent.Session{Items: items})
	if got.ToolCalls != 11 || len(got.Tools) != 8 || got.Tools[0].Name != "tool-0" || got.Tools[0].Count != 2 {
		t.Errorf("tool calls = %d, breakdown = %+v; want 11 calls and first 8 tools with tool-0 counted twice", got.ToolCalls, got.Tools)
	}
	if got.Total.Requests != 11 || got.Total.InputTokens != 11 {
		t.Errorf("total usage = %+v; want 11 requests and input tokens", got.Total)
	}
	if len(got.Models) != 8 || got.Models[0].Model != "model-0" || got.Models[0].Totals.Requests != 2 {
		t.Errorf("model breakdown = %+v; want first 8 models with model-0 counted twice", got.Models)
	}
}

func TestCollectStatsAccumulatesRecordedRateSplit(t *testing.T) {
	item := usageItem("provider-a", "model-a", 100, 20, 30, 10, 60, 0.02, false, false)
	item.Usage.CostInput = conversation.Float64(0.001)
	item.Usage.CostCacheRead = conversation.Float64(0.002)
	item.Usage.CostCacheWrite = conversation.Float64(0.003)
	item.Usage.CostOutput = conversation.Float64(0.004)
	got := agent.CollectStats(&agent.Session{Items: []conversation.Item{item}}).Total
	want := agent.UsageTotals{
		Requests: 1, InputTokens: 100, OutputTokens: 20, CachedTokens: 30,
		CacheWriteTokens: 10, UncachedInputTokens: 60, Spend: 0.02,
		CostInput: 0.001, CostCacheRead: 0.002, CostCacheWrite: 0.003, CostOutput: 0.004,
		SplitAvailable: true,
	}
	if got != want {
		t.Fatalf("rate split totals = %+v, want %+v", got, want)
	}
}

func TestCollectStatsResolvesPricesForEarlierFooters(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"output":8,"cache_read":0.5}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	item := conversation.Item{
		Kind: conversation.KindTurnUsage, Provider: conversation.String("openrouter"), Model: conversation.String("m"),
		Usage: &conversation.Usage{
			InputTokens: conversation.Int64(1000), OutputTokens: conversation.Int64(10),
			CachedTokens: conversation.Int64(100), UncachedInput: conversation.Int64(900),
		},
	}
	got := agent.CollectStatsWithProvider(&agent.Session{Items: []conversation.Item{item}}, provider).Total
	if math.Abs(got.Spend-0.00193) > 1e-12 || !got.SpendEstimated || got.Unpriced || !got.SplitAvailable ||
		math.Abs(got.CostInput-0.0018) > 1e-12 || math.Abs(got.CostCacheRead-0.00005) > 1e-12 ||
		math.Abs(got.CostOutput-0.00008) > 1e-12 {
		t.Fatalf("catalog-resolved usage = %+v", got)
	}
}

func TestCollectStatsUsesRateLookupForOtherRecordedProviders(t *testing.T) {
	item := conversation.Item{
		Kind: conversation.KindTurnUsage, Provider: conversation.String("source-provider"), Model: conversation.String("m"),
		Usage: &conversation.Usage{InputTokens: conversation.Int64(1000), OutputTokens: conversation.Int64(10)},
	}
	lookups := 0
	stats := agent.CollectStatsWithRateLookup(&agent.Session{Items: []conversation.Item{item}},
		func(providerID, model string) (catalog.Entry, bool) {
			lookups++
			if providerID != "source-provider" || model != "m" {
				return catalog.Entry{}, false
			}
			return catalog.Entry{CostInput: 2, CostOutput: 8, CostCacheRead: -1, CostCacheWrite: -1, CostCacheWrite1H: -1}, true
		})
	if lookups == 0 || math.Abs(stats.Total.Spend-0.00208) > 1e-12 || !stats.Total.SpendEstimated || stats.Total.Unpriced {
		t.Fatalf("other-provider price lookup = %+v, calls=%d", stats.Total, lookups)
	}
}

func usageItem(provider, model string, input, output, cached, cacheWrite, uncached int64, cost float64, estimated, inherited bool) conversation.Item {
	return conversation.Item{
		Kind: conversation.KindTurnUsage, Provider: conversation.String(provider), Model: conversation.String(model), Inherited: inherited,
		Usage: &conversation.Usage{
			InputTokens: conversation.Int64(input), OutputTokens: conversation.Int64(output),
			CachedTokens: conversation.Int64(cached), CacheWriteTokens: conversation.Int64(cacheWrite),
			UncachedInput: conversation.Int64(uncached), CostTotal: conversation.Float64(cost), CostEstimated: estimated,
		},
	}
}

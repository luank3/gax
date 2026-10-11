package agent_test

import (
	"math"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
)

func TestAddTurnUsageEstimatesCatalogCostAndRecordsProvenance(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"display_name":"Custom Router"}},"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":3}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	state := agent.Session{
		ProviderID: conversation.String("openrouter"),
		Model:      conversation.String("m"),
		ModelLabel: conversation.String("Custom display model"),
		Effort:     conversation.String("high"),
	}
	usage := &conversation.Usage{
		InputTokens: conversation.Int64(1000), OutputTokens: conversation.Int64(10),
		CachedTokens: conversation.Int64(100), CacheWriteTokens: conversation.Int64(300),
	}
	response := &conversation.StreamResponse{ID: conversation.String("response-1"), Model: conversation.String("m-actual"), Route: conversation.String("upstream-a")}
	if !state.AddTurnUsage(provider, usage, 25, response, conversation.Origin("")) {
		t.Fatal("reported usage should append a footer")
	}
	if len(state.Items) != 1 || state.Items[0].Kind != conversation.KindTurnUsage || state.Items[0].Usage == nil {
		t.Fatalf("usage footer was not appended: %+v", state.Items)
	}
	footer := state.Items[0]
	wantSplit := catalog.Split{UncachedInputTokens: 600, CostInput: 0.0012, CostCacheRead: 0.00005, CostCacheWrite: 0.0009, CostOutput: 0.00008}
	if footer.Usage.UncachedInput == nil || *footer.Usage.UncachedInput != wantSplit.UncachedInputTokens ||
		footer.Usage.CostInput == nil || math.Abs(*footer.Usage.CostInput-wantSplit.CostInput) > 1e-12 ||
		footer.Usage.CostCacheRead == nil || math.Abs(*footer.Usage.CostCacheRead-wantSplit.CostCacheRead) > 1e-12 ||
		footer.Usage.CostCacheWrite == nil || math.Abs(*footer.Usage.CostCacheWrite-wantSplit.CostCacheWrite) > 1e-12 ||
		footer.Usage.CostOutput == nil || math.Abs(*footer.Usage.CostOutput-wantSplit.CostOutput) > 1e-12 {
		t.Fatalf("catalog price split differs: %+v", footer.Usage)
	}
	if footer.Usage.CostTotal == nil || math.Abs(*footer.Usage.CostTotal-0.00223) > 1e-12 || !footer.Usage.CostEstimated {
		t.Fatalf("catalog total should be marked estimated: %+v", footer.Usage)
	}
	if footer.Usage.ElapsedMS == nil || *footer.Usage.ElapsedMS != 25 || footer.Usage.ProviderLabel == nil || *footer.Usage.ProviderLabel != "Custom Router" ||
		footer.Usage.ModelLabel == nil || *footer.Usage.ModelLabel != "Custom display model" || footer.Usage.Effort == nil || *footer.Usage.Effort != "high" ||
		footer.Usage.ServedModel == nil || *footer.Usage.ServedModel != "m-actual" || footer.Usage.Route == nil || *footer.Usage.Route != "upstream-a" ||
		footer.Usage.ResponseID == nil || *footer.Usage.ResponseID != "response-1" {
		t.Fatalf("usage provenance differs: %+v", footer.Usage)
	}
	if footer.Provider == nil || *footer.Provider != "openrouter" || footer.Model == nil || *footer.Model != "m" {
		t.Fatalf("usage item should carry stable wire identity: %+v", footer)
	}
}

func TestAddTurnUsageKeepsExactProviderCostAndUnknownFields(t *testing.T) {
	state := agent.Session{Model: conversation.String("m")}
	exact := &conversation.Usage{InputTokens: conversation.Int64(10), OutputTokens: conversation.Int64(5), Cost: conversation.Float64(0.012)}
	if !state.AddTurnUsage(nil, exact, 0, nil, conversation.OriginCompaction) {
		t.Fatal("provider-reported usage should append a footer without catalog metadata")
	}
	footer := state.Items[0]
	if footer.Origin != conversation.OriginCompaction || footer.Usage.CostTotal == nil || *footer.Usage.CostTotal != 0.012 || footer.Usage.CostEstimated {
		t.Fatalf("exact provider cost should take precedence: %+v", footer)
	}
	if footer.Usage.CostInput != nil || footer.Usage.CostOutput != nil {
		t.Fatalf("cost categories must stay unknown without model rates: %+v", footer.Usage)
	}

	unknown := agent.NewSession()
	if unknown.AddTurnUsage(nil, &conversation.Usage{}, -1, nil, conversation.Origin("")) {
		t.Fatal("unreported usage without elapsed time should not append a footer")
	}
	if !unknown.AddTurnUsage(nil, &conversation.Usage{}, 9, nil, conversation.Origin("")) || len(unknown.Items) != 1 {
		t.Fatal("elapsed time should be enough to retain a served attempt")
	}
}

func TestAddTurnUsageDoesNotEstimateWhenBothTokenCountsAreUnknown(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"output":8}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	state := agent.Session{Model: conversation.String("m")}
	unknown := &conversation.Usage{InputTokens: conversation.Int64(-1), OutputTokens: conversation.Int64(-1)}
	if !state.AddTurnUsage(provider, unknown, 1, nil, conversation.Origin("")) {
		t.Fatal("elapsed attempt should still be recorded")
	}
	footer := state.Items[0].Usage
	if footer.CostInput != nil || footer.CostOutput != nil || footer.CostTotal != nil || footer.CostEstimated {
		t.Fatalf("unknown token counts must not turn into a zero estimate: %+v", footer)
	}
}

func TestAddTurnUsageRetainsLabelsWithoutStableWireIDs(t *testing.T) {
	state := agent.NewSession()
	provider := &runtime.Provider{DisplayName: "Custom Name"}
	state.AddTurnUsage(provider, &conversation.Usage{InputTokens: conversation.Int64(1)}, 0,
		&conversation.StreamResponse{Model: conversation.String("served")}, conversation.Origin(""))
	footer := state.Items[0].Usage
	if footer.ProviderLabel == nil || *footer.ProviderLabel != "Custom Name" || footer.ServedModel == nil || *footer.ServedModel != "served" {
		t.Fatalf("display provenance should survive missing request ids: %+v", footer)
	}
}

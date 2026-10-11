package catalog_test

import (
	"math"
	"testing"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
)

func TestLookupMergesRuntimeProviderCatalogAndSnapshotFields(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"runtime":{"m":{"limit":{"context":"872k"}}},"openai":{"m":{"api":"anthropic-messages","cost":{"input":99},"limit":{"output":64000}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	catalogue := catalog.New(store)
	if err := catalogue.LoadSnapshot([]byte(`{"openai":{"npm":"@ai-sdk/openai","models":{"m":{"cost":{"input":2,"output":8,"cache_read":0.5},"limit":{"context":200000,"output":100000},"modalities":{"input":["text","image"]},"reasoning_options":[{"type":"effort","values":["low","high"]}],"interleaved":{"field":"reasoning_content"}}}}}`)); err != nil {
		t.Fatal(err)
	}

	entry, found := catalogue.Lookup("runtime", "openai", "m")
	if !found {
		t.Fatal("expected merged catalog metadata")
	}
	if entry.CostInput != 99 || entry.CostOutput != 8 || entry.CostCacheRead != 0.5 || entry.ContextWindow != 872000 || entry.MaxOutput != 64000 {
		t.Fatalf("metadata fields did not merge by precedence: %+v", entry)
	}
	if entry.ImageInput != catalog.SupportYes || entry.API != "anthropic-messages" || entry.InterleavedField != "reasoning_content" {
		t.Fatalf("catalog and config metadata were not applied: %+v", entry)
	}
	if !entry.EffortsKnown || len(entry.Efforts) != 2 || entry.Efforts[0] != "low" || entry.Efforts[1] != "high" {
		t.Fatalf("effort ladder did not parse: %+v", entry)
	}
}

func TestLookupPreservesUnknownAndExplicitOffMetadata(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openai":{"empty":{"reasoning":false,"interleaved":false},"text-only":{"modalities":{"input":["text"]}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	catalogue := catalog.New(store)
	if err := catalogue.LoadSnapshot([]byte(`{"openai":{"npm":"@ai-sdk/openai","models":{"known":{"cost":{"input":1,"output":2},"provider":{"npm":"@ai-sdk/anthropic"}},"global":{"cost":{"input":1,"output":2}},"unsupported":{"provider":{"npm":"@other/sdk"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	known, found := catalogue.Lookup("openai", "openai", "known")
	if !found || known.API != "anthropic-messages" {
		t.Fatalf("model-level SDK selector should override the provider hint: %+v found=%t", known, found)
	}
	global, found := catalogue.Lookup("openai", "openai", "global")
	if !found || global.API != "openai-responses" {
		t.Fatalf("provider-level SDK selector was not applied: %+v found=%t", global, found)
	}
	unsupported, found := catalogue.Lookup("openai", "openai", "unsupported")
	if !found || unsupported.API != "unsupported" {
		t.Fatalf("unknown SDK selector should be explicit and unsupported: %+v found=%t", unsupported, found)
	}
	empty, found := catalogue.Lookup("openai", "openai", "empty")
	if !found || !empty.EffortsKnown || len(empty.Efforts) != 0 || !empty.InterleavedDeclared || empty.InterleavedField != "" {
		t.Fatalf("explicit off values were lost: %+v found=%t", empty, found)
	}
	textOnly, found := catalogue.Lookup("openai", "openai", "text-only")
	if !found || textOnly.ImageInput != catalog.SupportNo {
		t.Fatalf("declared text-only modalities should mean no image input: %+v found=%t", textOnly, found)
	}
	missing, found := catalogue.Lookup("openai", "openai", "missing")
	if found || missing.CostInput != -1 || missing.ImageInput != catalog.SupportUnknown || missing.ContextWindow != 0 || missing.EffortsKnown {
		t.Fatalf("missing metadata should remain unknown: %+v found=%t", missing, found)
	}
}

func TestInvalidSnapshotDoesNotReplaceLastValidSnapshot(t *testing.T) {
	catalogue := catalog.New(nil)
	if err := catalogue.LoadSnapshot([]byte(`{"provider":{"models":{"m":{"cost":{"input":1,"output":2}}}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := catalogue.LoadSnapshot([]byte(`{"error":"not a models.dev snapshot"}`)); err == nil {
		t.Fatal("expected provider-shaped snapshot validation to fail")
	}
	entry, found := catalogue.Lookup("", "provider", "m")
	if !found || entry.CostInput != 1 {
		t.Fatalf("invalid replacement damaged the last valid snapshot: %+v found=%t", entry, found)
	}
}

func TestNullRatesStayUnknown(t *testing.T) {
	catalogue := catalog.New(nil)
	if err := catalogue.LoadSnapshot([]byte(`{"provider":{"models":{"m":{"cost":{"input":null,"output":null}}}}}`)); err != nil {
		t.Fatal(err)
	}
	entry, found := catalogue.Lookup("", "provider", "m")
	if found || entry.CostInput != -1 || entry.CostOutput != -1 {
		t.Fatalf("JSON null must not become a zero-cost rate: %+v found=%t", entry, found)
	}
}

func TestTiersParseContextSelectorsAndEmptyConfigDisablesSnapshotTiers(t *testing.T) {
	snapshot := []byte(`{"openai":{"models":{"m":{"cost":{"input":2,"output":8,"tiers":[{"input":4,"output":16,"tier":{"type":"context","size":"200k"}},{"input":9,"tier":{"type":"tokens_per_day","size":5}}]}}}}}`)
	catalogue := catalog.New(nil)
	if err := catalogue.LoadSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	entry, found := catalogue.Lookup("", "openai", "m")
	if !found || !entry.TiersDeclared || len(entry.Tiers) != 1 || entry.Tiers[0].ContextThreshold != 200000 || entry.Tiers[0].CostInput != 4 {
		t.Fatalf("context pricing tiers were not parsed: %+v found=%t", entry, found)
	}
	if got := catalog.Price(entry, 300000, 100000, 0, 0, 0, nil); got != 2.8 {
		t.Fatalf("context tier should replace all base rates above its threshold: %f", got)
	}

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"catalog":{"models":{"openai":{"m":{"cost":{"tiers":[]}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	catalogue = catalog.New(store)
	if err := catalogue.LoadSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	entry, found = catalogue.Lookup("", "openai", "m")
	if !found || !entry.TiersDeclared || len(entry.Tiers) != 0 || entry.CostInput != 2 {
		t.Fatalf("declared empty config tiers should replace the snapshot list but preserve rates: %+v found=%t", entry, found)
	}
}

func TestPriceUsesContextTiersAndSeparatesSurchargeWrites(t *testing.T) {
	entry := catalog.Entry{
		CostInput: 2, CostOutput: 8, CostCacheRead: 0.2, CostCacheWrite: 0.375, CostCacheWrite1H: -1,
		Tiers: []catalog.Tier{{ContextThreshold: 200000, CostInput: 10, CostOutput: -1, CostCacheRead: -1, CostCacheWrite: -1, CostCacheWrite1H: -1}},
	}
	if catalog.CacheWriteReplacesInput(entry) {
		t.Fatal("a write rate below input should be a surcharge")
	}
	var split catalog.Split
	cost := catalog.Price(entry, 300000, 0, 2000, 1000, 500, &split)
	want := (298000*10.0 + 2000*0.2 + 500*0.375 + 500*20.0) / 1e6
	if math.Abs(cost-want) > 1e-12 {
		t.Fatalf("tiered surcharge cost differs: got %.12f want %.12f split=%+v", cost, want, split)
	}
	if split.UncachedInputTokens != 298000 || split.CostInput != 2.98 || split.CostCacheRead != 0.0004 || math.Abs(split.CostCacheWrite-0.0101875) > 1e-12 {
		t.Fatalf("surcharge tokens must remain in input while cache charges stay separate: %+v", split)
	}
}

func TestPriceUnknownRatesAndReplacementWrites(t *testing.T) {
	unknown := catalog.Entry{CostInput: -1, CostOutput: 8, CostCacheWrite: -1}
	if got := catalog.Price(unknown, 1000, 1000, 0, 0, 0, nil); got != -1 {
		t.Fatalf("unknown input rate should suppress estimates, got %f", got)
	}
	entry := catalog.Entry{CostInput: 2, CostOutput: 8, CostCacheRead: 0.5, CostCacheWrite: 2.5, CostCacheWrite1H: -1}
	if !catalog.CacheWriteReplacesInput(entry) {
		t.Fatal("write rate at or above input should replace the ordinary input charge")
	}
	var split catalog.Split
	got := catalog.Price(entry, 1000000, 1000000, 500000, 250000, 100000, &split)
	if math.Abs(got-9.525) > 1e-12 || split.UncachedInputTokens != 250000 || split.CostCacheWrite != 0.775 {
		t.Fatalf("replacement pricing differs: cost=%f split=%+v", got, split)
	}
}

package openrouter_test

import (
	"reflect"
	"testing"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/provider/openrouter"
)

func TestParseModelReadsOpenRouterMetadata(t *testing.T) {
	model, err := openrouter.ParseModel([]byte(`{"id":"anthropic/claude-opus-5-fast","description":"Fast variant. \rMore details.","context_length":1000000,"max_output_tokens":128000,"architecture":{"input_modalities":["text","image"]},"reasoning":{"supported_efforts":["high","medium"]},"supported_parameters":["tools","reasoning"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if model.Description != "Fast variant." || model.ContextWindow != 1000000 || model.MaxOutput != 128000 ||
		model.ImageInput != catalog.SupportYes || !model.EffortsKnown || !reflect.DeepEqual(model.Efforts, []string{"high", "medium"}) {
		t.Fatalf("OpenRouter metadata was not parsed: %+v", model)
	}
}

func TestParseModelDistinguishesUnknownAndUnsupportedCapabilities(t *testing.T) {
	unknown, err := openrouter.ParseModel([]byte(`{"id":"vendor/model","supported_parameters":["reasoning"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if unknown.ImageInput != catalog.SupportUnknown || unknown.EffortsKnown {
		t.Fatalf("missing metadata should remain unknown: %+v", unknown)
	}
	unsupported, err := openrouter.ParseModel([]byte(`{"id":"vendor/model","architecture":{"input_modalities":["text"]},"supported_parameters":["max_tokens"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.ImageInput != catalog.SupportNo || !unsupported.EffortsKnown || len(unsupported.Efforts) != 0 {
		t.Fatalf("explicitly absent capabilities should be known unsupported: %+v", unsupported)
	}
}

func TestParseModelFallsBackToTopProviderOutputLimit(t *testing.T) {
	model, err := openrouter.ParseModel([]byte(`{"id":"vendor/model","max_output_tokens":0,"top_provider":{"max_completion_tokens":64000}}`))
	if err != nil || model.MaxOutput != 64000 {
		t.Fatalf("max output fallback = %+v, %v", model, err)
	}
}

func TestParseModelReadsPricingTiersAndToolSupport(t *testing.T) {
	model, err := openrouter.ParseModel([]byte(`{"id":"anthropic/claude-sonnet","supported_parameters":["tools"],"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375","input_cache_write_1h":"0.000006","overrides":[{"min_prompt_tokens":200000,"prompt":"0.000006","completion":"0.0000225","input_cache_read":"0.0000006","input_cache_write":"0.0000075"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	rates := model.Rates
	if rates == nil || rates.CostInput != 3 || rates.CostOutput != 15 || rates.CostCacheRead != 0.3 ||
		rates.CostCacheWrite != 3.75 || rates.CostCacheWrite1H != 6 || !rates.TiersDeclared || len(rates.Tiers) != 1 {
		t.Fatalf("OpenRouter base pricing did not parse: %+v", rates)
	}
	tier := rates.Tiers[0]
	if tier.ContextThreshold != 200000 || tier.CostInput != 6 || tier.CostOutput != 22.5 ||
		tier.CostCacheRead != 0.6 || tier.CostCacheWrite != 7.5 || tier.CostCacheWrite1H >= 0 {
		t.Fatalf("OpenRouter context tier did not parse: %+v", tier)
	}
	if model.Tools != catalog.SupportYes {
		t.Fatalf("supported tool parameter should report tool support: %v", model.Tools)
	}
}

func TestParseModelKeepsMalformedRatesUnknown(t *testing.T) {
	model, err := openrouter.ParseModel([]byte(`{"id":"vendor/model","supported_parameters":[],"pricing":{"prompt":"-1","completion":"1junk"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if model.Rates == nil || model.Rates.CostInput >= 0 || model.Rates.CostOutput >= 0 || model.Tools != catalog.SupportNo {
		t.Fatalf("malformed prices or absent tools should remain unknown/unsupported: %+v", model)
	}
}

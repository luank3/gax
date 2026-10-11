package deepseek

import (
	"reflect"
	"testing"

	"github.com/luank3/gax/internal/catalog"
)

func TestParseModel(t *testing.T) {
	model, err := ParseModel([]byte(`{"id":"deepseek-flash","name":"DeepSeek Flash","context_window":65536,"max_output_tokens":8192,"input_modalities":["text","image"],"effort":{"supported_levels":["low","high"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if model.Description != "DeepSeek Flash" || model.ContextWindow != 65536 || model.MaxOutput != 8192 ||
		model.ImageInput != catalog.SupportYes || !model.EffortsKnown ||
		!reflect.DeepEqual(model.Efforts, []string{"none", "low", "high"}) {
		t.Fatalf("parsed model = %+v", model)
	}
}

func TestParseModelKeepsMissingAndMalformedMetadataUnknown(t *testing.T) {
	model, err := ParseModel([]byte(`{"context_window":-1,"max_output_tokens":12.5,"input_modalities":null,"effort":{"supported_levels":"high"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 0 || model.MaxOutput != 0 || model.ImageInput != catalog.SupportUnknown || model.EffortsKnown {
		t.Fatalf("sparse model invented metadata: %+v", model)
	}
	if _, err := ParseModel([]byte(`[]`)); err == nil {
		t.Fatal("expected a non-object model to fail")
	}
}

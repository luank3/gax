package modelsort_test

import (
	"testing"

	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/modelsort"
)

func TestSortNewestFirstPerFamily(t *testing.T) {
	in := []provider.ModelEntry{
		{ID: "gpt-4.1"},
		{ID: "gpt-5.2"},
		{ID: "gpt-5.0"},
		{ID: "claude-sonnet-4-5"},
		{ID: "claude-sonnet-4-6"},
	}
	modelsort.Sort(in)
	if in[0].ID != "claude-sonnet-4-6" || in[1].ID != "claude-sonnet-4-5" {
		t.Fatalf("expected claude-sonnet-4-6 first, got %+v", in)
	}
	if in[2].ID != "gpt-5.2" || in[3].ID != "gpt-5.0" || in[4].ID != "gpt-4.1" {
		t.Fatalf("expected gpt-5.2 > 5.0 > 4.1, got %+v", in)
	}
}

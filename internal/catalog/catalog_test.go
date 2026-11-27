package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/luank3/gax/internal/catalog"
)

func TestCatalogRefreshAndOverrides(t *testing.T) {
	dir := t.TempDir()
	s := catalog.Load(dir)
	if !s.NeedsRefresh(24 * time.Hour) {
		t.Fatal("expected NeedsRefresh on empty cache")
	}

	err := s.Refresh(context.Background(), "https://models.dev/api.json", func(_ context.Context, _ string) ([]byte, error) {
		return []byte(`{
			"openai": {
				"models": {
					"gpt-5": {
						"reasoning": true,
						"modalities": {"input": ["text", "image"]},
						"cost": {"input": 1.25, "output": 10.0, "cache_read": 0.125},
						"limit": {"context": 400000, "output": 128000}
					}
				}
			}
		}`), nil
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if s.NeedsRefresh(24 * time.Hour) {
		t.Fatal("expected fresh cache after Refresh")
	}

	s.SetOverrides(map[string]any{
		"models": map[string]any{
			"codex": map[string]any{
				"gpt-5": map[string]any{
					"limit": map[string]any{"context": 872000},
				},
			},
		},
	})

	info, ok := s.Lookup("codex", "openai", "gpt-5")
	if !ok {
		t.Fatal("expected Lookup to find gpt-5")
	}
	if info.ContextLimit != 872000 || info.OutputLimit != 128000 || !info.ImageInput || !info.Reasoning {
		t.Fatalf("unexpected ModelInfo: %+v", info)
	}
}

// Package catalog caches and resolves models.dev metadata and local overrides
// for context limits, pricing, image input capability, and reasoning fields.
package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ModelInfo holds resolved metadata for one model.
type ModelInfo struct {
	API            string
	ContextLimit   int64
	OutputLimit    int64
	CostInput      float64
	CostOutput     float64
	CostCacheRead  float64
	ImageInput     bool
	Reasoning      bool
	ReasoningField string
}

type rawCatalog map[string]struct {
	Models map[string]rawModel `json:"models"`
}

type rawModel struct {
	Reasoning bool `json:"reasoning"`
	ToolCall  bool `json:"tool_call"`
	Provider  struct {
		NPM string `json:"npm"`
	} `json:"provider"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Cost struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		CacheRead float64 `json:"cache_read"`
	} `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Interleaved struct {
		Field string `json:"field"`
	} `json:"interleaved"`
}

// Store holds cached models.dev entries and config overrides.
type Store struct {
	cachePath string
	data      rawCatalog
	overrides map[string]map[string]rawModel
}

// Load reads the cached catalog from cacheDir/catalog.json if present.
func Load(cacheDir string) *Store {
	s := &Store{
		data:      make(rawCatalog),
		overrides: make(map[string]map[string]rawModel),
	}
	if cacheDir != "" {
		s.cachePath = filepath.Join(cacheDir, "catalog.json")
		if b, err := os.ReadFile(s.cachePath); err == nil {
			_ = json.Unmarshal(b, &s.data)
		}
	}
	return s
}

// SetOverrides installs catalog.models.<provider>.<model> overrides from config.
func (s *Store) SetOverrides(raw map[string]any) {
	if s == nil || raw == nil {
		return
	}
	modelsRaw, ok := raw["models"]
	if !ok {
		return
	}
	b, err := json.Marshal(modelsRaw)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.overrides)
}

// NeedsRefresh reports whether the cache file is missing or older than maxAge.
func (s *Store) NeedsRefresh(maxAge time.Duration) bool {
	if s == nil || s.cachePath == "" || maxAge <= 0 {
		return false
	}
	info, err := os.Stat(s.cachePath)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > maxAge
}

// Refresh fetches the catalog JSON via fetch and atomically updates the cache file.
func (s *Store) Refresh(ctx context.Context, url string, fetch func(context.Context, string) ([]byte, error)) error {
	if s == nil || url == "" || fetch == nil {
		return nil
	}
	body, err := fetch(ctx, url)
	if err != nil {
		return err
	}
	var parsed rawCatalog
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	s.data = parsed
	if s.cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(s.cachePath), 0o755)
		tmp := s.cachePath + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err == nil {
			_ = os.Rename(tmp, s.cachePath)
		}
	}
	return nil
}

// ListModels returns sorted model IDs known for catalogID.
func (s *Store) ListModels(catalogID string) []string {
	if s == nil || catalogID == "" {
		return nil
	}
	p, ok := s.data[catalogID]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(p.Models))
	for id := range p.Models {
		out = append(out, id)
	}
	return out
}

// Lookup resolves metadata for (runtimeProvider, catalogID, modelID), applying
// catalog.models overrides on top of cached models.dev entries.
func (s *Store) Lookup(runtimeProvider, catalogID, modelID string) (ModelInfo, bool) {
	if s == nil {
		return ModelInfo{}, false
	}
	var out ModelInfo
	found := false

	apply := func(m rawModel) {
		found = true
		if m.Limit.Context > 0 {
			out.ContextLimit = m.Limit.Context
		}
		if m.Limit.Output > 0 {
			out.OutputLimit = m.Limit.Output
		}
		if m.Cost.Input > 0 {
			out.CostInput = m.Cost.Input
		}
		if m.Cost.Output > 0 {
			out.CostOutput = m.Cost.Output
		}
		if m.Cost.CacheRead > 0 {
			out.CostCacheRead = m.Cost.CacheRead
		}
		if m.Reasoning {
			out.Reasoning = true
		}
		if m.Interleaved.Field != "" {
			out.ReasoningField = m.Interleaved.Field
		}
		switch m.Provider.NPM {
		case "@ai-sdk/anthropic":
			out.API = "anthropic-messages"
		case "@ai-sdk/openai":
			out.API = "openai-responses"
		case "@ai-sdk/openai-compatible":
			out.API = "openai-completions"
		}
		for _, mod := range m.Modalities.Input {
			if mod == "image" {
				out.ImageInput = true
			}
		}
	}

	if catalogID != "" {
		if p, ok := s.data[catalogID]; ok {
			if m, ok := p.Models[modelID]; ok {
				apply(m)
			}
		}
		if p, ok := s.overrides[catalogID]; ok {
			if m, ok := p[modelID]; ok {
				apply(m)
			}
		}
	}
	if runtimeProvider != "" && runtimeProvider != catalogID {
		if p, ok := s.overrides[runtimeProvider]; ok {
			if m, ok := p[modelID]; ok {
				apply(m)
			}
		}
	}
	return out, found
}

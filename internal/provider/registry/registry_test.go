package registry_test

import (
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/transport"
)

func TestAvailableProvidersHidesUnconfiguredCompatible(t *testing.T) {
	store := config.NewStore()
	avail := registry.AvailableProviders(store)
	hasZen := false
	for _, d := range avail {
		if d.ID == "openai-compatible" || d.ID == "anthropic-compatible" || d.ID == "mock" {
			t.Fatalf("did not expect %q in AvailableProviders", d.ID)
		}
		if d.ID == "opencode-zen" {
			hasZen = true
		}
	}
	if !hasZen {
		t.Fatal("expected opencode-zen in AvailableProviders")
	}

	t.Setenv("GAX_OPENAI_BASE_URL", "http://127.0.0.1:8000/v1")
	avail = registry.AvailableProviders(store)
	found := false
	for _, d := range avail {
		if d.ID == "openai-compatible" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected openai-compatible once base_url is set")
	}
}

func TestResolveMockAndLlamaCpp(t *testing.T) {
	store := config.NewStore()
	httpClient := transport.NewClient(transport.RetryPolicy{}, nil)

	m, err := registry.Resolve("mock", "", "", store, httpClient)
	if err != nil || m.ID() != "mock" {
		t.Fatalf("Resolve(mock): %v", err)
	}
	l, err := registry.Resolve("llamacpp", "qwen", "", store, httpClient)
	if err != nil || l.ID() != "llamacpp" {
		t.Fatalf("Resolve(llamacpp): %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")
	if got := registry.AutoSelect(store); got == "" {
		t.Fatal("expected AutoSelect to pick an available provider")
	}

	so := registry.ResolveStreamOptions("deepseek", "deepseek-chat", store, nil)
	if !so.ReasoningRequired {
		t.Fatal("expected deepseek to set ReasoningRequired=true")
	}
}

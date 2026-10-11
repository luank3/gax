package runtime_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

func TestPinnedProviderCannotBeRedirectedOrRewired(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return "secret-openai-key", true
		}
		return "", false
	})
	if err := store.LoadConfig([]byte(`{"providers":{"openai":{"base_url":"https://attacker.invalid/v1/","api":"anthropic-messages"}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := runtime.NewResolver(store, registry.New(store))
	resolved, err := providers.Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BaseURL != "https://api.openai.com/v1" || resolved.API != "openai-responses" || resolved.Wire != runtime.WireResponses {
		t.Fatalf("pinned provider accepted endpoint or API override: %+v", resolved)
	}
	if resolved.Endpoint != "https://api.openai.com/v1/responses" || !resolved.Availability.Ready() || !resolved.HasAPIKey() {
		t.Fatalf("pinned provider did not resolve its endpoint and credential: %+v", resolved)
	}
	if got := fmt.Sprintf("%+v %s", resolved, resolved); strings.Contains(got, "secret-openai-key") {
		t.Fatalf("provider formatting exposed a credential: %s", got)
	}
	if got := fmt.Sprintf("%+v %#v", *resolved, *resolved); strings.Contains(got, "secret-openai-key") {
		t.Fatalf("formatting a copied provider value exposed a credential: %s", got)
	}
	warnings := resolved.Warnings()
	wantWarnings := []string{
		"provider 'openai': api is pinned to openai-responses — use model_apis for per-model protocols, or a custom provider",
		"provider 'openai': base_url is pinned to https://api.openai.com/v1 — use a custom provider for another endpoint",
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("pinned endpoint and protocol warnings = %q, want %q", warnings, wantWarnings)
	}
}

func TestProviderConfigWarnsForUnusedAndUnknownFields(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"base_url":"https://demo.example/v1","api":"openai-responses","cache":"on","thinking_mode":"budget","port":8080,"typo":"x","metadata_api":"anthropic","version":"2024-02-01"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"provider 'demo': field 'cache' is not used by openai-responses providers",
		"provider 'demo': field 'thinking_mode' is not used by openai-responses providers",
		"provider 'demo': field 'port' is not configurable for this provider",
		"provider 'demo': unknown field 'typo' (see docs/providers.md)",
	}
	if got := provider.Warnings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider config warnings = %q, want %q", got, want)
	}
}

func TestProviderConfigLinterAllowsMixedAndRegisteredFields(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"mixed":{"base_url":"https://mixed.example/v1","model_apis":{"claude-*":"anthropic-messages"},"thinking_mode":"budget","reasoning_format":"nested"},"catgw":{"base_url":"https://catalog.example/v1","api":"catalog","catalog_id":"opencode","thinking_mode":"budget","reasoning_format":"nested"},"llamacpp":{"port":12345}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	for _, id := range []string{"mixed", "catgw", "llamacpp"} {
		provider, err := resolver.Resolve(id)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", id, err)
		}
		if warnings := provider.Warnings(); len(warnings) != 0 {
			t.Errorf("provider %q produced false-positive config warnings: %q", id, warnings)
		}
	}
}

func TestProviderWarningsFollowUpstreamMetadataLintAndRuleOrder(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"base_url":"https://demo.example/v1","api":"openai-completions","metadata_api":"invalid","model_apis":{"claude-*":false},"thinking_mode":"budget","port":8080}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"unknown metadata_api 'invalid' (openai or anthropic) — using default",
		"provider 'demo': field 'port' is not configurable for this provider",
		"providers.demo.model_apis: 'claude-*' needs a dialect value (openai-completions, openai-responses, anthropic-messages) — ignoring it",
		"provider 'demo': models route by catalog metadata, but catalog_id is not set",
	}
	if got := provider.Warnings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider warning order = %q, want %q", got, want)
	}
}

func TestCodexResolverLoadsProviderModelAndEffortDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"),
		[]byte("model = \"gpt-5.6\"\nmodel_reasoning_effort = \"high\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil || provider.DefaultModel != "gpt-5.6" || provider.DefaultEffort != "high" {
		t.Fatalf("Codex defaults = (%q, %q), err=%v", provider.DefaultModel, provider.DefaultEffort, err)
	}
}

func TestSortModelsPreferenceUsesGlobalOverrideAndProviderDefault(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   bool
	}{
		{name: "auto defaults to sorted", config: `{}`, want: true},
		{name: "provider can keep API order", config: `{"providers":{"openai":{"sort_models":"off"}}}`, want: false},
		{name: "global on overrides provider order", config: `{"sort_models":"on","providers":{"openai":{"sort_models":"off"}}}`, want: true},
		{name: "global off overrides provider default", config: `{"sort_models":"off"}`, want: false},
		{name: "auto uses provider sorted default", config: `{"sort_models":"auto","providers":{"openai":{"sort_models":"on"}}}`, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := config.New(config.DefaultSettings())
			store.SetEnvironment(nil)
			if err := store.LoadConfig([]byte(test.config)); err != nil {
				t.Fatal(err)
			}
			provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai")
			if err != nil {
				t.Fatal(err)
			}
			if got := provider.SortModels(); got != test.want {
				t.Fatalf("SortModels() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCustomProviderResolvesAPIKeyEnvAndTrimsEndpoint(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "GATEWAY_SECRET" {
			return "gateway-key", true
		}
		return "", false
	})
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1///","api":"CHAT","api_key_env":"GATEWAY_SECRET"}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := runtime.NewResolver(store, registry.New(store))
	resolved, err := providers.Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BaseURL != "https://gateway.example/v1" || resolved.Endpoint != "https://gateway.example/v1/chat/completions" {
		t.Fatalf("custom endpoint was not normalized: base=%q endpoint=%q", resolved.BaseURL, resolved.Endpoint)
	}
	if resolved.API != "CHAT" || resolved.Wire != runtime.WireChat || resolved.MetadataAPI != runtime.MetadataOpenAI {
		t.Fatalf("API alias or inferred metadata dialect is wrong: %+v", resolved)
	}
	if resolved.Credentials() != "gateway-key" || !resolved.Availability.Ready() || resolved.ExtraBody != nil {
		t.Fatalf("configured API key environment variable did not make provider ready: %+v", resolved)
	}
}

func TestLocalProviderPortAndProbeAvailability(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"llamacpp":{"port":9000}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := runtime.NewResolver(store, registry.New(store))
	llama, err := providers.Resolve("llama.cpp")
	if err != nil {
		t.Fatal(err)
	}
	if llama.BaseURL != "http://127.0.0.1:9000/v1" || llama.Availability.State != runtime.AvailabilityNeedsProbe ||
		llama.Availability.ProbeURL != "http://127.0.0.1:9000/v1/models" {
		t.Fatalf("llama.cpp port template did not resolve: %+v", llama)
	}
	ollama, err := providers.Resolve("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if ollama.BaseURL != "http://127.0.0.1:11434/v1" || ollama.Availability.State != runtime.AvailabilityNeedsProbe || ollama.Availability.ProbeURL != "http://127.0.0.1:11434/v1/models" {
		t.Fatalf("ollama defaults or probe requirement changed: %+v", ollama)
	}
}

func TestProbeAvailabilityChecksReachabilityRatherThanHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("probe path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("providers.llamacpp.base_url", stringPtrForRuntimeTest(server.URL))
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("llamacpp")
	if err != nil || provider.Availability.State != runtime.AvailabilityNeedsProbe {
		t.Fatalf("local provider did not require a reachability probe: provider=%+v err=%v", provider, err)
	}
	if !provider.ProbeAvailability(context.Background(), transport.NewClient()) || !provider.Availability.Ready() {
		t.Fatalf("reachable provider with an HTTP error status was considered down: %+v", provider.Availability)
	}
}

func TestLlamaModelDiscoveryReconcilesServerCatalog(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		configured string
		wantModel  string
		wantFound  bool
		wantWarn   string
	}{
		{
			name:      "first model for a regular server",
			body:      `{"data":[{"id":"model-a"},{"id":"model-b"}]}`,
			wantModel: "model-a", wantFound: true,
		},
		{
			name:       "canonicalizes configured router alias",
			body:       `{"data":[{"id":"model-a","aliases":["short-name"],"status":{"value":"loaded"}}]}`,
			configured: "short-name", wantModel: "model-a",
		},
		{
			name:      "unique running router model",
			body:      `{"data":[{"id":"model-a","status":{"value":"unloaded"}},{"id":"model-b","status":{"value":"loaded"}}]}`,
			wantModel: "model-b", wantFound: true,
		},
		{
			name: "router with no running model",
			body: `{"data":[{"id":"model-a","status":{"value":"unloaded"}},{"id":"model-b","status":{"value":"unloaded"}}]}`,
		},
		{
			name: "router with multiple running models",
			body: `{"data":[{"id":"model-a","status":{"value":"loaded"}},{"id":"model-b","status":{"value":"sleeping"}}]}`,
		},
		{
			name:       "fallback when configured model is not served",
			body:       `{"data":[{"id":"/models/served-model.gguf"}]}`,
			configured: "/models/missing-model.gguf", wantModel: "/models/served-model.gguf", wantFound: true,
			wantWarn: "model 'missing-model' is not served — using 'served-model'",
		},
		{
			name:       "router clears unmatched configured model",
			body:       `{"data":[{"id":"model-a","status":{"value":"loaded"}}]}`,
			configured: "missing", wantWarn: "not in the router catalog",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			provider := testLlamaProvider(t, server.URL)
			model, discovered, warning, err := provider.DiscoverModel(context.Background(), &transport.Client{HTTP: server.Client()}, test.configured)
			if err != nil || model != test.wantModel || discovered != test.wantFound || !strings.Contains(warning, test.wantWarn) {
				t.Fatalf("discovery = (%q, %t, %q), err=%v; want (%q, %t, containing %q)",
					model, discovered, warning, err, test.wantModel, test.wantFound, test.wantWarn)
			}
		})
	}
}

func TestLlamaModelDiscoveryPreservesConfiguredModelOnListFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	provider := testLlamaProvider(t, server.URL)
	model, discovered, warning, err := provider.DiscoverModel(context.Background(), &transport.Client{HTTP: server.Client()}, "configured-model")
	if err != nil || model != "configured-model" || discovered || warning != "" {
		t.Fatalf("failed discovery changed configured model: (%q, %t, %q), err=%v", model, discovered, warning, err)
	}
}

func TestLlamaModelDiscoveryRejectsMalformedListEvenWithConfiguredModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer server.Close()
	provider := testLlamaProvider(t, server.URL)
	_, _, _, err := provider.DiscoverModel(context.Background(), &transport.Client{HTTP: server.Client()}, "configured-model")
	if err == nil || !strings.Contains(err.Error(), "has no model list") {
		t.Fatalf("malformed successful response should fail discovery, err=%v", err)
	}
}

func TestLlamaModelDiscoveryDistinguishesEmptyAndMalformedCatalogs(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "no usable entries", body: `{"data":[{"id":7},{}]}`},
		{name: "null data is malformed", body: `{"data":null}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			provider := testLlamaProvider(t, server.URL)
			model, discovered, warning, err := provider.DiscoverModel(context.Background(), &transport.Client{HTTP: server.Client()}, "")
			if test.wantError {
				if err == nil {
					t.Fatalf("malformed catalog succeeded: model=%q discovered=%t warning=%q", model, discovered, warning)
				}
				return
			}
			if err != nil || model != "" || discovered || !strings.Contains(warning, "no models available") {
				t.Fatalf("empty catalog discovery = (%q, %t, %q), err=%v", model, discovered, warning, err)
			}
		})
	}
}

func TestListModelsParsesLlamaCapabilitiesAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a","aliases":["friendly-name"],"meta":{"n_ctx":32768},"architecture":{"input_modalities":["text","image"]},"status":{"value":"failed","failed":true,"exit_code":2}}]}`)
	}))
	defer server.Close()
	provider := testLlamaProvider(t, server.URL)
	models, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
	if err != nil || len(models) != 1 {
		t.Fatalf("llama model listing = %+v, err=%v", models, err)
	}
	model := models[0]
	if model.ID != "model-a" || !reflect.DeepEqual(model.Aliases, []string{"friendly-name"}) ||
		!model.StatusObject || model.Status != "failed" || model.ContextWindow != 32768 ||
		model.ImageInput != catalog.SupportYes || model.Description != "failed (exit 2)" {
		t.Fatalf("llama model details were not retained: %+v", model)
	}
}

func TestListedModelIDsKeepProviderOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"z-model"}]}`)
	}))
	defer server.Close()
	provider := testLlamaProvider(t, server.URL)
	if _, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil); err != nil {
		t.Fatal(err)
	}
	if got := provider.ListedModelIDs(); !reflect.DeepEqual(got, []string{"z-model", "a-model"}) {
		t.Fatalf("listed model IDs = %v; want unique provider order", got)
	}
}

func TestOpenAICompatibleListsModelsWithoutBackgroundProbe(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/models" || request.URL.RawQuery != "" {
			t.Errorf("model list request = %s %s", request.Method, request.URL)
		}
		requests.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"selected-model"},{"id":"available-model"}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openai-compatible":{"base_url":"` + server.URL + `","api_key":"test-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	client := &transport.Client{HTTP: server.Client()}
	if provider.CanProbeSelectedModel() || provider.StartModelProbe(context.Background(), "selected-model", client) {
		t.Fatal("OpenAI-compatible provider unexpectedly exposed a model metadata probe")
	}
	if requests.Load() != 0 {
		t.Fatalf("model metadata probe issued %d requests before listing", requests.Load())
	}
	models, err := provider.ListModels(context.Background(), client, nil)
	if err != nil || len(models) != 2 || models[0].ID != "selected-model" || models[1].ID != "available-model" {
		t.Fatalf("explicit OpenAI-compatible model list = %+v, err=%v", models, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("explicit model list made %d requests, want 1", requests.Load())
	}
	if got := provider.ListedModelIDs(); !reflect.DeepEqual(got, []string{"selected-model", "available-model"}) {
		t.Fatalf("explicit model list IDs = %v", got)
	}
}

func TestModelProbeDoesNotListModelsWhenNoModelIsSelected(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-test","max_input_tokens":200000}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"` + server.URL + `","api":"anthropic-messages","api_key":"test-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	client := &transport.Client{HTTP: server.Client()}
	if provider.StartModelProbe(context.Background(), "", client) {
		t.Fatal("listing-only background model probe started without a selected model")
	}
	if _, found := provider.ProbeModel(context.Background(), "", client, nil); found {
		t.Fatal("listing-only model probe returned metadata without a selected model")
	}
	if requests.Load() != 0 {
		t.Fatalf("model probe issued %d listing requests without a selected model", requests.Load())
	}
}

func TestStartModelProbeJoinsPreviousTargetBeforeStartingReplacement(t *testing.T) {
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var requests atomic.Int32
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			close(firstStarted)
			<-request.Context().Done()
			close(firstCanceled)
			<-releaseFirst
			return nil, request.Context().Err()
		case 2:
			close(secondStarted)
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body:    io.NopCloser(strings.NewReader(`{"data":[{"id":"new-model","context_length":128000}]}`)),
				Request: request,
			}, nil
		default:
			t.Errorf("unexpected model probe request %d", requests.Load())
			return nil, fmt.Errorf("unexpected model probe request")
		}
	})}}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "test-key", name == "OPENROUTER_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	release := func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}
	defer release()

	if !provider.StartModelProbe(context.Background(), "old-model", client) {
		t.Fatal("initial model probe did not start")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("initial model probe did not reach the transport")
	}
	startedReplacement := make(chan bool, 1)
	go func() { startedReplacement <- provider.StartModelProbe(context.Background(), "new-model", client) }()
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("replacing the target did not cancel the initial probe")
	}
	select {
	case <-secondStarted:
		t.Fatal("replacement probe started before the canceled probe exited")
	case <-time.After(25 * time.Millisecond):
	}

	release()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement probe did not start after the first probe exited")
	}
	select {
	case started := <-startedReplacement:
		if !started {
			t.Fatal("replacement model probe was not started")
		}
	case <-time.After(time.Second):
		t.Fatal("replacement probe start did not finish")
	}
	waitForModelMetadata(t, provider, "new-model")
}

func waitForModelMetadata(t *testing.T, provider *runtime.Provider, model string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, found := provider.ModelMetadata(model); found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("background probe did not retain metadata for %q", model)
}

func testOpenAICompatibleProvider(t *testing.T, baseURL string) *runtime.Provider {
	t.Helper()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openai-compatible":{"base_url":"` + baseURL + `","api_key":"test-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func testLlamaProvider(t *testing.T, baseURL string) *runtime.Provider {
	t.Helper()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("providers.llamacpp.base_url", stringPtrForRuntimeTest(baseURL))
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func stringPtrForRuntimeTest(value string) *string { return &value }

func TestNilStoreUsesProviderPortDefaults(t *testing.T) {
	providers := runtime.NewResolver(nil, nil)
	llama, err := providers.Resolve("llama.cpp")
	if err != nil {
		t.Fatal(err)
	}
	if llama.BaseURL != "http://127.0.0.1:8080/v1" || llama.Endpoint != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Fatalf("llama.cpp default port was not expanded without a store: %+v", llama)
	}
	ollama, err := providers.Resolve("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if ollama.BaseURL != "http://127.0.0.1:11434/v1" || ollama.Availability.ProbeURL != "http://127.0.0.1:11434/v1/models" {
		t.Fatalf("ollama default port was not expanded without a store: %+v", ollama)
	}
}

func TestMissingConfigurationAndCredentialsHaveStableReasons(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	providers := runtime.NewResolver(store, registry.New(store))
	compatible, err := providers.Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if compatible.Availability.State != runtime.AvailabilityUnconfigured || compatible.Availability.Reason != "GAX_ANTHROPIC_BASE_URL not set" {
		t.Fatalf("missing compatible endpoint reason changed: %+v", compatible.Availability)
	}
	openai, err := providers.Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	if openai.Availability.State != runtime.AvailabilityMissingCredential || openai.Availability.Reason != "OPENAI_API_KEY not set" {
		t.Fatalf("missing key reason changed: %+v", openai.Availability)
	}
	deepseek, err := providers.Resolve("deepseek")
	if err != nil || deepseek.Availability.State != runtime.AvailabilityMissingCredential || deepseek.Availability.Reason != "DEEPSEEK_API_KEY not set" {
		t.Fatalf("missing DeepSeek key reason changed: provider=%+v err=%v", deepseek, err)
	}
	mock, err := providers.Resolve("mock")
	if err != nil || !mock.Availability.Ready() || mock.DefaultModel != "mock-model" || mock.MockProvider() == nil {
		t.Fatalf("internal constructor provider should not require an HTTP endpoint: provider=%+v err=%v", mock, err)
	}
}

func TestAgnesModelListingDoesNotInventModelMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer agnes-list-key" {
			t.Errorf("unexpected Agnes model list request: method=%s path=%s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"agnes-2.5-flash"},{"id":"agnes-3.0-pro"}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "AGNES_API_KEY" {
			return "agnes-list-key", true
		}
		return "", false
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("agnes")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = server.URL
	models, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("Agnes model listing = %+v", models)
	}
	model := models[0]
	if model.ID != "agnes-2.5-flash" || model.ContextWindow != 0 || model.MaxOutput != 0 ||
		model.ImageInput != catalog.SupportUnknown || model.Tools != catalog.SupportUnknown {
		t.Fatalf("Agnes listing should retain IDs without invented capabilities: %+v", model)
	}
	metadata, found := provider.ModelMetadata(model.ID)
	if !found || metadata.CostInput != -1 || metadata.CostCacheRead != -1 || metadata.CostOutput != -1 {
		t.Fatalf("Agnes must not invent pricing metadata for a listed model: found=%t metadata=%+v", found, metadata)
	}
	if provider.CanProbeSelectedModel() {
		t.Fatal("Agnes should not run a custom selected-model probe")
	}
}

func TestDeepseekListsProviderSpecificModelMetadata(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" ||
			r.Header.Get("Authorization") != "Bearer deepseek-test-key" {
			t.Errorf("unexpected DeepSeek model request: method=%s path=%s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		requests.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4","name":"DeepSeek V4","context_window":262144,"max_output_tokens":65536,"input_modalities":["text","image"],"effort":{"supported_levels":["low","high"]}}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "deepseek-test-key", name == "DEEPSEEK_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://api.deepseek.com" || provider.API != "openai-completions" ||
		provider.Wire != runtime.WireChat || !provider.Availability.Ready() {
		t.Fatalf("DeepSeek request configuration = %+v", provider)
	}
	provider.BaseURL = server.URL
	models, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "deepseek-v4" || models[0].Description != "DeepSeek V4" ||
		models[0].ContextWindow != 262144 || models[0].MaxOutput != 65536 || requests.Load() != 1 {
		t.Fatalf("DeepSeek model catalog = %+v requests=%d", models, requests.Load())
	}
	metadata, found := provider.ModelMetadata("deepseek-v4")
	if !found || metadata.ImageInput != catalog.SupportYes || !metadata.EffortsKnown ||
		!reflect.DeepEqual(metadata.Efforts, []string{"none", "low", "high"}) || models[0].Tools != catalog.SupportUnknown {
		t.Fatalf("DeepSeek listing metadata = %+v found=%t", metadata, found)
	}
}

func TestOpenRouterModelProbeQueriesCatalogAndStoresMetadata(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "openrouter-probe-key", name == "OPENROUTER_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	var requestedURL string
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		if request.URL.Host != "openrouter.ai" || request.URL.Path != "/api/v1/models" ||
			request.URL.Query().Get("q") != "vendor/model" || request.Header.Get("Authorization") != "Bearer openrouter-probe-key" {
			t.Errorf("OpenRouter probe request mismatch: url=%q headers=%v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`{"data":[{"id":"vendor/model","context_length":200000,"max_output_tokens":64000,"supported_parameters":["tools"],"architecture":{"input_modalities":["text","image"]},"reasoning":{"supported_efforts":["low","high"]},"pricing":{"prompt":"0.000002","completion":"0.00001"}}]}`)),
			Request: request,
		}, nil
	})}}
	model, found := provider.ProbeModel(context.Background(), "vendor/model", client, nil)
	if !found || model.ID != "vendor/model" || model.ContextWindow != 200000 || model.Tools != catalog.SupportYes {
		t.Fatalf("OpenRouter probe result = %+v found=%t", model, found)
	}
	if requestedURL != "https://openrouter.ai/api/v1/models?q=vendor%2Fmodel" {
		t.Fatalf("OpenRouter probe URL = %q", requestedURL)
	}
	if got := provider.ListedModelIDs(); len(got) != 0 {
		t.Fatalf("OpenRouter metadata probe changed the listed-model cache: %v", got)
	}
	metadata, ok := provider.ModelMetadata(model.ID)
	if !ok || metadata.CostInput != 2 || metadata.CostOutput != 10 ||
		!reflect.DeepEqual(provider.EffortLevels(model.ID), []string{"low", "high"}) {
		t.Fatalf("probed model metadata was not retained: found=%t entry=%+v efforts=%v", ok, metadata, provider.EffortLevels(model.ID))
	}
}

func TestOpenRouterModelProbeEscapesSelectedModelLikeUpstream(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "openrouter-probe-key", name == "OPENROUTER_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	const modelID = "vendor/x y+?~"
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.RawQuery != "q=vendor%2Fx%20y%2B%3F~" {
			t.Errorf("OpenRouter probe query = %q", request.URL.RawQuery)
		}
		body, _ := json.Marshal(map[string]any{"data": []any{map[string]any{
			"id": modelID, "context_length": 1024, "max_output_tokens": 256,
		}}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}}
	if _, found := provider.ProbeModel(context.Background(), modelID, client, nil); !found {
		t.Fatal("OpenRouter did not return the selected model")
	}
}

func TestOpenRouterModelProbeStartsInBackgroundAndWaitsForMetadata(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		return "openrouter-probe-key", name == "OPENROUTER_API_KEY"
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`{"data":[{"id":"vendor/model","context_length":120000,"supported_parameters":["tools"]}]}`)),
			Request: request,
		}, nil
	})}}
	startResult := make(chan bool, 1)
	go func() { startResult <- provider.StartModelProbe(context.Background(), "vendor/model", client) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("background probe did not start")
	}
	select {
	case startedProbe := <-startResult:
		if !startedProbe {
			close(release)
			t.Fatal("expected model probe to start")
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("starting model probe blocked on the network request")
	}
	close(release)
	if !provider.WaitModelMetadata(time.Second, nil) {
		t.Fatal("metadata wait did not complete")
	}
	metadata, found := provider.ModelMetadata("vendor/model")
	if !found || metadata.ContextWindow != 120000 {
		t.Fatalf("background probe metadata = %+v found=%t", metadata, found)
	}
}

func TestAnthropicModelProbeUsesAnthropicModelEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" || request.URL.Query().Get("limit") != "1000" ||
			request.Header.Get("x-api-key") != "anthropic-key" || request.Header.Get("anthropic-version") == "" {
			t.Errorf("Anthropic model probe request = %s headers=%v", request.URL, request.Header)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-test","max_input_tokens":200000,"max_tokens":8192,"capabilities":{"image_input":{"supported":true},"effort":{"supported":true,"low":{"supported":true},"high":{"supported":true}}}},{"id":"claude-haiku-test"}]}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"` + server.URL + `","api":"anthropic-messages","api_key":"anthropic-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	model, found := provider.ProbeModel(context.Background(), "claude-sonnet-test", transport.NewClient(), nil)
	if !found || model.ContextWindow != 200000 || model.MaxOutput != 8192 || model.ImageInput != catalog.SupportYes ||
		!reflect.DeepEqual(model.Efforts, []string{"low", "high"}) {
		t.Fatalf("Anthropic model probe result = %+v found=%t", model, found)
	}
	if got := provider.ListedModelIDs(); len(got) != 0 {
		t.Fatalf("Anthropic metadata probe changed the listed-model cache: %v", got)
	}
}

func TestCodexModelProbeUsesAuthenticatedVersionedCatalog(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"access-secret","account_id":"account-123"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://chatgpt.com/backend-api/codex/models?client_version=999.0.0" ||
			request.Header.Get("Authorization") != "Bearer access-secret" || request.Header.Get("chatgpt-account-id") != "account-123" {
			t.Errorf("Codex model probe request = %s headers=%v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"models":[{"slug":"gpt-test","context_window":128000,"max_context_window":256000,"input_modalities":["text","image"],"description":"Codex model","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"slug":"hidden-test","visibility":"hide"}]}`)), Request: request}, nil
	})}}
	model, found := provider.ProbeModel(context.Background(), "gpt-test", client, nil)
	if !found || model.ContextWindow != 128000 || model.MaxContext != 256000 || model.ImageInput != catalog.SupportYes ||
		model.Description != "Codex model" || !reflect.DeepEqual(model.Efforts, []string{"none", "low", "high"}) {
		t.Fatalf("Codex model probe result = %+v found=%t", model, found)
	}
	if got := provider.ListedModelIDs(); len(got) != 0 {
		t.Fatalf("Codex metadata probe changed the listed-model cache: %v", got)
	}
}

func TestCodexResolverLoadsManagedCredentialsWithoutHome(t *testing.T) {
	stateHome := t.TempDir()
	storePath := filepath.Join(stateHome, "gax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte(`{"codex":{"access_token":"access-secret","refresh_token":"refresh-secret","account_id":"account-123"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "XDG_STATE_HOME" {
			return stateHome, true
		}
		return "", false
	})

	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil || !provider.Availability.Ready() || !provider.HasAPIKey() {
		t.Fatalf("configured gax credentials should load without HOME: provider=%+v err=%v", provider, err)
	}
}

func TestCodexModelProbeSkipsTokensNearExpiry(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]int64{"exp": time.Now().Add(30 * time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"`+token+`","account_id":"account-123"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"models":[{"slug":"gpt-test","context_window":128000}]}`)), Request: request}, nil
	})}}
	if _, found := provider.ProbeModel(context.Background(), "gpt-test", client, nil); found || requests != 0 {
		t.Fatalf("near-expiry Codex token should defer metadata probe: found=%t requests=%d", found, requests)
	}
}

func TestCodexListModelsParsesItsVersionedModelsEnvelope(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"access-secret","account_id":"account-123"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	client := &transport.Client{HTTP: &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://chatgpt.com/backend-api/codex/models?client_version=999.0.0" ||
			request.Header.Get("Authorization") != "Bearer access-secret" || request.Header.Get("chatgpt-account-id") != "account-123" {
			t.Errorf("Codex model list request = %s headers=%v", request.URL, request.Header)
		}
		body := `{"models":[{"slug":"gpt-test","context_window":128000,"max_context_window":256000,"input_modalities":["text","image"],"description":"Codex model","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"slug":"hidden-model","visibility":"hide"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}}
	models, err := provider.ListModels(context.Background(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-test" || models[0].ContextWindow != 128000 || models[0].MaxContext != 256000 ||
		models[0].ImageInput != catalog.SupportYes || models[0].Description != "Codex model" ||
		!reflect.DeepEqual(models[0].Efforts, []string{"none", "low", "high"}) {
		t.Fatalf("Codex model list = %+v", models)
	}
}

func TestLlamaModelProbeReadsLoadedServerProperties(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/props" || request.URL.Query().Get("model") != "org/model name" {
			t.Errorf("llama.cpp model probe request = %s", request.URL)
		}
		_, _ = io.WriteString(w, `{"default_generation_settings":{"n_ctx":65536},"modalities":{"vision":true}}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("providers.llamacpp.base_url", stringPtrForRuntimeTest(server.URL+"/v1"))
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	model, found := provider.ProbeModel(context.Background(), "org/model name", transport.NewClient(), nil)
	if !found || model.ContextWindow != 65536 || model.ImageInput != catalog.SupportYes {
		t.Fatalf("llama.cpp model probe result = %+v found=%t", model, found)
	}
}

type runtimeRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip runtimeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestListModelsAcceptsNullAndEmptyListsAndRejectsMalformedShape(t *testing.T) {
	for _, test := range []struct {
		name, body, wantError string
	}{
		{name: "null", body: `{"data":null}`},
		{name: "empty", body: `{"data":[]}`},
		{name: "missing data", body: `{"models":[]}`, wantError: "/models response has no model list"},
		{name: "no usable ids", body: `{"data":[{"name":"missing id"}]}`, wantError: "/models response contains no usable model ids"},
		{name: "invalid json", body: `{"data":[`, wantError: "/models response is not valid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.body) }))
			defer server.Close()
			provider := testOpenAICompatibleProvider(t, server.URL)
			models, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
			if test.wantError == "" {
				if err != nil || len(models) != 0 {
					t.Fatalf("empty model response: models=%+v err=%v", models, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("listing error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestListModelsFormatsHTTPFailureWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	provider := testOpenAICompatibleProvider(t, server.URL)
	_, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("model list auth error should be useful and redact the key: %v", err)
	}
}

func TestAnthropicListModelsFollowsCursorAndParsesCapabilities(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if r.URL.Query().Get("limit") != "1000" || r.Header.Get("x-api-key") != "secret-key" ||
			r.Header.Get("anthropic-version") == "" {
			t.Errorf("Anthropic model-list request missing pagination or auth headers: query=%q headers=%v", r.URL.RawQuery, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "claude-opus-4" {
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4","max_input_tokens":200000,"max_tokens":64000}],"has_more":false,"last_id":"claude-sonnet-4"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-4","max_input_tokens":200000,"max_tokens":32000,"capabilities":{"image_input":{"supported":true},"effort":{"supported":true,"low":{"supported":true},"high":{"supported":true}}}}],"has_more":true,"last_id":"claude-opus-4"}`)
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"` + server.URL + `","api_key":"secret-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil || provider.MetadataAPI != runtime.MetadataAnthropic {
		t.Fatalf("Anthropic provider did not resolve: provider=%+v err=%v", provider, err)
	}
	models, err := provider.ListModels(context.Background(), &transport.Client{HTTP: server.Client()}, nil)
	if err != nil || len(models) != 2 || len(requests) != 2 {
		t.Fatalf("Anthropic pages = %+v, requests=%v, err=%v", models, requests, err)
	}
	if models[0].ID != "claude-opus-4" || models[0].ContextWindow != 200000 || models[0].MaxOutput != 32000 ||
		models[0].ImageInput != catalog.SupportYes || !models[0].EffortsKnown || !reflect.DeepEqual(models[0].Efforts, []string{"low", "high"}) {
		t.Fatalf("Anthropic model capabilities were not parsed: %+v", models[0])
	}
	if got := provider.EffortLevels("claude-opus-4"); !reflect.DeepEqual(got, []string{"low", "high"}) {
		t.Fatalf("listed Anthropic efforts were not retained for selection: %v", got)
	}
	if !strings.Contains(requests[0], "limit=1000") || requests[1] != "limit=1000&after_id=claude-opus-4" {
		t.Fatalf("Anthropic cursors = %v", requests)
	}
}

func TestModelAPIRulesRouteByFirstMatchingGlob(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"openai-completions","model_apis":{"acme/*":"anthropic-messages","acme/gpt-4":"openai-responses","legacy/*":"responses","numeric/v[0-9]":"openai-responses","bad/*":"unknown"}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.WireForModel("acme/gpt-4"); got != runtime.WireAnthropic {
		t.Fatalf("first matching model_apis rule should win, got %q", got)
	}
	if got := provider.EndpointForModel("acme/gpt-4"); got != "https://gateway.example/v1/messages" {
		t.Fatalf("model endpoint should follow its routed wire, got %q", got)
	}
	if got := provider.WireForModel("legacy/model-x"); got != runtime.WireResponses {
		t.Fatalf("short responses dialect alias was not accepted, got %q", got)
	}
	if got := provider.WireForModel("numeric/v7"); got != runtime.WireResponses {
		t.Fatalf("bracket character class was not matched, got %q", got)
	}
	if got := provider.WireForModel("unmatched/model"); got != runtime.WireChat {
		t.Fatalf("unmatched model should retain provider default wire, got %q", got)
	}
	if len(provider.Warnings()) != 1 || !strings.Contains(provider.Warnings()[0], `bad/*`) {
		t.Fatalf("invalid dialect rule should be warned and ignored: %q", provider.Warnings())
	}
}

func TestMalformedModelAPIRulesWarnAndLeaveDefaultWire(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"responses","model_apis":[]}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	wantWarnings := []string{
		"providers.gateway.model_apis must be a JSON object of pattern/dialect members — ignoring it",
	}
	if provider.WireForModel("any/model") != runtime.WireResponses || !reflect.DeepEqual(provider.Warnings(), wantWarnings) {
		t.Fatalf("malformed model_apis should be ignored with a warning: wire=%q warnings=%q", provider.WireForModel("any/model"), provider.Warnings())
	}
}

func TestModelAPIRulesCanRoutePinnedProvidersAndGlobAcrossSlashes(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openai":{"model_apis":{"vendor/*":"responses"}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openai")
	if err != nil {
		t.Fatal(err)
	}
	if provider.API != "openai-responses" || provider.Wire != runtime.WireResponses || provider.WireForModel("vendor/nested/gpt-5") != runtime.WireResponses {
		t.Fatalf("pinned provider model rule should override its per-model wire: %+v", provider)
	}
}

func TestCatalogAPIHintsRouteOnlyAfterExplicitRules(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"catalog","catalog_id":"vendor","model_apis":{"rule/*":"anthropic-messages"}}},"catalog":{"models":{"gateway":{"configured/model":{"api":"openai-responses"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	modelCatalog := catalog.New(store)
	if err := modelCatalog.LoadSnapshot([]byte(`{"vendor":{"npm":"@ai-sdk/openai-compatible","models":{"rule/model":{"provider":{"npm":"@ai-sdk/openai"}},"vendor/model":{"provider":{"npm":"@ai-sdk/anthropic"}},"configured/model":{"provider":{"npm":"@ai-sdk/anthropic"}},"unsupported/model":{"provider":{"npm":"@other/sdk"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	resolver.SetCatalog(modelCatalog)
	provider, err := resolver.Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]runtime.Wire{
		"rule/model":       runtime.WireAnthropic,
		"vendor/model":     runtime.WireAnthropic,
		"configured/model": runtime.WireResponses,
		"unlisted/model":   runtime.WireChat,
	} {
		if got := provider.WireForModel(model); got != want {
			t.Errorf("wire for %q: got %q want %q", model, got, want)
		}
	}
	if got := provider.WireForModel("unsupported/model"); got != "" {
		t.Fatalf("unsupported SDK selectors must not silently fall back: %q", got)
	}
	if got := provider.EndpointForModel("configured/model"); got != "https://gateway.example/v1/responses" {
		t.Fatalf("catalog-config wire did not select its endpoint: %q", got)
	}
}

func TestCatalogRoutingWarnsOnlyWhenNoCatalogIdentityOrConfiguredAPIsExist(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"missing":{"base_url":"https://missing.example/v1","api":"catalog"},"configured":{"base_url":"https://configured.example/v1","api":"catalog"}},"catalog":{"models":{"configured":{"m":{"api":"openai-responses"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	missing, err := resolver.Resolve("missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Warnings()) != 1 || !strings.Contains(missing.Warnings()[0], "catalog_id is not set") {
		t.Fatalf("unidentifiable catalog routing should warn: %q", missing.Warnings())
	}
	configured, err := resolver.Resolve("configured")
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Warnings()) != 0 {
		t.Fatalf("configured per-model API hints make catalog_id unnecessary: %q", configured.Warnings())
	}
}

func TestEffortLevelsUsesProviderLadderAndModelRestrictions(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"chat"},"messages":{"base_url":"https://messages.example/v1","api":"anthropic-messages"}},"catalog":{"models":{"gateway":{"m":{"reasoning_options":[{"type":"effort","values":["low","high"]}]},"custom":{"reasoning_options":[{"type":"effort","values":["low","turbo"]}]},"empty":{"reasoning":false}},"messages":{"m":{"reasoning_options":[{"type":"effort","values":["none","minimal","low"]}]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := provider.EffortLevels("m"), []string{"low", "high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model effort levels = %v, want %v", got, want)
	}
	if got := provider.EffortLevels("empty"); len(got) != 0 {
		t.Fatalf("explicitly empty model effort list = %v, want none", got)
	}
	if got, want := provider.EffortLevels("custom"), []string{"low", "turbo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit model effort vocabulary = %v, want %v", got, want)
	}
	if got, want := provider.EffortLevels("unlisted"), []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unlisted model effort levels = %v, want provider ladder %v", got, want)
	}

	local, err := resolver.Resolve("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	if got := local.EffortLevels("m"); len(got) != 0 {
		t.Fatalf("provider without categorical efforts exposed %v", got)
	}
	messages, err := resolver.Resolve("messages")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := messages.EffortLevels("m"), []string{"none", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Anthropic message effort levels = %v, want %v", got, want)
	}
}

func TestEffortLevelsRespectPinnedAnthropicThinkingModes(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"messages":{"base_url":"https://messages.example/v1","api":"anthropic-messages","thinking_mode":"off"},"budget":{"base_url":"https://budget.example/v1","api":"anthropic-messages","thinking_mode":"budget"},"adaptive":{"base_url":"https://adaptive.example/v1","api":"anthropic-messages","thinking_mode":"adaptive"},"mixed":{"base_url":"https://mixed.example/v1","api":"anthropic-messages","thinking_mode":"off","model_apis":{"chat/*":"openai-responses"}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	for _, id := range []string{"messages", "budget"} {
		provider, err := resolver.Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := provider.EffortLevels("model"); len(got) != 0 {
			t.Errorf("%s thinking mode exposed effort levels %v, want none", id, got)
		}
		none := "none"
		if got := provider.ResolveEffort("model", &none); got != nil {
			t.Errorf("%s thinking mode resolved unsupported effort %q, want no effort", id, *got)
		}
	}
	adaptive, err := resolver.Resolve("adaptive")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := adaptive.EffortLevels("model"), []string{"none", "low", "medium", "high", "xhigh", "max"}; !reflect.DeepEqual(got, want) {
		t.Errorf("adaptive thinking effort levels = %v, want %v", got, want)
	}
	mixed, err := resolver.Resolve("mixed")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mixed.EffortLevels("chat/model"), []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mixed-wire provider effort levels = %v, want %v", got, want)
	}
}

func TestResolveEffortClampsUnsupportedRequestsToNearestSupportedLevel(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"openai-completions"}},"catalog":{"models":{"gateway":{"m":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	requested := "max"
	if got := provider.ResolveEffort("m", &requested); got == nil || *got != "high" {
		t.Fatalf("unsupported max effort resolved to %v, want high", got)
	}
	provider.DefaultEffort = "high"
	custom := "turbo"
	if got := provider.ResolveEffort("m", &custom); got == nil || *got != "high" {
		t.Fatalf("unknown effort with provider default resolved to %v, want high", got)
	}
	empty := ""
	if got := provider.ResolveEffort("m", &empty); got != nil {
		t.Fatalf("explicit empty effort resolved to %q, want omission", *got)
	}
}

func TestRequestSettingsResolveChatCacheReasoningAndUsageDefaults(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_format":"invalid"}},"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"cache_write":1,"cache_write_1h":3},"interleaved":{"field":"reasoning"}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "session-1"
	settings := provider.RequestSettings("m", nil, &sessionID)
	if settings.ChatCacheMarkers || settings.CacheWriteIsOneHour {
		t.Fatalf("auto cache should skip a write priced below input: %+v", settings)
	}
	if settings.SessionCacheKey == nil || *settings.SessionCacheKey != sessionID || !settings.RequestCost {
		t.Fatalf("provider cache-key and usage defaults were not applied: %+v", settings)
	}
	if settings.ReasoningField == nil || *settings.ReasoningField != "reasoning" || settings.ReasoningFormat != "nested" {
		t.Fatalf("catalog reasoning field and definition format were not applied: %+v", settings)
	}

	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"cache":"on"}},"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"cache_write":1,"cache_write_1h":3}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("m", nil, &sessionID)
	if !settings.ChatCacheMarkers || !settings.CacheWriteIsOneHour {
		t.Fatalf("explicit cache on and 1h pricing metadata should enable markers and usage classification: %+v", settings)
	}
}

func TestRequestReadsModelMetadataOnlyWhenWirePolicyConsumesIt(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"responses":{"base_url":"https://responses.example/v1","api":"openai-responses"},"chat":{"base_url":"https://chat.example/v1","api":"openai-completions"},"routed":{"base_url":"https://routed.example/v1","api":"openai-responses","model_apis":{"vendor/*":"openai-completions"}},"catalog":{"base_url":"https://catalog.example/v1","api":"catalog","catalog_id":"vendor"}}}`)); err != nil {
		t.Fatal(err)
	}
	resolver := runtime.NewResolver(store, registry.New(store))
	for _, test := range []struct {
		id   string
		want bool
	}{
		{id: "openai", want: false},
		{id: "responses", want: false},
		{id: "chat", want: true},
		{id: "anthropic", want: true},
		{id: "routed", want: true},
		{id: "catalog", want: true},
	} {
		provider, err := resolver.Resolve(test.id)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", test.id, err)
		}
		if got := provider.RequestReadsModelMetadata(); got != test.want {
			t.Errorf("%s RequestReadsModelMetadata() = %v, want %v", test.id, got, test.want)
		}
	}
}

func TestRequestSettingsResolveReasoningRoundtripPolicy(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings := provider.RequestSettings("unlisted", nil, nil)
	if settings.ReasoningField != nil {
		t.Fatalf("provider with no configured or catalog field should not replay reasoning: %+v", settings)
	}

	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_roundtrip":"off"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("unlisted", nil, nil)
	if settings.ReasoningField != nil {
		t.Fatalf("pinned off should leave reasoning replay disabled: %+v", settings)
	}

	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_roundtrip":"auto"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("unlisted", nil, nil)
	if settings.ReasoningField != nil {
		t.Fatalf("auto without a catalog field should leave reasoning replay disabled: %+v", settings)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_roundtrip":"on"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("unlisted", nil, nil)
	if settings.ReasoningField == nil || *settings.ReasoningField != "reasoning_content" {
		t.Fatalf("on should pin the reasoning_content member: %+v", settings)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_roundtrip":"1"}} ,"catalog":{"models":{"openrouter":{"hinted":{"interleaved":{"field":"reasoning"}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("hinted", nil, nil)
	if settings.ReasoningField == nil || *settings.ReasoningField != "reasoning_content" {
		t.Fatalf("numeric on should pin reasoning_content ahead of catalog: %+v", settings)
	}

	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"reasoning_roundtrip":"auto"}},"catalog":{"models":{"openrouter":{"hinted":{"interleaved":{"field":"reasoning"}},"disabled":{"interleaved":false}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	hinted := provider.RequestSettings("hinted", nil, nil)
	if hinted.ReasoningField == nil || *hinted.ReasoningField != "reasoning" {
		t.Fatalf("catalog reasoning field should enable replay: %+v", hinted)
	}
	disabled := provider.RequestSettings("disabled", nil, nil)
	if disabled.ReasoningField != nil {
		t.Fatalf("catalog-declared disabled replay should suppress configured fallback: %+v", disabled)
	}
}

func TestDeepSeekRequiredReasoningReplayAndOverride(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) { return "deepseek-key", name == "DEEPSEEK_API_KEY" })
	resolver := runtime.NewResolver(store, registry.New(store))
	provider, err := resolver.Resolve("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	settings := provider.RequestSettings("unlisted", nil, nil)
	if !settings.ReasoningRequired || settings.ReasoningField == nil || *settings.ReasoningField != "reasoning_content" {
		t.Fatalf("DeepSeek reasoning replay defaults = %+v", settings)
	}

	if err := store.LoadConfig([]byte(`{"providers":{"deepseek":{"reasoning_roundtrip":"off"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("unlisted", nil, nil)
	if !settings.ReasoningRequired || settings.ReasoningField != nil {
		t.Fatalf("explicit reasoning replay off was not respected: %+v", settings)
	}
	if !strings.Contains(strings.Join(provider.Warnings(), "\n"), "reasoning_roundtrip is off") {
		t.Fatalf("missing warning for required reasoning replay disabled: %v", provider.Warnings())
	}

	if err := store.LoadConfig([]byte(`{"providers":{"deepseek":{"reasoning_required":false}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err = resolver.Resolve("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	settings = provider.RequestSettings("unlisted", nil, nil)
	if settings.ReasoningRequired {
		t.Fatalf("explicit reasoning_required override was ignored: %+v", settings)
	}
}

func TestRequestSettingsResolveAnthropicLimitsThinkingAndCache(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"show_reasoning":"on","providers":{"anthropic-compatible":{"base_url":"https://messages.example/v1","cache":"off","cache_ttl":"5m","max_tokens":2000,"thinking_mode":"auto","thinking_budget":900}},"catalog":{"models":{"anthropic-compatible":{"m":{"limit":{"output":1024},"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	low := "low"
	settings := provider.RequestSettings("m", &low, nil)
	if settings.AnthropicCacheMarkers || settings.CacheTTL != "5m" {
		t.Fatalf("Anthropic cache settings were not resolved: %+v", settings)
	}
	if settings.MaxTokens != 1024 || settings.ThinkingMode != "adaptive" || settings.ThinkingBudget != 900 {
		t.Fatalf("model limits or adaptive effort mapping were not resolved: %+v", settings)
	}
	if got := provider.RequestSettings("unlisted", nil, nil).ThinkingMode; got != "budget" {
		t.Fatalf("auto mode without a settled model ladder should use budget: %q", got)
	}
	if !settings.ShowReasoning || !settings.AllowEmptySignature {
		t.Fatalf("global reasoning display or compatibility signatures were not resolved: %+v", settings)
	}
	none := "none"
	narrowedNone := provider.RequestSettings("m", &none, nil)
	if narrowedNone.ThinkingMode != "adaptive" || narrowedNone.EffectiveEffort == nil || *narrowedNone.EffectiveEffort != "low" {
		t.Fatalf("unsupported none effort should clamp to the model's low floor: %+v", narrowedNone)
	}
	unlistedNone := provider.RequestSettings("unlisted", &none, nil)
	if unlistedNone.ThinkingMode != "off" || unlistedNone.EffectiveEffort == nil || *unlistedNone.EffectiveEffort != "none" {
		t.Fatalf("provider-supported none effort should disable Anthropic thinking: %+v", unlistedNone)
	}
}

func TestRequestSettingsWarnsOnceForUnknownAnthropicThinkingMode(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"https://messages.example/v1","thinking_mode":"mystery"}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.Warnings()) != 0 {
		t.Fatalf("request-only policy warning should wait until Messages policy is resolved: %q", provider.Warnings())
	}
	provider.RequestSettings("m", nil, nil)
	provider.RequestSettings("m", nil, nil)
	warnings := provider.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown thinking_mode 'mystery'") {
		t.Fatalf("unknown mode should warn once and use the provider default: %q", warnings)
	}
}

func TestMetadataAPICanOverrideTheWireFamilyAndInvalidValuesFallBack(t *testing.T) {
	store := config.New(config.DefaultSettings())
	if err := store.LoadConfig([]byte(`{"providers":{"openai-compatible":{"base_url":"https://gateway.example/v1","metadata_api":"anthropic"}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := runtime.NewResolver(store, registry.New(store))
	resolved, err := providers.Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MetadataAPI != runtime.MetadataAnthropic {
		t.Fatalf("metadata API should be independent of the request wire: %+v", resolved)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"openai-compatible":{"base_url":"https://gateway.example/v1","metadata_api":"typo"}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err = providers.Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MetadataAPI != runtime.MetadataOpenAI || len(resolved.Warnings()) != 1 {
		t.Fatalf("invalid metadata API should warn and use the wire-family default: %+v warnings=%q", resolved, resolved.Warnings())
	}
}

func TestRequestHeadersMergeProviderDefaultsAndExpandSession(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "OPENROUTER_API_KEY" {
			return "router-key", true
		}
		return "", false
	})
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"extra_headers":{"x-title":"custom title","X-SESSION-ID":""}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	got := resolved.Headers(runtime.WireChat, "session-123", true)
	want := []string{
		"Authorization: Bearer router-key", "Accept: text/event-stream", "Content-Type: application/json",
		"HTTP-Referer: https://github.com/luank3/gax", "X-OpenRouter-Categories: cli-agent", "x-title: custom title",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider header defaults and overrides differ: got=%q want=%q", got, want)
	}
}

func TestAnthropicHeaderVersionAndRecursiveExtraBodyOverride(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "GAX_ANTHROPIC_API_KEY" {
			return "anthropic-key", true
		}
		if key == "GAX_ANTHROPIC_VERSION" {
			return "2025-01-01", true
		}
		return "", false
	})
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"https://messages.example/v1","extra_body":{"model":"wrong","nested":{"user":true}}},"codex":{"extra_body":{"instructions":"wrong","text":{"verbosity":"high","format":"plain"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := runtime.NewResolver(store, registry.New(store))
	anthropic, err := providers.Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := anthropic.Headers(runtime.WireAnthropic, "", true), []string{
		"x-api-key: anthropic-key", "anthropic-version: 2025-01-01", "Accept: text/event-stream", "Content-Type: application/json",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Anthropic auth/version headers differ: got=%q want=%q", got, want)
	}
	codex, err := providers.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshal(t, codex.ExtraBody), &body); err != nil {
		t.Fatal(err)
	}
	var text map[string]json.RawMessage
	if err := json.Unmarshal(body["text"], &text); err != nil {
		t.Fatal(err)
	}
	if string(text["verbosity"]) != `"high"` || string(text["format"]) != `"plain"` || body["instructions"] != nil {
		t.Fatalf("default and configured extra_body should merge recursively after filtering: %s", mustMarshal(t, codex.ExtraBody))
	}
}

func TestCodexAuthSourceLoadsCLIAccountAndBuildsAuthenticatedHeaders(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"),
		[]byte(`{"tokens":{"access_token":"access-secret","account_id":"account-123"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !provider.Availability.Ready() || !provider.HasAPIKey() || provider.Credentials() != "access-secret" || provider.AuthAccountID() != "account-123" {
		t.Fatalf("Codex auth should make the provider available: %+v", provider)
	}
	headers := strings.Join(provider.Headers(runtime.WireResponses, "", false), "\n")
	for _, want := range []string{"Authorization: Bearer access-secret", "chatgpt-account-id: account-123", "originator: codex_cli_rs"} {
		if !strings.Contains(headers, want) {
			t.Errorf("Codex request headers omit %q: %s", want, headers)
		}
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

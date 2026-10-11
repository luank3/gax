package client_test

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/anthropic"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

func TestStreamDispatchesResolvedWireBodyHeadersAndEvents(t *testing.T) {
	tests := []struct {
		name       string
		api        string
		path       string
		authHeader string
		body       string
		wire       runtime.Wire
	}{
		{
			name: "chat", api: "openai-completions", path: "/chat/completions",
			authHeader: "Authorization", wire: runtime.WireChat,
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "responses", api: "openai-responses", path: "/responses",
			authHeader: "Authorization", wire: runtime.WireResponses,
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\"}\n\n",
		},
		{
			name: "anthropic", api: "anthropic-messages", path: "/messages",
			authHeader: "x-api-key", wire: runtime.WireAnthropic,
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":2}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectedAuth := "client-secret"
			if test.authHeader == "Authorization" {
				expectedAuth = "Bearer client-secret"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1"+test.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get(test.authHeader); got != expectedAuth {
					t.Errorf("unexpected authentication header %s=%q", test.authHeader, got)
				}
				if r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("stream headers missing: %v", r.Header)
				}
				if r.Header.Get("X-Client-Test") != "enabled" {
					t.Errorf("provider extra header missing: %v", r.Header)
				}
				var requestBody map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				var model string
				if err := json.Unmarshal(requestBody["model"], &model); err != nil || model != "m1" {
					t.Errorf("wire body did not include model m1: %s", requestBody["model"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			resolved := resolveTestProvider(t, server.URL, test.api)
			request, err := client.PrepareWithOptions(resolved, "m1", coreprovider.Context{SystemPrompt: "system"}, client.BodyOptions{
				Anthropic: anthropic.BodyOptions{MaxTokens: 128},
			})
			if err != nil {
				t.Fatal(err)
			}
			if request.Wire != test.wire || request.Endpoint != server.URL+"/v1"+test.path {
				t.Fatalf("prepared request did not follow wire: %+v", request)
			}
			if got := headerValue(request.Headers, test.authHeader); got != expectedAuth {
				t.Fatalf("prepared auth header differs: got %q want %q in %q", got, expectedAuth, request.Headers)
			}
			if got := headerValue(request.Headers, "x-client-test"); got != "enabled" {
				t.Fatalf("provider extra header missing from request: %q", request.Headers)
			}
			if got := fmt.Sprintf("%+v %#v", request, &request); strings.Contains(got, "client-secret") || strings.Contains(got, "system") {
				t.Fatalf("request formatting exposed a credential or prompt body: %s", got)
			}

			httpClient := transport.NewClient()
			httpClient.HTTP = server.Client()
			var events []conversation.Event
			response, err := client.Stream(context.Background(), httpClient, resolved, "m1", coreprovider.Context{SystemPrompt: "system"}, client.StreamOptions{
				Body:        &client.BodyOptions{Anthropic: anthropic.BodyOptions{MaxTokens: 128}},
				RetryPolicy: transport.RetryPolicy{MaxAttempts: 1},
			}, nil, func(event conversation.Event) { events = append(events, event) })
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("unexpected HTTP response: %#v", response)
			}
			foundText, foundDone := false, false
			for _, event := range events {
				foundText = foundText || event.Kind == conversation.EventTextDelta && event.Text == "hello"
				foundDone = foundDone || event.Kind == conversation.EventDone
			}
			if !foundText || !foundDone {
				t.Fatalf("wire events were not translated: %#v", events)
			}
		})
	}
}

func TestStreamPrefetchesButDoesNotWaitForMetadataOnPureResponses(t *testing.T) {
	catalogStarted := make(chan struct{}, 1)
	requestStarted := make(chan struct{}, 1)
	releaseCatalog := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCatalog) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog":
			catalogStarted <- struct{}{}
			<-releaseCatalog
			_, _ = io.WriteString(w, `{"openai":{"npm":"@ai-sdk/openai","models":{}}}`)
		case "/v1/responses":
			requestStarted <- struct{}{}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(string) (string, bool) { return "", false })
	if err := store.LoadConfig([]byte(`{"catalog":{"url":"` + server.URL + `/catalog","refresh":"24h"},"providers":{"openai-compatible":{"base_url":"` + server.URL + `/v1","api_key":"test-key","api":"openai-responses","catalog_id":"openai"}}}`)); err != nil {
		t.Fatal(err)
	}
	models := catalog.New(store)
	defer func() {
		release()
		models.Shutdown()
	}()
	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	models.PrefetchWithOptions(catalog.FetchOptions{
		Client: httpClient, CachePath: filepath.Join(t.TempDir(), "catalog.json"), Timeout: 10 * time.Second,
	})
	select {
	case <-catalogStarted:
	case <-time.After(time.Second):
		t.Fatal("catalog refresh did not start")
	}

	resolver := runtime.NewResolver(store, registry.New(store))
	resolver.SetCatalog(models)
	provider, err := resolver.Resolve("openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if provider.RequestReadsModelMetadata() {
		t.Fatal("pure Responses provider unexpectedly reads request metadata")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, httpClient, provider, "model",
			coreprovider.Context{}, client.StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 1}}, nil,
			func(conversation.Event) {})
		done <- err
	}()
	select {
	case <-requestStarted:
		release()
	case <-time.After(time.Second):
		cancel()
		release()
		<-done
		t.Fatal("Responses request waited for the catalog refresh")
	}
	if err := <-done; err != nil {
		t.Fatalf("Responses request failed: %v", err)
	}
}

func TestMockStreamRunsWithoutHTTPEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock.txt")
	if err := os.WriteFile(path, []byte("text scripted answer\nend-turn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	configData, err := json.Marshal(map[string]any{"providers": map[string]any{"mock": map[string]string{"script": path}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LoadConfig(configData); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("mock")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Endpoint != "" || provider.DefaultModel != "mock-model" {
		t.Fatalf("mock provider should use its in-process default: %+v", provider)
	}

	var events []conversation.Event
	response, err := client.Stream(context.Background(), nil, provider, provider.DefaultModel,
		coreprovider.Context{}, client.StreamOptions{}, nil,
		func(event conversation.Event) { events = append(events, event) })
	if err != nil || response.StatusCode != 0 || response.Cancelled {
		t.Fatalf("mock client stream = (%+v, %v)", response, err)
	}
	var answer strings.Builder
	done := false
	for _, event := range events {
		if event.Kind == conversation.EventTextDelta {
			answer.WriteString(event.Text)
		}
		done = done || event.Kind == conversation.EventDone
	}
	if answer.String() != "scripted answer" || !done {
		t.Fatalf("mock events = %+v", events)
	}
}

func TestPrepareDoesNotInferReasoningMemberFromTranscript(t *testing.T) {
	resolved := resolveTestProvider(t, "https://gateway.example", "openai-completions")
	request, err := client.Prepare(resolved, "m1", coreprovider.Context{Items: []conversation.Item{{
		Kind: conversation.KindReasoning, ReasoningText: conversation.String("thought"), ReasoningField: "reasoning",
		Provider: conversation.String("gateway"), Model: conversation.String("m1"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 0 {
		t.Fatalf("provider preparation inferred a reasoning member from transcript data: %#v", body.Messages)
	}
}

func TestCodexStreamRefreshesAndRetriesOnceAfterUnauthorized(t *testing.T) {
	home, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	storePath := filepath.Join(stateHome, "gax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	oldAccess := streamTestJWT(t, time.Now().Add(-time.Minute).Unix())
	newAccess := streamTestJWT(t, time.Now().Add(time.Hour).Unix())
	finalAccess := streamTestJWT(t, time.Now().Add(2*time.Hour).Unix())
	storeBody := `{"codex":{"access_token":"` + oldAccess + `","refresh_token":"old-refresh","account_id":"account-123"}}`
	if err := os.WriteFile(storePath, []byte(storeBody), 0600); err != nil {
		t.Fatal(err)
	}

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(os.LookupEnv)
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = "https://chatgpt.com/backend-api/codex"
	provider.CatalogID = nil

	var modelRequests, refreshRequests atomic.Int32
	httpClient := &transport.Client{HTTP: &http.Client{Transport: streamRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "auth.openai.com" {
			refreshNumber := refreshRequests.Add(1)
			var body map[string]string
			wantRefresh := "old-refresh"
			rotatedRefresh := "new-refresh"
			rotatedAccess := newAccess
			if refreshNumber == 2 {
				wantRefresh = "new-refresh"
				rotatedRefresh = "final-refresh"
				rotatedAccess = finalAccess
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["refresh_token"] != wantRefresh {
				t.Errorf("Codex refresh request body = %v, err=%v", body, err)
			}
			return streamResponse(request, http.StatusOK, `{"access_token":"`+rotatedAccess+`","refresh_token":"`+rotatedRefresh+`"}`), nil
		}
		modelRequests.Add(1)
		if request.URL.Path != "/backend-api/codex/responses" || request.Header.Get("chatgpt-account-id") != "account-123" {
			t.Errorf("Codex model request target or account mismatch: %s headers=%v", request.URL, request.Header)
		}
		switch request.Header.Get("Authorization") {
		case "Bearer " + newAccess:
			return streamResponse(request, http.StatusUnauthorized, `{"error":"expired"}`), nil
		case "Bearer " + finalAccess:
			return streamResponse(request, http.StatusOK, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"), nil
		default:
			t.Errorf("unexpected Codex authorization header: %q", request.Header.Get("Authorization"))
			return streamResponse(request, http.StatusUnauthorized, "{}"), nil
		}
	})}}

	var events []conversation.Event
	response, err := client.Stream(context.Background(), httpClient, provider, "gpt-5", coreprovider.Context{}, client.StreamOptions{
		RetryPolicy: transport.RetryPolicy{MaxAttempts: 1},
	}, nil, func(event conversation.Event) { events = append(events, event) })
	if err != nil || response.StatusCode != http.StatusOK || modelRequests.Load() != 2 || refreshRequests.Load() != 2 {
		t.Fatalf("Codex unauthorized recovery: response=%+v model requests=%d refresh requests=%d err=%v",
			response, modelRequests.Load(), refreshRequests.Load(), err)
	}
	foundText := false
	for _, event := range events {
		foundText = foundText || event.Kind == conversation.EventTextDelta && event.Text == "ready"
	}
	if !foundText || provider.Credentials() != finalAccess {
		t.Fatalf("Codex stream did not use refreshed auth: events=%+v credential=%q", events, provider.Credentials())
	}
	updated, err := os.ReadFile(storePath)
	if err != nil || !strings.Contains(string(updated), "final-refresh") {
		t.Fatalf("rotated Codex credentials not saved: %s err=%v", updated, err)
	}
}

func TestCodexDeferredModelProbeRestartsAfterRequestAuthRefresh(t *testing.T) {
	home, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	storePath := filepath.Join(stateHome, "gax", "auth.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	oldAccess := streamTestJWT(t, time.Now().Add(30*time.Second).Unix())
	newAccess := streamTestJWT(t, time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(storePath,
		[]byte(`{"codex":{"access_token":"`+oldAccess+`","refresh_token":"old-refresh","account_id":"account-123"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(os.LookupEnv)
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = "https://chatgpt.com/backend-api/codex"
	provider.CatalogID = nil

	var modelRequests, probeRequests, refreshRequests atomic.Int32
	probeStarted := make(chan struct{}, 1)
	httpClient := &transport.Client{HTTP: &http.Client{Transport: streamRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "auth.openai.com" {
			refreshRequests.Add(1)
			return streamResponse(request, http.StatusOK,
				`{"access_token":"`+newAccess+`","refresh_token":"new-refresh"}`), nil
		}
		switch request.URL.Path {
		case "/backend-api/codex/models":
			probeRequests.Add(1)
			if request.Header.Get("Authorization") != "Bearer "+newAccess {
				t.Errorf("deferred probe did not use refreshed Codex credentials: %q", request.Header.Get("Authorization"))
			}
			probeStarted <- struct{}{}
			return streamResponse(request, http.StatusOK,
				`{"models":[{"slug":"gpt-5","context_window":128000}]}`), nil
		case "/backend-api/codex/responses":
			modelRequests.Add(1)
			return streamResponse(request, http.StatusOK,
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"), nil
		default:
			t.Errorf("unexpected Codex request: %s", request.URL)
			return streamResponse(request, http.StatusNotFound, "{}"), nil
		}
	})}}
	if !provider.StartModelProbe(context.Background(), "gpt-5", httpClient) {
		t.Fatal("initial Codex model probe did not start")
	}
	if !provider.WaitModelProbe(time.Second, nil) {
		t.Fatal("near-expiry Codex probe did not finish its deferral")
	}
	if probeRequests.Load() != 0 {
		t.Fatalf("near-expiry Codex probe unexpectedly made %d requests", probeRequests.Load())
	}

	response, err := client.Stream(context.Background(), httpClient, provider, "gpt-5",
		coreprovider.Context{}, client.StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 1}}, nil,
		func(conversation.Event) {})
	if err != nil || response.StatusCode != http.StatusOK || refreshRequests.Load() != 1 || modelRequests.Load() != 1 {
		t.Fatalf("Codex request after auth refresh: response=%+v refresh=%d model=%d err=%v",
			response, refreshRequests.Load(), modelRequests.Load(), err)
	}
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred Codex model probe was not restarted after auth refresh")
	}
	if !provider.WaitModelProbe(time.Second, nil) {
		t.Fatal("restarted Codex model probe did not finish")
	}
	if probeRequests.Load() != 1 {
		t.Fatalf("restarted Codex probe made %d requests, want 1", probeRequests.Load())
	}
	if metadata, found := provider.ModelMetadata("gpt-5"); !found || metadata.ContextWindow != 128000 {
		t.Fatalf("deferred Codex metadata was not retained: %+v found=%t", metadata, found)
	}
}

func streamTestJWT(t *testing.T, expiry int64) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": expiry})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func streamResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

type streamRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip streamRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func headerValue(headers []string, name string) string {
	for _, line := range headers {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func TestPrepareAppliesModelRoutingAndProviderExtraBody(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"openai-completions","model_apis":{"vendor/*":"openai-responses"},"extra_body":{"store":false,"metadata":{"user":true}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.Prepare(resolved, "vendor/model", coreprovider.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if request.Wire != runtime.WireResponses || request.Endpoint != "https://gateway.example/v1/responses" {
		t.Fatalf("prepared endpoint ignored model wire rule: %+v", request)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["store"] != false || body["metadata"].(map[string]any)["user"] != true {
		t.Fatalf("provider extra_body was not applied: %s", request.Body)
	}
}

func TestPrepareResolvesOpenRouterBodyDefaultsFromProviderAndCatalog(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"openrouter":{"cache":"on","cache_ttl":"1h"}},"catalog":{"models":{"openrouter":{"m":{"cost":{"input":2,"cache_write":1,"cache_write_1h":3},"interleaved":{"field":"reasoning_content"}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	effort, sessionID, text := "high", "session-42", "hello"
	providerID, model, thought := "openrouter", "m", "prior thought"
	request, err := client.Prepare(resolved, "m", coreprovider.Context{
		Effort: &effort, SessionID: &sessionID,
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: &text},
			{Kind: conversation.KindReasoning, Provider: &providerID, Model: &model, ReasoningText: &thought},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["prompt_cache_key"] != sessionID || body["usage"].(map[string]any)["include"] != true {
		t.Fatalf("provider session cache and usage defaults missing: %s", request.Body)
	}
	if body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("provider reasoning format was not applied: %s", request.Body)
	}
	messages := body["messages"].([]any)
	assistant := messages[len(messages)-1].(map[string]any)
	if assistant["reasoning_content"] != thought {
		t.Fatalf("catalog interleaved-reasoning field was not applied: %s", request.Body)
	}
	content := messages[0].(map[string]any)["content"].([]any)
	if content[len(content)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("explicit cache mode did not add a cache breakpoint: %s", request.Body)
	}
}

func TestPrepareClampsUnsupportedEffortToModelCapabilities(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"https://gateway.example/v1","api":"openai-completions"}},"catalog":{"models":{"gateway":{"m":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`)); err != nil {
		t.Fatal(err)
	}
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	effort := "max"
	request, err := client.Prepare(provider, "m", coreprovider.Context{Effort: &effort})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("request effort = %v, want the nearest supported level high; body=%s", body["reasoning_effort"], request.Body)
	}
}

func TestPrepareResolvesAnthropicBodyDefaultsAndModelOutputCap(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"anthropic-compatible":{"base_url":"https://messages.example/v1","cache":"off","max_tokens":500,"thinking_mode":"off"}},"catalog":{"models":{"anthropic-compatible":{"m":{"limit":{"output":300}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("anthropic-compatible")
	if err != nil {
		t.Fatal(err)
	}
	effort, text := "high", "system prompt"
	request, err := client.Prepare(resolved, "m", coreprovider.Context{SystemPrompt: text, Effort: &effort})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(300) || body["thinking"] != nil {
		t.Fatalf("model output cap or disabled thinking was ignored: %s", request.Body)
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != text || system["cache_control"] != nil {
		t.Fatalf("Anthropic cache-off setting was ignored: %s", request.Body)
	}
}

func TestStreamUsesCatalogOneHourCacheRateForChatUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cache_write_tokens\":3}}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"gateway":{"base_url":"` + server.URL + `/v1","api":"openai-completions","cache":"on","cache_ttl":"1h"}},"catalog":{"models":{"gateway":{"m":{"cost":{"input":2,"cache_write":3,"cache_write_1h":6}}}}}}`)); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	var events []conversation.Event
	response, err := client.Stream(context.Background(), httpClient, resolved, "m", coreprovider.Context{}, client.StreamOptions{
		RetryPolicy: transport.RetryPolicy{MaxAttempts: 1},
	}, nil, func(event conversation.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(events) == 0 {
		t.Fatalf("stream did not complete: response=%#v events=%#v", response, events)
	}
	last := events[len(events)-1]
	if last.Kind != conversation.EventDone || last.Usage == nil || last.Usage.CacheWrite1HTokens == nil || *last.Usage.CacheWrite1HTokens != 3 {
		t.Fatalf("known one-hour cache rate was not reflected in usage: %#v", events)
	}
}

func resolveTestProvider(t *testing.T, baseURL, api string) *runtime.Provider {
	t.Helper()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "CLIENT_TEST_KEY" {
			return "client-secret", true
		}
		return "", false
	})
	data := []byte(`{"providers":{"gateway":{"base_url":"` + baseURL + `/v1","api":"` + api + `","api_key_env":"CLIENT_TEST_KEY","extra_headers":{"x-client-test":"enabled"}}}}`)
	if err := store.LoadConfig(data); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestStreamWaitsForModelCatalogBeforeSelectingWireAndLimits(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var catalogRequests atomic.Int32
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		catalogRequests.Add(1)
		_, _ = w.Write([]byte(`{"vendor":{"models":{"m":{"api":"anthropic-messages","limit":{"output":512}}}}}`))
	}))
	defer catalogServer.Close()

	var providerRequests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		if r.URL.Path != "/v1/messages" {
			t.Errorf("catalog wire selection chose wrong endpoint: %s", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode anthropic body: %v", err)
		}
		var maxTokens int
		if err := json.Unmarshal(body["max_tokens"], &maxTokens); err != nil || maxTokens != 512 {
			t.Errorf("catalog output limit not applied: max_tokens=%s err=%v", body["max_tokens"], err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer providerServer.Close()

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(fmt.Sprintf(`{"providers":{"gateway":{"base_url":%q,"api":"catalog","catalog_id":"vendor"}},"catalog":{"url":%q,"refresh":"24h"}}`, providerServer.URL+"/v1", catalogServer.URL))); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.NewResolver(store, registry.New(store)).Resolve("gateway")
	if err != nil {
		t.Fatal(err)
	}
	httpClient := transport.NewClient()
	httpClient.HTTP = providerServer.Client()
	response, err := client.Stream(context.Background(), httpClient, resolved, "m", coreprovider.Context{}, client.StreamOptions{
		RetryPolicy: transport.RetryPolicy{MaxAttempts: 1},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || catalogRequests.Load() != 1 || providerRequests.Load() != 1 {
		t.Fatalf("catalog was not fetched before the provider request: response=%#v catalog=%d provider=%d", response, catalogRequests.Load(), providerRequests.Load())
	}
}

package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/web"
)

const webPage = `<html><head><title>Docs</title></head><body><h1>Docs</h1><script>bad()</script><p>Body &amp; text</p></body></html>`

// webSearchFrontend is the result the keyless You.com backend answers with: two entries, so a
// limited search can be told from a full one. It stays on one line because the MCP reply is a
// server-sent event.
const webSearchFrontend = `{"results":{"web":[` +
	`{"title":"The <b>complete</b> guide","url":"https://blog.example.com/guide","description":"Take apart the timeout stages."},` +
	`{"title":"How to set a timeout?","url":"https://stackoverflow.com/q/1","description":"Use a context deadline."}]}}`

// webSearchCrawl is the result page DuckDuckGo serves, which the fallback parses.
const webSearchCrawl = `<html><body>
<a rel="nofollow" class="result__a" href="https://blog.example.com/guide">The complete <b>guide</b></a>
<a class="result__snippet" href="https://blog.example.com/guide">Take apart the <b>timeout</b> stages.</a>
<a rel="nofollow" class="result__a" href="https://stackoverflow.com/q/1">How to set a timeout?</a>
<a class="result__snippet" href="https://stackoverflow.com/q/1">Use a context deadline.</a>
</body></html>`

// webSearchBrave is the result the Brave Search API answers with.
const webSearchBrave = `{"web":{"results":[{"title":"Go timeouts","url":"https://blog.example.com/guide","description":"Take apart the stages."}]}}`

// newWebTestServer serves the page, search, and keyless frontend endpoints the web tools use, so
// the tool tests exercise the real rendering without reaching the network.
func newWebTestServer(t *testing.T) (*httptest.Server, *web.Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, webPage)
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
		case "/free-search":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message\ndata: "+`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":`+webSearchFrontend+`}}`+"\n\n")
		case "/brave":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, webSearchBrave)
		default:
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "missing")
		}
	}))
	t.Cleanup(server.Close)
	return server, &web.Client{HTTP: server.Client(), BraveEndpoint: server.URL + "/brave", YouMCPEndpoint: server.URL + "/free-search"}
}

func newWebTestRegistry(t *testing.T, client *web.Client, options Options) *Registry {
	t.Helper()
	options.WebClient = client
	registry := New(options)
	t.Cleanup(registry.Close)
	return registry
}

func TestWebToolDefinitionsAndDisable(t *testing.T) {
	registry := New(Options{})
	definitions := registry.Definitions()
	byName := make(map[string]int, len(definitions))
	for index, definition := range definitions {
		byName[definition.Name] = index
	}
	fetchIndex, ok := byName["web_fetch"]
	if !ok {
		t.Fatalf("web_fetch is not advertised: %#v", definitions)
	}
	if byName["web_search"] != fetchIndex+1 || fetchIndex != len(definitions)-3 {
		t.Fatalf("web tools are not adjacent and before the skill tool: %#v", definitions)
	}
	fetch := definitions[fetchIndex]
	if len(fetch.Params) != 2 || fetch.Params[0].Name != "url" || !fetch.Params[0].Required || fetch.Params[1].Name != "strip" {
		t.Fatalf("web_fetch schema = %#v", fetch.Params)
	}
	search := definitions[byName["web_search"]]
	if len(search.Params) != 2 || search.Params[0].Name != "query" || !search.Params[0].Required || search.Params[1].Name != "count" {
		t.Fatalf("web_search schema = %#v", search.Params)
	}

	disabled := New(Options{NoWeb: true}).Definitions()
	for _, definition := range disabled {
		if definition.Name == "web_fetch" || definition.Name == "web_search" {
			t.Fatalf("no_web still advertised %q", definition.Name)
		}
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"no_web":true}`)); err != nil {
		t.Fatal(err)
	}
	fromConfig := NewFromConfig(store)
	defer fromConfig.Close()
	if len(fromConfig.Definitions()) != 6 {
		t.Fatalf("no_web config still advertised the web tools: %#v", fromConfig.Definitions())
	}
}

func TestWebFetchStripsHTMLByDefault(t *testing.T) {
	server, client := newWebTestServer(t)
	registry := newWebTestRegistry(t, client, Options{})

	result := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/page"}), 1)
	want := "Docs\nDocs\nBody & text\n\n[200 OK · text/html · stripped · " + formatByteSize(int64(len(webPage))) + "]"
	if result.Text != want {
		t.Fatalf("stripped fetch = %q, want %q", result.Text, want)
	}

	raw := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/page", "strip": false}), 1)
	want = webPage + "\n\n[200 OK · text/html · " + formatByteSize(int64(len(webPage))) + "]"
	if raw.Text != want {
		t.Fatalf("raw fetch = %q, want %q", raw.Text, want)
	}
}

func TestWebFetchReportsBinaryStatusAndFailures(t *testing.T) {
	server, client := newWebTestServer(t)
	registry := newWebTestRegistry(t, client, Options{})

	binary := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/image"}), 1)
	want := "[200 OK · image/png · 4B · only text responses are returned]"
	if binary.Text != want {
		t.Fatalf("binary fetch = %q, want %q", binary.Text, want)
	}

	missing := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/nope"}), 1)
	if !strings.HasPrefix(missing.Text, "missing\n\n[404 Not Found · text/plain · ") {
		t.Fatalf("404 fetch = %q", missing.Text)
	}

	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "missing url", arguments: `{}`, want: "missing 'url' argument"},
		{name: "blank url", arguments: `{"url":"   "}`, want: "missing 'url' argument"},
		{name: "bad scheme", arguments: `{"url":"ftp://example.com"}`, want: `error fetching ftp://example.com: unsupported URL scheme "ftp" (use http:// or https://)`},
		{name: "bad strip", arguments: `{"url":"https://example.com","strip":"yes"}`, want: "'strip' must be a boolean"},
		{name: "bad json", arguments: `nope`, want: "invalid arguments: "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := registry.Run("web_fetch", test.arguments, 1)
			if !strings.Contains(got.Text, test.want) {
				t.Fatalf("web_fetch(%s) = %q, want substring %q", test.arguments, got.Text, test.want)
			}
		})
	}
}

func TestWebFetchCapsOutput(t *testing.T) {
	server, client := newWebTestServer(t)
	registry := newWebTestRegistry(t, client, Options{OutputCap: 24})

	result := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/page"}), 1)
	if !strings.HasSuffix(result.Text, "[truncated at 24 bytes; the response is larger — fetch a narrower URL or use bash + curl]") {
		t.Fatalf("capped fetch = %q", result.Text)
	}
	if len(result.Text) > 24+len("\n\n[truncated at 24 bytes; the response is larger — fetch a narrower URL or use bash + curl]") {
		t.Fatalf("capped fetch kept too much: %d bytes", len(result.Text))
	}
}

func TestWebSearchRendersResults(t *testing.T) {
	_, client := newWebTestServer(t)
	registry := newWebTestRegistry(t, client, Options{})

	result := registry.Run("web_search", jsonArgs(map[string]any{"query": "golang timeout"}), 1)
	want := "1. The complete guide\n   https://blog.example.com/guide\n   Take apart the timeout stages.\n\n" +
		"2. How to set a timeout?\n   https://stackoverflow.com/q/1\n   Use a context deadline.\n\n" +
		"[2 results · you]"
	if result.Text != want {
		t.Fatalf("search = %q, want %q", result.Text, want)
	}

	limited := registry.Run("web_search", jsonArgs(map[string]any{"query": "golang timeout", "count": 1}), 1)
	if !strings.HasSuffix(limited.Text, "[1 results · you]") || strings.Contains(limited.Text, "stackoverflow") {
		t.Fatalf("limited search = %q", limited.Text)
	}

	missing := registry.Run("web_search", jsonArgs(map[string]any{}), 1)
	if missing.Text != "missing 'query' argument" {
		t.Fatalf("search without a query = %q", missing.Text)
	}
	if bad := registry.Run("web_search", jsonArgs(map[string]any{"query": "q", "count": 0}), 1); bad.Text != "'count' must be >= 1" {
		t.Fatalf("search with count 0 = %q", bad.Text)
	}
}

func TestWebSearchUsesTheConfiguredBraveKey(t *testing.T) {
	server, client := newWebTestServer(t)
	var subscription string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subscription = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"web":{"results":[{"title":"Go timeouts","url":"https://blog.example.com/guide","description":"Take apart the stages."}]}}`)
	}))
	defer brave.Close()

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"web.brave_api_key":"brave-secret"}`)); err != nil {
		t.Fatal(err)
	}
	client.BraveEndpoint = brave.URL
	registry := newWebTestRegistry(t, client, Options{})
	registry.store = store

	result := registry.Run("web_search", jsonArgs(map[string]any{"query": "golang timeout"}), 1)
	if !strings.HasSuffix(result.Text, "[1 results · brave]") || subscription != "brave-secret" {
		t.Fatalf("brave search = %q (subscription header %q)", result.Text, subscription)
	}
	if strings.Contains(result.Text, "brave-secret") {
		t.Fatalf("search output leaked the key: %q", result.Text)
	}
	_ = server
}

// TestWebSearchProviderSettingPicksTheBackend covers the provider override: an explicit backend
// answers even when another one holds a key.
func TestWebSearchProviderSettingPicksTheBackend(t *testing.T) {
	_, client := newWebTestServer(t)
	var reached string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = "brave"
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, webSearchBrave)
	}))
	defer brave.Close()
	client.BraveEndpoint = brave.URL

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"web.search_provider":"you","web.brave_api_key":"brave-secret"}`)); err != nil {
		t.Fatal(err)
	}
	registry := newWebTestRegistry(t, client, Options{})
	registry.store = store

	result := registry.Run("web_search", jsonArgs(map[string]any{"query": "golang timeout"}), 1)
	if !strings.HasSuffix(result.Text, "[2 results · you]") || reached != "" {
		t.Fatalf("provider override search = %q (brave was reached: %q)", result.Text, reached)
	}
}

// TestWebSearchFallsBackToTheDuckDuckGoCrawl covers the fallback through the tool: the keyless
// default cannot answer, and the crawl still returns results the model can read.
func TestWebSearchFallsBackToTheDuckDuckGoCrawl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crawl":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, webSearchCrawl)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"upstream is down"}}`)
		}
	}))
	defer server.Close()
	client := &web.Client{
		HTTP:               server.Client(),
		YouMCPEndpoint:     server.URL + "/free-search",
		HTMLSearchEndpoint: server.URL + "/crawl",
	}
	registry := newWebTestRegistry(t, client, Options{})

	result := registry.Run("web_search", jsonArgs(map[string]any{"query": "golang timeout"}), 1)
	want := "1. The complete guide\n   https://blog.example.com/guide\n   Take apart the timeout stages.\n\n" +
		"2. How to set a timeout?\n   https://stackoverflow.com/q/1\n   Use a context deadline.\n\n" +
		"[2 results · duckduckgo]"
	if result.Text != want {
		t.Fatalf("fallback search = %q, want %q", result.Text, want)
	}
}

// TestWebSearchKeySettingsMatchTheBackends keeps the credential settings and the backends in step:
// one web.<provider>_api_key setting per backend, reading that backend's documented environment
// variable, and a provider setting whose choices are the backends.
func TestWebSearchKeySettingsMatchTheBackends(t *testing.T) {
	settings := make(map[string]config.Setting)
	for _, setting := range config.DefaultSettings() {
		settings[setting.Key] = setting
	}
	providers := make(map[string]bool, len(web.SearchProviders))
	for _, provider := range web.SearchProviders {
		providers[provider] = true
		setting, ok := settings[webSearchKeySetting(provider)]
		if !ok {
			t.Fatalf("web.%s_api_key is not a registered setting", provider)
		}
		if setting.EnvVar != web.CredentialEnv(provider) || !setting.Secret {
			t.Fatalf("web.%s_api_key = %+v, want the credential variable %q marked secret",
				provider, setting, web.CredentialEnv(provider))
		}
	}
	for key := range settings {
		if name, found := strings.CutPrefix(key, "web."); found {
			if provider, found := strings.CutSuffix(name, "_api_key"); found && !providers[provider] {
				t.Fatalf("%s names a backend that does not exist", key)
			}
		}
	}
	choice, ok := settings["web.search_provider"]
	if !ok {
		t.Fatal("web.search_provider is not a registered setting")
	}
	if want := "auto|" + strings.Join(web.SearchBackends, "|"); choice.Choices != want {
		t.Fatalf("web.search_provider choices = %q, want %q", choice.Choices, want)
	}
}

func TestWebToolsReportDisabledExecution(t *testing.T) {
	server, client := newWebTestServer(t)
	registry := newWebTestRegistry(t, client, Options{NoWeb: true})
	for _, tool := range []string{"web_fetch", "web_search"} {
		if got := registry.Run(tool, `{}`, 1); got.Text != "the web tools are disabled (no_web)" {
			t.Fatalf("%s with no_web = %q", tool, got.Text)
		}
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"no_web":true}`)); err != nil {
		t.Fatal(err)
	}
	registry.store = store
	if got := registry.Run("web_fetch", jsonArgs(map[string]any{"url": server.URL + "/page"}), 1); got.Text != "the web tools are disabled (no_web)" {
		t.Fatalf("web_fetch under a no_web store = %q", got.Text)
	}
}

func TestWebFetchHonorsTurnCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "late")
	}))
	defer server.Close()
	defer close(release)

	registry := newWebTestRegistry(t, &web.Client{HTTP: server.Client()}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	call := conversation.Item{
		Kind: conversation.KindToolCall, CallID: conversation.String("fetch-1"),
		ToolName: conversation.String("web_fetch"), Arguments: conversation.String(jsonArgs(map[string]any{"url": server.URL})),
	}
	execution := registry.ExecuteWithDisplayContext(ctx, call, 1, nil)
	if execution.Result.Output == nil || !strings.Contains(*execution.Result.Output, "interrupted") {
		t.Fatalf("cancelled fetch = %#v", execution.Result)
	}
}

func TestWebToolConfigLimits(t *testing.T) {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"web.timeout":"5s","web.max_bytes":"1k"}`)); err != nil {
		t.Fatal(err)
	}
	fromConfig := NewFromConfig(store)
	defer fromConfig.Close()
	limits := fromConfig.webLimits()
	if limits.Timeout.Seconds() != 5 || limits.MaxBytes != 1024 {
		t.Fatalf("web limits = %+v", limits)
	}
}

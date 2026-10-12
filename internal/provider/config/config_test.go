package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	baseconfig "github.com/luank3/gax/internal/config"
	providerconfig "github.com/luank3/gax/internal/provider/config"
	"github.com/luank3/gax/internal/wiretrace"
)

func TestScopedValuesUseRegistryEnvironmentAliases(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		if key == "GAX_OPENAI_BASE_URL" {
			return "https://gateway.example/v1", true
		}
		return "", false
	})
	providers := providerconfig.New(store)
	if got := providers.String("providers.openai-compatible", "base_url"); got == nil || *got != "https://gateway.example/v1" {
		t.Fatalf("provider field did not use its GAX alias: %v", got)
	}
}

func TestResolvedProviderSecretsAreRedactedFromWireTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	logger := wiretrace.Default()
	if err := logger.Configure(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		_ = logger.Configure("")
	})
	store := baseconfig.New(baseconfig.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		switch key {
		case "INLINE_SECRET":
			return "api-key-secret", true
		case "HEADER_SECRET":
			return "custom-header-secret", true
		default:
			return "", false
		}
	})
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"api_key":"$INLINE_SECRET","extra_headers":{"X-Custom":"$HEADER_SECRET"}}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := providerconfig.New(store)
	apiKey := providers.APIKey("providers.demo", "")
	headers := providers.ExtraHeaderTemplates("providers.demo")
	logger.Request("POST", "https://example.test", append(headers, "X-Provider-Key: "+apiKey), []byte(`{"token":"api-key-secret"}`))
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, secret := range []string{"api-key-secret", "custom-header-secret"} {
		if strings.Contains(text, secret) {
			t.Errorf("wire trace leaked resolved credential %q: %s", secret, text)
		}
	}
	for _, want := range []string{"X-Custom: <redacted>", "X-Provider-Key: <redacted>", `"token": "<redacted>"`} {
		if !strings.Contains(text, want) {
			t.Errorf("wire trace missing %q: %s", want, text)
		}
	}
}

func TestAPIKeyIndirectionEscapeAndFallback(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	store.SetEnvironment(func(key string) (string, bool) {
		switch key {
		case "INLINE_SECRET":
			return "inline-secret-value", true
		case "FALLBACK_SECRET":
			return "fallback-secret-value", true
		case "EMPTY_SECRET":
			return "", true
		default:
			return "", false
		}
	})
	providers := providerconfig.New(store)

	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"api_key":"$INLINE_SECRET"}}}`)); err != nil {
		t.Fatal(err)
	}
	if got := providers.APIKey("providers.demo", "FALLBACK_SECRET"); got != "inline-secret-value" {
		t.Fatalf("inline environment indirection did not resolve: %q", got)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"api_key":"$MISSING_SECRET"}}}`)); err != nil {
		t.Fatal(err)
	}
	if got := providers.APIKey("providers.demo", "FALLBACK_SECRET"); got != "fallback-secret-value" {
		t.Fatalf("unresolved indirection should fall back to the provider env alias: %q", got)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"api_key":"$$literal"}}}`)); err != nil {
		t.Fatal(err)
	}
	if got := providers.APIKey("providers.demo", "FALLBACK_SECRET"); got != "$literal" {
		t.Fatalf("doubled dollar should escape env lookup: %q", got)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"api_key":"$EMPTY_SECRET"}}}`)); err != nil {
		t.Fatal(err)
	}
	if got := providers.APIKey("providers.demo", "FALLBACK_SECRET"); got != "fallback-secret-value" {
		t.Fatalf("empty indirection should fall back to provider env alias: %q", got)
	}
}

func TestCacheTTLAndExtraBodyFiltering(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	providers := providerconfig.New(store)
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"cache_ttl":"5M","extra_body":{"messages":[],"allowed":{"x":1}}}}}`)); err != nil {
		t.Fatal(err)
	}
	if got := providers.CacheTTL("providers.demo"); got != "5m" {
		t.Fatalf("cache TTL should canonicalize case: %q", got)
	}
	body := providers.ExtraBody("providers.demo")
	if len(body) != 1 || string(body["allowed"]) != `{"x":1}` {
		t.Fatalf("protocol-owned body keys were not filtered: %s", marshalBody(t, body))
	}
	if err := store.LoadState([]byte(`{"providers":{"demo":{"extra_body":{"tools":[],"nested":{"keep":true}}}}}`)); err != nil {
		t.Fatal(err)
	}
	body = providers.ExtraBody("providers.demo")
	if len(body) != 1 || string(body["nested"]) != `{"keep":true}` {
		t.Fatalf("state object should replace config object before filtering: %s", marshalBody(t, body))
	}
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"cache_ttl":"invalid","extra_body":[]}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadState(nil); err != nil {
		t.Fatal(err)
	}
	if got := providers.CacheTTL("providers.demo"); got != "1h" {
		t.Fatalf("invalid cache TTL should default to 1h: %q", got)
	}
	if body := providers.ExtraBody("providers.demo"); body != nil {
		t.Fatalf("non-object extra_body should be ignored: %v", body)
	}
}

func TestExtraHeadersValidateResolveMergeAndExpand(t *testing.T) {
	store := baseconfig.New(nil)
	store.SetEnvironment(func(key string) (string, bool) {
		switch key {
		case "HEADER_SECRET":
			return "token-value", true
		case "EMPTY_HEADER":
			return "", true
		default:
			return "", false
		}
	})
	providers := providerconfig.New(store)
	object := json.RawMessage(`{"X-Session":"session={session_id}","X-Override":"new","X-Token":"$HEADER_SECRET","X-Literal":"$$cash","X-Remove":"","X-Bad-Value":"safe\r\nInjected: yes","Bad Name":"ignored","X-Empty-Env":"$EMPTY_HEADER"}`)
	overrides := providers.HeadersFromObject(object)
	wantOverrides := []string{"X-Session: session={session_id}", "X-Override: new", "X-Token: token-value", "X-Literal: $cash", "X-Remove:"}
	if !reflect.DeepEqual(overrides, wantOverrides) {
		t.Fatalf("header parsing differs: got=%q want=%q", overrides, wantOverrides)
	}
	defaults := []string{"X-Default: keep", "x-override: old", "X-Remove: old", "X-Dropped: old"}
	merged := providerconfig.MergeHeaders(defaults, overrides)
	wantMerged := []string{"X-Default: keep", "X-Dropped: old", "X-Session: session={session_id}", "X-Override: new", "X-Token: token-value", "X-Literal: $cash"}
	if !reflect.DeepEqual(merged, wantMerged) {
		t.Fatalf("header overrides differ: got=%q want=%q", merged, wantMerged)
	}
	wantExpanded := []string{"X-Default: keep", "X-Dropped: old", "X-Session: session=abc-123", "X-Override: new", "X-Token: token-value", "X-Literal: $cash"}
	if got := providerconfig.ExpandHeaders(merged, "abc-123"); !reflect.DeepEqual(got, wantExpanded) {
		t.Fatalf("session placeholder expansion differs: got=%q want=%q", got, wantExpanded)
	}
}

func TestNullHeaderValueIsIgnoredRatherThanRemovingDefault(t *testing.T) {
	providers := providerconfig.New(baseconfig.New(nil))
	var warnings []string
	providers.SetWarningHandler(func(message string) { warnings = append(warnings, message) })
	configured := providers.HeadersFromObject(json.RawMessage(`{"x-opencode-session":null}`))
	merged := providerconfig.MergeHeaders([]string{"x-opencode-session: {session_id}"}, configured)
	want := []string{"x-opencode-session: {session_id}"}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("null should not remove the shipped session header: got=%q want=%q", merged, want)
	}
	if len(warnings) != 1 {
		t.Fatalf("null header value should be reported as invalid, warnings=%q", warnings)
	}
}

func TestProviderConfigurationWarningsMatchDocumentedMessages(t *testing.T) {
	store := baseconfig.New(baseconfig.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"providers":{"demo":{"cache_ttl":"2h","extra_body":{"model":"reserved","n":2},"extra_headers":{"Bad Name":"x","X-Number":7,"X-Unset":"$NOT_SET","X-Evil":"bad\r\ninjected"}}}}`)); err != nil {
		t.Fatal(err)
	}
	providers := providerconfig.New(store)
	var warnings []string
	providers.SetWarningHandler(func(message string) { warnings = append(warnings, message) })

	if got := providers.CacheTTL("providers.demo"); got != "1h" {
		t.Fatalf("invalid cache TTL = %q, want fallback 1h", got)
	}
	_ = providers.ExtraBody("providers.demo")
	_ = providers.ExtraHeaderTemplates("providers.demo")
	want := []string{
		"unknown cache_ttl '2h' (5m or 1h) — using 1h",
		"providers.demo.extra_body: 'model' is protocol-owned — ignoring it",
		"providers.demo.extra_body: 'n' is protocol-owned — ignoring it",
		"providers.demo.extra_headers: invalid header name 'Bad Name' — ignoring it",
		"providers.demo.extra_headers: header 'X-Number' needs a string value — ignoring it",
		"providers.demo.extra_headers: header 'X-Unset' dropped — NOT_SET is not set",
		"providers.demo.extra_headers: header 'X-Evil' needs a control-character-free value — ignoring it",
	}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("provider warnings = %q, want the documented diagnostics %q", warnings, want)
	}
	if err := store.LoadConfig([]byte(`{"providers":{"bad":{"extra_body":[]}}}`)); err != nil {
		t.Fatal(err)
	}
	_ = providers.ExtraBody("providers.bad")
	want = append(want, "providers.bad.extra_body must be a JSON object — ignoring it")
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("non-object body warning = %q, want %q", warnings[len(warnings)-1], want[len(want)-1])
	}
}

func TestProviderFieldInventory(t *testing.T) {
	field, ok := providerconfig.Field("api_key")
	if !ok || !field.Secret || field.Classes&providerconfig.FieldKeyed == 0 {
		t.Fatalf("API key inventory metadata was lost: %+v found=%t", field, ok)
	}
	if got := providerconfig.Fields(); len(got) != 23 || got[0].Name != "api" || got[len(got)-1].Name != "extra_headers" {
		t.Fatalf("provider field inventory order changed: count=%d fields=%+v", len(got), got)
	}
	if field, ok := providerconfig.Field("reasoning_required"); !ok || !field.AppliesTo(providerconfig.FieldOpenAIChat) {
		t.Fatalf("reasoning_required field metadata missing: %+v found=%t", field, ok)
	}
}

func TestCompatibleSettingsProjectProviderFieldInventory(t *testing.T) {
	blocks := []struct {
		prefix  string
		dialect providerconfig.FieldClass
	}{
		{prefix: "providers.openai-compatible.", dialect: providerconfig.FieldOpenAI},
		{prefix: "providers.anthropic-compatible.", dialect: providerconfig.FieldAnthropic},
	}
	fields := make(map[string]providerconfig.FieldMetadata)
	for _, field := range providerconfig.Fields() {
		fields[field.Name] = field
	}
	settings := baseconfig.DefaultSettings()
	for _, setting := range settings {
		for _, block := range blocks {
			if !strings.HasPrefix(setting.Key, block.prefix) {
				continue
			}
			leaf := strings.TrimPrefix(setting.Key, block.prefix)
			field, ok := fields[leaf]
			if !ok {
				t.Errorf("setting %q has no provider field metadata", setting.Key)
				continue
			}
			classes := block.dialect | providerconfig.FieldKeyed | providerconfig.FieldUnpinned
			if field.Classes&classes == 0 {
				t.Errorf("setting %q field classes = %b, want compatibility with %b", setting.Key, field.Classes, classes)
			}
			if field.Secret != setting.Secret {
				t.Errorf("setting %q secret metadata = %t, want %t", setting.Key, field.Secret, setting.Secret)
			}
		}
	}
}

func marshalBody(t *testing.T, body map[string]json.RawMessage) string {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

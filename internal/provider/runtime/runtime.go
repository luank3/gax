// Package runtime resolves provider definitions into endpoint and credential state.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/codex"
	providerconfig "github.com/luank3/gax/internal/provider/config"
	"github.com/luank3/gax/internal/provider/deepseek"
	mockprovider "github.com/luank3/gax/internal/provider/mock"
	"github.com/luank3/gax/internal/provider/openrouter"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/transport"
)

type Wire string

const (
	WireChat               Wire = "openai-completions"
	WireResponses          Wire = "openai-responses"
	WireAnthropic          Wire = "anthropic-messages"
	apiCatalog                  = "catalog"
	ModelMetadataWait           = 5 * time.Second
	availabilityProbeWait       = 2 * time.Second
	availabilityProbeLimit      = 1 << 20
)

func (wire Wire) Path() string {
	switch wire {
	case WireChat:
		return "/chat/completions"
	case WireResponses:
		return "/responses"
	case WireAnthropic:
		return "/messages"
	default:
		return ""
	}
}

type MetadataAPI string

const (
	MetadataOpenAI    MetadataAPI = "openai"
	MetadataAnthropic MetadataAPI = "anthropic"
)

type AvailabilityState string

const (
	AvailabilityReady                AvailabilityState = "ready"
	AvailabilityNeedsProbe           AvailabilityState = "needs_probe"
	AvailabilityUnconfigured         AvailabilityState = "unconfigured"
	AvailabilityMissingCredential    AvailabilityState = "missing_credential"
	AvailabilityAuthManagedElsewhere AvailabilityState = "auth_managed_elsewhere"
)

type Availability struct {
	State    AvailabilityState
	Reason   string
	ProbeURL string
}

func (availability Availability) Ready() bool { return availability.State == AvailabilityReady }

// ModelInfo contains one provider's currently listed model and any provider-specific
// capabilities understood by this runtime.
type ModelInfo struct {
	ID            string
	Description   string
	Aliases       []string
	StatusObject  bool
	Status        string
	ContextWindow int64
	MaxContext    int64
	MaxOutput     int64
	ImageInput    catalog.Support
	Tools         catalog.Support
	Efforts       []string
	EffortsKnown  bool
	Rates         *catalog.Entry
}

type modelListFetchError struct{ message string }

func (err modelListFetchError) Error() string { return err.message }

var effortLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

type Resolver struct {
	store            *config.Store
	registry         *registry.Registry
	catalog          *catalog.Catalog
	diagnosticWriter io.Writer
}

type Provider struct {
	Definition      *registry.Definition
	API             string
	Wire            Wire
	MetadataAPI     MetadataAPI
	BaseURL         string
	Endpoint        string
	DisplayName     string
	CatalogID       *string
	Version         string
	CacheTTL        string
	DefaultModel    string
	DefaultEffort   string
	ModelDiscovered bool
	ExtraBody       map[string]json.RawMessage
	Availability    Availability
	apiKey          secret
	modelCache      *listedModelCache
	authAccountID   string
	authEmail       string
	codexAuth       codex.Auth
	extraHeaders    []string
	warnings        []string
	wireRules       []wireRule
	catalogWires    bool
	modelCatalog    *catalog.Catalog
	store           *config.Store
	probe           *modelProbeState
	mock            *mockprovider.Provider
}

type modelProbeState struct {
	startMu  sync.Mutex
	mu       sync.Mutex
	job      *modelProbeJob
	deferred bool
	closed   bool
}

type modelProbeJob struct {
	model   string
	started time.Time
	done    chan struct{}
	cancel  context.CancelFunc
}

// RequestSettings contains the provider/model policy consumed by the wire body builders and
// stream parsers.
type RequestSettings struct {
	ChatCacheMarkers      bool
	AnthropicCacheMarkers bool
	CacheTTL              string
	SessionCacheKey       *string
	RequestCost           bool
	ReasoningField        *string
	ReasoningRequired     bool
	ReasoningFormat       string
	EffectiveEffort       *string
	CacheWriteIsOneHour   bool
	LengthHint            string
	MaxTokens             int
	ThinkingMode          string
	ThinkingBudget        int
	ShowReasoning         bool
	AllowEmptySignature   bool
}

type wireRule struct {
	pattern string
	wire    Wire
}

type secret string

func (secret) String() string   { return "[redacted]" }
func (secret) GoString() string { return "[redacted]" }
func (value secret) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[redacted]"))
}

func NewResolver(store *config.Store, providers *registry.Registry) *Resolver {
	if store == nil {
		store = config.New(config.DefaultSettings())
		store.SetEnvironment(nil)
	}
	if providers == nil {
		providers = registry.New(store)
	}
	return &Resolver{store: store, registry: providers, catalog: catalog.New(store)}
}

func (resolver *Resolver) SetCatalog(models *catalog.Catalog) {
	if resolver == nil {
		return
	}
	if models == nil {
		models = catalog.New(resolver.store)
	}
	resolver.catalog = models
}

// SetDiagnosticWriter routes provider diagnostics to the caller's stderr stream.
func (resolver *Resolver) SetDiagnosticWriter(writer io.Writer) {
	if resolver != nil {
		resolver.diagnosticWriter = writer
	}
}

func (resolver *Resolver) Catalog() *catalog.Catalog {
	if resolver == nil {
		return nil
	}
	return resolver.catalog
}

func (resolver *Resolver) Resolve(id string) (*Provider, error) {
	if resolver == nil || resolver.registry == nil {
		return nil, fmt.Errorf("provider registry is unavailable")
	}
	definition, ok := resolver.registry.Find(id)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	provider := &Provider{Definition: definition, store: resolver.store, modelCache: &listedModelCache{}, probe: &modelProbeState{}}
	prefix := "providers." + definition.ID
	providerConfig := providerconfig.New(resolver.store)
	providerConfig.SetWarningHandler(func(message string) { provider.warnings = append(provider.warnings, message) })

	if configured := configStringNonempty(resolver.store, prefix+".api"); definition.Pinned && configured != nil {
		provider.warnings = append(provider.warnings, fmt.Sprintf("provider '%s': api is pinned to %s — use model_apis for per-model protocols, or a custom provider", definition.ID, definition.API))
	}
	provider.API, provider.Wire = resolveWire(definition, providerConfig)
	if provider.Wire == "" {
		return nil, fmt.Errorf("provider %q: unsupported api %q", definition.ID, provider.API)
	}
	provider.catalogWires = strings.EqualFold(provider.API, apiCatalog) || wireRulesDeclared(resolver.store, definition.ID)
	provider.modelCatalog = resolver.catalog
	provider.MetadataAPI = resolveMetadataAPI(definition, provider.Wire, providerConfig, func(message string) {
		provider.warnings = append(provider.warnings, message)
	})
	if definition.Hooks&registry.HookConstruct == 0 {
		classes := providerconfig.FieldOpenAIChat
		switch provider.Wire {
		case WireResponses:
			classes = providerconfig.FieldOpenAIResponses
		case WireAnthropic:
			classes = providerconfig.FieldAnthropic
		}
		if provider.catalogWires {
			classes |= providerconfig.FieldOpenAI | providerconfig.FieldAnthropic
		}
		if definition.Hooks&registry.HookAuthSource == 0 {
			classes |= providerconfig.FieldKeyed
			if !definition.Pinned {
				classes |= providerconfig.FieldUnpinned
			}
		}
		if strings.Contains(definition.BaseURL, "{port}") {
			classes |= providerconfig.FieldPortTemplated
		}
		var metadataFields []string
		if provider.MetadataAPI == MetadataAnthropic {
			metadataFields = []string{"version"}
		}
		providerConfig.WarnUnusedFields(definition.ID, string(provider.Wire), classes, metadataFields)
	}
	provider.wireRules = resolveWireRules(resolver.store, definition.ID, func(message string) {
		provider.warnings = append(provider.warnings, message)
	})
	provider.DisplayName = resolver.registry.DisplayName(definition)
	provider.CatalogID = resolver.registry.CatalogID(definition)
	if provider.catalogWires && provider.CatalogID == nil && len(provider.wireRules) == 0 &&
		(resolver.catalog == nil || !resolver.catalog.RoutesModels(definition.ID)) {
		provider.warnings = append(provider.warnings, fmt.Sprintf("provider '%s': models route by catalog metadata, but catalog_id is not set", provider.DisplayName))
	}
	provider.Version = configuredOrDefault(providerConfig.String(prefix, "version"), "2023-06-01")
	provider.CacheTTL = providerConfig.CacheTTL(prefix)
	defaultHeaders := providerConfig.HeadersFromObject(definition.ExtraHeaders)
	configuredHeaders := providerConfig.ExtraHeaderTemplates(prefix)
	provider.extraHeaders = providerconfig.MergeHeaders(defaultHeaders, configuredHeaders)
	provider.ExtraBody = mergeExtraBody(definition.ExtraBody, providerConfig.ExtraBody(prefix))

	if definition.Hooks&registry.HookConstruct != 0 {
		provider.Availability = Availability{State: AvailabilityReady}
		if definition.ID == "mock" {
			provider.DefaultModel = "mock-model"
			provider.mock = mockprovider.New(providerConfig.String(prefix, "script"))
			provider.mock.SetDiagnosticWriter(resolver.diagnosticWriter)
		}
		return provider, nil
	}

	if configured := configStringNonempty(resolver.store, prefix+".base_url"); definition.Pinned && configured != nil {
		provider.warnings = append(provider.warnings, fmt.Sprintf("provider '%s': base_url is pinned to %s — use a custom provider for another endpoint", definition.ID, definition.BaseURL))
	}
	provider.BaseURL = resolveBaseURL(definition, resolver.store, prefix)
	if provider.BaseURL == "" {
		reason := definition.UnconfiguredReason
		if reason == "" {
			reason = "no base_url"
		}
		provider.Availability = Availability{State: AvailabilityUnconfigured, Reason: reason}
		return provider, nil
	}
	provider.Endpoint = provider.BaseURL + provider.Wire.Path()
	if definition.Hooks&registry.HookLoadDefaults != 0 && definition.ID == "codex" {
		home, _ := os.UserHomeDir()
		provider.DefaultModel, provider.DefaultEffort = codex.LoadDefaults(home)
	}

	if definition.Hooks&registry.HookAuthSource != 0 {
		if definition.ID == "codex" {
			home, homeErr := os.UserHomeDir()
			stateHome, _ := resolver.store.LookupEnv("XDG_STATE_HOME")
			if homeErr != nil && stateHome == "" {
				provider.Availability = Availability{State: AvailabilityMissingCredential, Reason: "could not determine the home directory"}
				return provider, nil
			}
			auth, authErr := codex.LoadAuth(home, stateHome)
			if authErr != nil {
				provider.Availability = Availability{State: AvailabilityMissingCredential, Reason: authErr.Error()}
				return provider, nil
			}
			provider.codexAuth = auth
			provider.apiKey = secret(auth.AccessToken)
			provider.authAccountID = auth.AccountID
			provider.authEmail = auth.Email
			provider.Availability = Availability{State: AvailabilityReady}
			return provider, nil
		}
		provider.Availability = Availability{State: AvailabilityAuthManagedElsewhere}
		return provider, nil
	}

	apiKeyEnv := definition.APIKeyEnv
	if !definition.Pinned {
		if configured := providerConfig.String(prefix, "api_key_env"); configured != nil && *configured != "" {
			apiKeyEnv = *configured
		}
	}
	inlineKey := providerConfig.String(prefix, "api_key")
	provider.apiKey = secret(providerConfig.APIKey(prefix, apiKeyEnv))
	if inlineKey != nil && *inlineKey != "" || apiKeyEnv != "" {
		if provider.apiKey == "" {
			reason := prefix + ".api_key not set"
			if apiKeyEnv != "" {
				reason = apiKeyEnv + " not set"
			}
			provider.Availability = Availability{State: AvailabilityMissingCredential, Reason: reason}
			return provider, nil
		}
		provider.Availability = Availability{State: AvailabilityReady}
		return provider, nil
	}
	if definition.Probe || definition.Hooks&registry.HookPrepareAvailability != 0 {
		provider.Availability = Availability{
			State:    AvailabilityNeedsProbe,
			Reason:   "server not reachable",
			ProbeURL: provider.BaseURL + "/models",
		}
		return provider, nil
	}
	provider.Availability = Availability{State: AvailabilityReady}
	return provider, nil
}

// MockProvider returns this resolved mock provider's captured script state, if it is mock.
func (provider *Provider) MockProvider() *mockprovider.Provider {
	if provider == nil {
		return nil
	}
	return provider.mock
}

func (provider *Provider) HasAPIKey() bool {
	if provider == nil {
		return false
	}
	if provider.Definition != nil && provider.Definition.ID == "codex" {
		return provider.codexAuth.AccessToken != ""
	}
	return provider.apiKey != ""
}

// SortModels reports whether model choices should use version-aware ordering. The global
// setting accepts auto, which inherits the provider-specific sort_models setting (on by default).
func (provider *Provider) SortModels() bool {
	providerDefault := provider.boolOr("sort_models", true)
	return provider.globalBoolOr("sort_models", providerDefault)
}

// ListedModelIDs returns a copy of the latest explicit provider model listing in its original
// order. Selected-model metadata probes update details but do not add completion candidates.
func (provider *Provider) ListedModelIDs() []string {
	if provider == nil || provider.modelCache == nil {
		return nil
	}
	provider.modelCache.mu.RLock()
	defer provider.modelCache.mu.RUnlock()
	return append([]string(nil), provider.modelCache.listedIDs...)
}

// Close cancels and joins the provider's background model probe. A closed provider cannot start
// another probe, matching upstream provider destruction and metadata release.
func (provider *Provider) Close() {
	if provider == nil || provider.probe == nil {
		return
	}
	probe := provider.probe
	probe.startMu.Lock()
	defer probe.startMu.Unlock()
	probe.mu.Lock()
	if probe.closed {
		probe.mu.Unlock()
		return
	}
	probe.closed = true
	job := probe.job
	if job == nil {
		probe.mu.Unlock()
		return
	}
	job.cancel()
	probe.mu.Unlock()
	<-job.done
	probe.mu.Lock()
	if probe.job == job {
		probe.job = nil
	}
	probe.mu.Unlock()
}

// ProbeAvailability checks providers whose readiness depends on a live local endpoint. HTTP
// status codes do not determine reachability; a server response, including a 404, is available.
func (provider *Provider) ProbeAvailability(ctx context.Context, httpClient *transport.Client) bool {
	if provider == nil {
		return false
	}
	if provider.Availability.Ready() {
		return true
	}
	if provider.Availability.State != AvailabilityNeedsProbe || provider.Availability.ProbeURL == "" {
		return false
	}
	if httpClient == nil {
		httpClient = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeContext, cancel := context.WithTimeout(ctx, availabilityProbeWait)
	defer cancel()
	_, status, err := httpClient.Get(probeContext, provider.Availability.ProbeURL,
		provider.MetadataHeaders(""), availabilityProbeWait, availabilityProbeLimit, nil)
	if err != nil && status == 0 {
		return false
	}
	provider.Availability = Availability{State: AvailabilityReady}
	return true
}

// PrefetchModelMetadata starts the shared catalog refresh only for providers with a catalog
// identity, matching the upstream model metadata lifecycle.
func (provider *Provider) PrefetchModelMetadata() {
	if provider == nil || provider.modelCatalog == nil || provider.CatalogID == nil {
		return
	}
	provider.modelCatalog.Prefetch()
}

// WaitModelMetadata starts a due catalog refresh and waits within a budget anchored at fetch
// start. A tick can abandon the wait while the refresh continues in the background.
func (provider *Provider) WaitModelMetadata(maxWait time.Duration, tick func() bool) bool {
	if provider == nil {
		return true
	}
	if provider.modelCatalog == nil || provider.CatalogID == nil {
		return provider.WaitModelProbe(maxWait, tick)
	}
	provider.PrefetchModelMetadata()
	if !provider.modelCatalog.Wait(maxWait, tick) {
		return false
	}
	return provider.WaitModelProbe(maxWait, tick)
}

// WaitModelProbe waits for the active model lookup within a budget anchored at its start.
func (provider *Provider) WaitModelProbe(maxWait time.Duration, tick func() bool) bool {
	return provider.waitModelProbe(maxWait, tick)
}

// StartModelProbe starts a selected-model metadata refresh asynchronously. Replacing an active
// target cancels and joins it first.
func (provider *Provider) StartModelProbe(ctx context.Context, model string, client *transport.Client) bool {
	if model == "" || !provider.CanProbeSelectedModel() || provider.probe == nil {
		return false
	}
	target := model
	provider.probe.startMu.Lock()
	defer provider.probe.startMu.Unlock()
	if cached, found := provider.listedModel(target); found && cached.ContextWindow > 0 {
		return false
	}
	provider.probe.mu.Lock()
	if provider.probe.closed {
		provider.probe.mu.Unlock()
		return false
	}
	if current := provider.probe.job; current != nil {
		if current.model == target && !modelProbeDone(current.done) {
			provider.probe.mu.Unlock()
			return true
		}
		current.cancel()
		provider.probe.mu.Unlock()
		<-current.done
	} else {
		provider.probe.mu.Unlock()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeContext, cancel := context.WithCancel(ctx)
	job := &modelProbeJob{model: target, started: time.Now(), done: make(chan struct{}), cancel: cancel}
	provider.probe.mu.Lock()
	provider.probe.job = job
	provider.probe.mu.Unlock()
	go func() {
		defer close(job.done)
		defer cancel()
		_, _ = provider.probeModel(probeContext, target, client, nil)
	}()
	return true
}

func (provider *Provider) waitModelProbe(maxWait time.Duration, tick func() bool) bool {
	if provider == nil {
		return true
	}
	if provider.probe == nil {
		return true
	}
	provider.probe.mu.Lock()
	job := provider.probe.job
	provider.probe.mu.Unlock()
	if job == nil || job.model == "" || modelProbeDone(job.done) {
		return true
	}
	if maxWait > 0 {
		maxWait -= time.Since(job.started)
	}
	if maxWait <= 0 {
		return modelProbeDone(job.done)
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	if tick != nil && tick() {
		return false
	}
	for {
		select {
		case <-job.done:
			return true
		case <-timer.C:
			return modelProbeDone(job.done)
		case <-ticker.C:
			if tick != nil && tick() {
				return false
			}
		}
	}
}

// RestartDeferredModelProbe retries a Codex metadata probe after request authentication has had
// its chance to refresh an access token that was too close to expiry for a background probe.
func (provider *Provider) RestartDeferredModelProbe(ctx context.Context, model string, client *transport.Client) bool {
	if provider == nil || provider.Definition == nil || provider.Definition.ID != "codex" || provider.probe == nil {
		return false
	}
	if codex.TokenExpiresWithin(provider.CodexAuth().AccessToken, time.Minute) {
		return false
	}
	provider.probe.mu.Lock()
	deferred := provider.probe.deferred
	provider.probe.deferred = false
	provider.probe.mu.Unlock()
	if !deferred {
		return false
	}
	return provider.StartModelProbe(ctx, model, client)
}

func modelProbeDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// ProbeModel fetches the selected model's provider metadata. An empty model does not issue a
// request. Lookup failures leave cached metadata available.
func (provider *Provider) ProbeModel(ctx context.Context, model string, client *transport.Client,
	tick func() bool) (ModelInfo, bool) {
	if model == "" || !provider.supportsModelProbe() {
		return ModelInfo{}, false
	}
	if model != "" && modelProbeReportsSelection(provider) {
		if cached, found := provider.listedModel(model); found && cached.ContextWindow > 0 {
			return cached, true
		}
	}
	if model != "" && !modelProbeReportsSelection(provider) {
		model = ""
	}
	return provider.probeModel(ctx, model, client, tick)
}

func (provider *Provider) probeModel(ctx context.Context, model string, client *transport.Client,
	tick func() bool) (ModelInfo, bool) {
	if !provider.supportsModelProbe() {
		return ModelInfo{}, false
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, timeout, maxResponseBytes, ok := modelProbeRequest(provider, model)
	if !ok {
		return ModelInfo{}, false
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, _, err := client.Get(probeContext, endpoint, provider.MetadataHeaders(""), timeout, maxResponseBytes, tick)
	if err != nil || !json.Valid(bytes.TrimSpace(data)) {
		return ModelInfo{}, false
	}
	info, found := parseModelProbeResponse(provider, data, model)
	if found {
		provider.storeProbedModel(info)
	}
	return info, found
}

// ListModels fetches the OpenAI-style /models envelope used by OpenRouter and compatible
// providers. It returns an empty list for data:null and applies any shipped model parser while
// retaining the exact wire model ID.
func (provider *Provider) ListModels(ctx context.Context, client *transport.Client,
	tick func() bool) ([]ModelInfo, error) {
	if provider == nil || provider.Definition == nil {
		return nil, fmt.Errorf("provider is unavailable")
	}
	if provider.Definition.ID == "codex" {
		return provider.listCodexModels(ctx, client, tick)
	}
	if provider.MetadataAPI == MetadataAnthropic {
		return provider.listAnthropicModels(ctx, client, tick)
	}
	if provider.MetadataAPI != MetadataOpenAI {
		return nil, fmt.Errorf("provider %q uses unsupported /models metadata API %q", provider.Definition.ID, provider.MetadataAPI)
	}
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("provider %q has no base URL", provider.Definition.ID)
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const timeout = 10 * time.Second
	const maxResponseBytes = 32 << 20
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := baseURL + "/models"
	data, status, err := client.Get(ctx, url, provider.MetadataHeaders(""), timeout, maxResponseBytes, tick)
	if err != nil {
		return nil, modelListFetchError{message: transport.FormatModelListError(providerDisplayName(provider), baseURL, provider.HasAPIKey(), status)}
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%s /models response is not valid JSON", providerDisplayName(provider))
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%s /models response is not valid JSON", providerDisplayName(provider))
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(trimmed, &envelope) != nil || envelope == nil {
		return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
	}
	dataField := bytes.TrimSpace(envelope["data"])
	if bytes.Equal(dataField, []byte("null")) {
		if provider.Definition.ID == "llamacpp" && provider.Definition.Hooks&registry.HookDiscover != 0 {
			return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
		}
		models := []ModelInfo{}
		provider.storeListedModels(models)
		return models, nil
	}
	if len(dataField) == 0 {
		return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
	}
	var entries []json.RawMessage
	if dataField[0] != '[' || json.Unmarshal(dataField, &entries) != nil || entries == nil {
		return nil, fmt.Errorf("%s /models response has no model list", providerDisplayName(provider))
	}
	if len(entries) == 0 {
		models := []ModelInfo{}
		provider.storeListedModels(models)
		return models, nil
	}
	models := make([]ModelInfo, 0, len(entries))
	for _, entry := range entries {
		var object map[string]json.RawMessage
		if json.Unmarshal(entry, &object) != nil || object == nil {
			continue
		}
		id := modelID(object["id"])
		if id == "" {
			continue
		}
		model := ModelInfo{ID: id, ImageInput: catalog.SupportUnknown, Tools: catalog.SupportUnknown}
		if provider.Definition.ID == "llamacpp" && provider.Definition.Hooks&registry.HookDiscover != 0 {
			model.Aliases, model.StatusObject, model.Status = parseLlamaModelDetails(entry)
			model.ContextWindow, model.ImageInput, model.Description = parseLlamaModelMetadata(entry)
		}
		if provider.Definition.Hooks&registry.HookParseModel != 0 {
			switch provider.Definition.ID {
			case "openrouter":
				parsed, err := parseOpenRouterModel(entry, id)
				if err != nil {
					return nil, fmt.Errorf("parse %s model %q: %w", providerDisplayName(provider), id, err)
				}
				model = parsed
			case "deepseek":
				parsed, err := deepseek.ParseModel(entry)
				if err != nil {
					return nil, fmt.Errorf("parse %s model %q: %w", providerDisplayName(provider), id, err)
				}
				model = ModelInfo{
					ID: id, Description: parsed.Description, ContextWindow: parsed.ContextWindow,
					MaxOutput: parsed.MaxOutput, ImageInput: parsed.ImageInput, Tools: catalog.SupportUnknown,
					Efforts: append([]string(nil), parsed.Efforts...), EffortsKnown: parsed.EffortsKnown,
				}
			}
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		if provider.Definition.ID == "llamacpp" && provider.Definition.Hooks&registry.HookDiscover != 0 {
			models := []ModelInfo{}
			provider.storeListedModels(models)
			return models, nil
		}
		return nil, fmt.Errorf("%s /models response contains no usable model ids", providerDisplayName(provider))
	}
	provider.storeListedModels(models)
	return models, nil
}

func parseOpenRouterModel(raw json.RawMessage, id string) (ModelInfo, error) {
	parsed, err := openrouter.ParseModel(raw)
	if err != nil {
		return ModelInfo{}, err
	}
	return ModelInfo{
		ID: id, Description: parsed.Description, ContextWindow: parsed.ContextWindow,
		MaxOutput: parsed.MaxOutput, ImageInput: parsed.ImageInput, Tools: parsed.Tools,
		Efforts: append([]string(nil), parsed.Efforts...), EffortsKnown: parsed.EffortsKnown,
		Rates: cloneCatalogEntry(parsed.Rates),
	}, nil
}

func providerDisplayName(provider *Provider) string {
	if provider.DisplayName != "" {
		return provider.DisplayName
	}
	if provider.Definition != nil {
		return provider.Definition.ID
	}
	return "provider"
}

func modelID(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '"' {
		return ""
	}
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return ""
	}
	return id
}

// RequestSettings resolves request-body policy from provider config, definition defaults, model
// metadata, and the selected effort.
func (provider *Provider) RequestSettings(model string, effort, sessionID *string) RequestSettings {
	if provider == nil || provider.Definition == nil {
		return RequestSettings{}
	}
	definition := provider.Definition
	entry := provider.modelEntry(model)
	settings := RequestSettings{
		CacheTTL:            provider.CacheTTL,
		RequestCost:         provider.boolOr("request_cost", definition.RequestCost),
		ReasoningFormat:     definition.ReasoningFormat,
		LengthHint:          definition.LengthHint,
		ThinkingBudget:      provider.intValue("thinking_budget"),
		ShowReasoning:       provider.globalBoolOr("show_reasoning", false),
		AllowEmptySignature: !definition.StrictSignatures,
		EffectiveEffort:     provider.ResolveEffort(model, effort),
	}
	if provider.WireForModel(model) == WireAnthropic {
		if configured := provider.stringValue("thinking_mode"); configured != nil && *configured != "" {
			if _, _, ok := parseThinkingSetting(*configured); !ok {
				provider.warnOnce(fmt.Sprintf("unknown thinking_mode '%s' (auto/prefer-adaptive/adaptive/budget/off) — using default", *configured))
			}
		}
	}
	if configured := provider.stringValue("reasoning_format"); configured != nil {
		switch strings.ToLower(*configured) {
		case "flat", "nested":
			settings.ReasoningFormat = strings.ToLower(*configured)
		}
	}

	if provider.boolOr("send_cache_key", definition.SendCacheKey) && sessionID != nil && *sessionID != "" {
		key := *sessionID
		settings.SessionCacheKey = &key
	}
	settings.ChatCacheMarkers = provider.resolveChatCache(entry)
	settings.AnthropicCacheMarkers = provider.boolOr("cache", true)
	settings.CacheWriteIsOneHour = settings.ChatCacheMarkers && settings.CacheTTL == "1h" && entry.CostCacheWrite1H >= 0

	field, pinned := provider.resolveReasoningField()
	settings.ReasoningRequired = provider.boolOr("reasoning_required", definition.ReasoningRequired)
	if !pinned {
		if entry.InterleavedField != "" {
			field = entry.InterleavedField
		} else if entry.InterleavedDeclared && !settings.ReasoningRequired {
			field = ""
		}
	}
	if settings.ReasoningRequired && field == "" {
		if pinned {
			provider.warnOnce(fmt.Sprintf("provider '%s': reasoning_roundtrip is off, so the reasoning the endpoint requires is not sent", provider.DisplayName))
		} else if provider.CatalogID == nil {
			provider.warnOnce(fmt.Sprintf("provider '%s': reasoning_required needs the reasoning field's name — set reasoning_roundtrip (e.g. reasoning_content) or catalog_id", provider.DisplayName))
		}
	}
	if field != "" {
		settings.ReasoningField = &field
	}

	settings.MaxTokens = provider.resolveMaxTokens(entry.MaxOutput)
	settings.ThinkingMode = provider.resolveThinkingMode(entry, settings.EffectiveEffort)
	return settings
}

func (provider *Provider) resolveChatCache(entry catalog.Entry) bool {
	withFalseFallback := provider.boolOr("cache", false)
	withTrueFallback := provider.boolOr("cache", true)
	if withFalseFallback == withTrueFallback {
		return withTrueFallback
	}
	switch strings.ToLower(provider.Definition.Cache) {
	case "auto":
		return catalog.CacheWriteReplacesInput(entry)
	case "on":
		return true
	default:
		return false
	}
}

func (provider *Provider) resolveReasoningField() (string, bool) {
	configured := provider.stringValue("reasoning_roundtrip")
	if configured != nil && *configured == "auto" {
		configured = nil
	}
	field := provider.Definition.ReasoningRoundtrip
	pinned := configured != nil
	if configured != nil {
		switch *configured {
		case "", "off", "0":
			field = ""
		case "on", "1":
			field = "reasoning_content"
		default:
			field = *configured
		}
	}
	return field, pinned
}

func (provider *Provider) resolveMaxTokens(modelLimit int64) int {
	configured := provider.intValue("max_tokens")
	userSet := provider.source("max_tokens") != "default"
	if userSet && configured > 0 {
		if modelLimit > 0 && int64(configured) > modelLimit {
			return intLimit(modelLimit)
		}
		return configured
	}
	if modelLimit > 0 {
		return intLimit(modelLimit)
	}
	if configured > 0 {
		return configured
	}
	return 32000
}

func (provider *Provider) resolveThinkingMode(entry catalog.Entry, effort *string) string {
	if effort != nil && *effort == "none" {
		return "off"
	}
	follow, mode, _ := parseThinkingSetting(provider.Definition.ThinkingMode)
	if configured := provider.stringValue("thinking_mode"); configured != nil && *configured != "" {
		if configuredFollow, configuredMode, ok := parseThinkingSetting(*configured); ok {
			follow, mode = configuredFollow, configuredMode
		}
	}
	if !follow {
		return mode
	}
	if entry.EffortsKnown {
		if len(entry.Efforts) > 0 {
			return "adaptive"
		}
		return "budget"
	}
	if effort != nil && *effort != "" {
		return "adaptive"
	}
	return mode
}

func parseThinkingSetting(value string) (followCatalog bool, mode string, ok bool) {
	followCatalog, mode, ok = true, "budget", true
	switch strings.ToLower(value) {
	case "", "auto":
	case "prefer-adaptive":
		mode = "adaptive"
	case "adaptive", "budget", "off":
		followCatalog = false
		mode = strings.ToLower(value)
	default:
		ok = false
	}
	return
}

func (provider *Provider) modelEntry(model string) catalog.Entry {
	entry, _ := provider.ModelMetadata(model)
	return entry
}

// ModelMetadata resolves configured and catalog metadata using the provider's catalog identity.
func (provider *Provider) ModelMetadata(model string) (catalog.Entry, bool) {
	if provider == nil || provider.Definition == nil {
		return unknownModelEntry(), false
	}
	entry := unknownModelEntry()
	found := false
	configured := unknownModelEntry()
	configuredFound := false
	if provider.modelCatalog != nil {
		catalogID := ""
		if provider.CatalogID != nil {
			catalogID = *provider.CatalogID
		}
		entry, found = provider.modelCatalog.Lookup(provider.Definition.ID, catalogID, model)
		configured, configuredFound = provider.modelCatalog.LookupConfig(provider.Definition.ID, catalogID, model)
	}
	listed, listedFound := provider.listedModel(model)
	if listedFound {
		if listed.ContextWindow > 0 && (!configuredFound || configured.ContextWindow <= 0) {
			entry.ContextWindow = listed.ContextWindow
		}
		if listed.MaxOutput > 0 && (!configuredFound || configured.MaxOutput <= 0) {
			entry.MaxOutput = listed.MaxOutput
		}
		if listed.ImageInput != catalog.SupportUnknown && (!configuredFound || configured.ImageInput == catalog.SupportUnknown) {
			entry.ImageInput = listed.ImageInput
		}
		if listed.EffortsKnown && (!configuredFound || !configured.EffortsKnown) {
			entry.Efforts = append([]string(nil), listed.Efforts...)
			entry.EffortsKnown = true
		}
		mergeListedRates(&entry, configured, listed.Rates)
	}
	return entry, found || listedFound
}

func (provider *Provider) storeListedModels(models []ModelInfo) {
	if provider == nil {
		return
	}
	listed := make(map[string]ModelInfo, len(models))
	listedIDs := make([]string, 0, len(models))
	for _, model := range models {
		if _, found := listed[model.ID]; !found {
			listedIDs = append(listedIDs, model.ID)
		}
		model.Efforts = append([]string(nil), model.Efforts...)
		model.Rates = cloneCatalogEntry(model.Rates)
		listed[model.ID] = model
	}
	if provider.modelCache == nil {
		return
	}
	provider.modelCache.mu.Lock()
	provider.modelCache.models = listed
	provider.modelCache.listedIDs = listedIDs
	provider.modelCache.mu.Unlock()
}

func (provider *Provider) storeProbedModel(model ModelInfo) {
	if provider == nil || provider.modelCache == nil || model.ID == "" {
		return
	}
	provider.modelCache.mu.Lock()
	if provider.modelCache.models == nil {
		provider.modelCache.models = make(map[string]ModelInfo)
	}
	if previous, found := provider.modelCache.models[model.ID]; found {
		model = mergeProbedModel(model, previous)
	}
	model.Efforts = append([]string(nil), model.Efforts...)
	model.Rates = cloneCatalogEntry(model.Rates)
	provider.modelCache.models[model.ID] = model
	provider.modelCache.mu.Unlock()
}

func mergeProbedModel(probed, previous ModelInfo) ModelInfo {
	if probed.Description == "" {
		probed.Description = previous.Description
	}
	if probed.ContextWindow <= 0 {
		probed.ContextWindow = previous.ContextWindow
	}
	if probed.MaxContext <= 0 {
		probed.MaxContext = previous.MaxContext
	}
	if probed.MaxOutput <= 0 {
		probed.MaxOutput = previous.MaxOutput
	}
	if probed.ImageInput == catalog.SupportUnknown {
		probed.ImageInput = previous.ImageInput
	}
	if probed.Tools == catalog.SupportUnknown {
		probed.Tools = previous.Tools
	}
	if !probed.EffortsKnown && previous.EffortsKnown {
		probed.Efforts = append([]string(nil), previous.Efforts...)
		probed.EffortsKnown = true
	}
	if probed.Rates == nil {
		probed.Rates = cloneCatalogEntry(previous.Rates)
	} else if previous.Rates != nil {
		merged := cloneCatalogEntry(probed.Rates)
		mergeRateField := func(dst *float64, src float64) {
			if *dst < 0 && src >= 0 {
				*dst = src
			}
		}
		mergeRateField(&merged.CostInput, previous.Rates.CostInput)
		mergeRateField(&merged.CostOutput, previous.Rates.CostOutput)
		mergeRateField(&merged.CostCacheRead, previous.Rates.CostCacheRead)
		mergeRateField(&merged.CostCacheWrite, previous.Rates.CostCacheWrite)
		mergeRateField(&merged.CostCacheWrite1H, previous.Rates.CostCacheWrite1H)
		if !merged.TiersDeclared && previous.Rates.TiersDeclared {
			merged.Tiers = append([]catalog.Tier(nil), previous.Rates.Tiers...)
			merged.TiersDeclared = true
		}
		probed.Rates = merged
	}
	return probed
}

func (provider *Provider) listedModel(model string) (ModelInfo, bool) {
	if provider == nil || model == "" {
		return ModelInfo{}, false
	}
	if provider.modelCache == nil {
		return ModelInfo{}, false
	}
	provider.modelCache.mu.RLock()
	listed, found := provider.modelCache.models[model]
	provider.modelCache.mu.RUnlock()
	listed.Efforts = append([]string(nil), listed.Efforts...)
	listed.Rates = cloneCatalogEntry(listed.Rates)
	return listed, found
}

func mergeListedRates(entry *catalog.Entry, configured catalog.Entry, rates *catalog.Entry) {
	if entry == nil || rates == nil {
		return
	}
	if configured.CostInput < 0 && rates.CostInput >= 0 {
		entry.CostInput = rates.CostInput
	}
	if configured.CostOutput < 0 && rates.CostOutput >= 0 {
		entry.CostOutput = rates.CostOutput
	}
	if configured.CostCacheRead < 0 && rates.CostCacheRead >= 0 {
		entry.CostCacheRead = rates.CostCacheRead
	}
	if configured.CostCacheWrite < 0 && rates.CostCacheWrite >= 0 {
		entry.CostCacheWrite = rates.CostCacheWrite
	}
	if configured.CostCacheWrite1H < 0 && rates.CostCacheWrite1H >= 0 {
		entry.CostCacheWrite1H = rates.CostCacheWrite1H
	}
	if configured.TiersDeclared {
		return
	}
	if rates.TiersDeclared {
		entry.Tiers = append([]catalog.Tier(nil), rates.Tiers...)
		entry.TiersDeclared = true
	} else if rates.CostInput >= 0 || rates.CostOutput >= 0 {
		// A provider's live prices and snapshot tiers can come from different billing sources.
		entry.Tiers = nil
		entry.TiersDeclared = false
	}
}

func cloneCatalogEntry(entry *catalog.Entry) *catalog.Entry {
	if entry == nil {
		return nil
	}
	copy := *entry
	copy.Efforts = append([]string(nil), entry.Efforts...)
	copy.Tiers = append([]catalog.Tier(nil), entry.Tiers...)
	return &copy
}

// SupportsEffortLevels reports whether this provider exposes a categorical effort ladder before
// model-specific metadata is applied.
func (provider *Provider) SupportsEffortLevels() bool {
	if provider == nil || provider.Definition == nil || provider.Definition.NoEfforts {
		return false
	}
	if provider.Wire == WireAnthropic && len(provider.wireRules) == 0 && !provider.catalogWires {
		follow, mode, _ := parseThinkingSetting(provider.Definition.ThinkingMode)
		if configured := provider.stringValue("thinking_mode"); configured != nil && *configured != "" {
			if configuredFollow, configuredMode, ok := parseThinkingSetting(*configured); ok {
				follow, mode = configuredFollow, configuredMode
			}
		}
		if !follow && mode != "adaptive" {
			return false
		}
	}
	return true
}

// EffortLevels returns the reasoning effort values the provider can express for model. Catalog
// metadata narrows the shared provider ladder; explicit configuration and provider reports may
// extend it with values the provider can send.
func (provider *Provider) EffortLevels(model string) []string {
	if !provider.SupportsEffortLevels() {
		return nil
	}
	entry, _ := provider.ModelMetadata(model)
	if !entry.EffortsKnown {
		return filterEffortLevels(provider, model, effortLadder)
	}
	if len(entry.Efforts) == 0 {
		return nil
	}
	accepted := make(map[string]bool, len(entry.Efforts))
	for _, level := range entry.Efforts {
		accepted[level] = true
	}
	levels := make([]string, 0, len(entry.Efforts))
	for _, level := range effortLadder {
		if accepted[level] {
			levels = append(levels, level)
			delete(accepted, level)
		}
	}
	authoritative := false
	if provider.modelCatalog != nil {
		catalogID := ""
		if provider.CatalogID != nil {
			catalogID = *provider.CatalogID
		}
		configured, _ := provider.modelCatalog.LookupConfig(provider.Definition.ID, catalogID, model)
		authoritative = configured.EffortsKnown
	}
	if !authoritative {
		if reported, found := provider.listedModel(model); found && reported.EffortsKnown {
			authoritative = true
		}
	}
	if authoritative {
		for _, level := range entry.Efforts {
			if accepted[level] {
				levels = append(levels, level)
			}
		}
	}
	return filterEffortLevels(provider, model, levels)
}

// ResolveEffort applies model-supported levels to the requested effort, falling back to the
// provider default when the request is unsupported. A nil or empty request means provider default
// and explicit omission, respectively.
func (provider *Provider) ResolveEffort(model string, requested *string) *string {
	levels := provider.EffortLevels(model)
	if len(levels) == 0 {
		return nil
	}
	if requested != nil && *requested == "" {
		return nil
	}
	value := requested
	if value == nil && provider.DefaultEffort != "" {
		value = &provider.DefaultEffort
	}
	if value == nil || *value == "" {
		return nil
	}
	if resolved := clampEffort(levels, *value); resolved != nil {
		return resolved
	}
	if requested != nil && provider.DefaultEffort != "" {
		return clampEffort(levels, provider.DefaultEffort)
	}
	return nil
}

func clampEffort(levels []string, value string) *string {
	for _, level := range levels {
		if value == level {
			copy := value
			return &copy
		}
	}
	requestedRank := effortRank(value)
	if requestedRank < 0 {
		return nil
	}
	lower, upper := "", ""
	lowerRank, upperRank := -1, len(effortLadder)
	for _, level := range levels {
		rank := effortRank(level)
		if rank < 0 {
			continue
		}
		if rank <= requestedRank && rank > lowerRank {
			lower, lowerRank = level, rank
		}
		if rank > requestedRank && rank < upperRank {
			upper, upperRank = level, rank
		}
	}
	if lower != "" {
		return &lower
	}
	if upper != "" {
		return &upper
	}
	return nil
}

func effortRank(level string) int {
	for index, candidate := range effortLadder {
		if level == candidate {
			return index
		}
	}
	return -1
}

func filterEffortLevels(provider *Provider, model string, levels []string) []string {
	if provider.WireForModel(model) != WireAnthropic {
		return append([]string(nil), levels...)
	}
	filtered := make([]string, 0, len(levels))
	for _, level := range levels {
		if level != "minimal" {
			filtered = append(filtered, level)
		}
	}
	return filtered
}

func (provider *Provider) ImageInputForModel(model string) int {
	entry, found := provider.ModelMetadata(model)
	if !found {
		return int(catalog.SupportUnknown)
	}
	return int(entry.ImageInput)
}

func (provider *Provider) ContextWindowForModel(model string) int64 {
	entry, _ := provider.ModelMetadata(model)
	return entry.ContextWindow
}

func unknownModelEntry() catalog.Entry {
	return catalog.Entry{
		CostInput: -1, CostOutput: -1, CostCacheRead: -1, CostCacheWrite: -1,
		CostCacheWrite1H: -1, ImageInput: catalog.SupportUnknown,
	}
}

func (provider *Provider) stringValue(field string) *string {
	if provider == nil || provider.store == nil || provider.Definition == nil {
		return nil
	}
	return provider.store.String("providers." + provider.Definition.ID + "." + field)
}

func (provider *Provider) boolOr(field string, fallback bool) bool {
	if provider == nil || provider.store == nil || provider.Definition == nil {
		return fallback
	}
	return provider.store.BoolOr("providers."+provider.Definition.ID+"."+field, fallback)
}

func (provider *Provider) intValue(field string) int {
	if provider == nil || provider.store == nil || provider.Definition == nil {
		return 0
	}
	return provider.store.Int("providers." + provider.Definition.ID + "." + field)
}

func (provider *Provider) source(field string) string {
	if provider == nil || provider.store == nil || provider.Definition == nil {
		return "default"
	}
	return provider.store.Source("providers." + provider.Definition.ID + "." + field)
}

func (provider *Provider) globalBoolOr(key string, fallback bool) bool {
	if provider == nil || provider.store == nil {
		return fallback
	}
	return provider.store.BoolOr(key, fallback)
}

func (provider *Provider) warnOnce(message string) {
	for _, warning := range provider.warnings {
		if warning == message {
			return
		}
	}
	provider.warnings = append(provider.warnings, message)
}

func intLimit(value int64) int {
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

// WireForModel resolves the first matching providers.<id>.model_apis glob, falling back to the
// provider's default wire when no rule matches.
func (provider *Provider) WireForModel(model string) Wire {
	if provider == nil {
		return ""
	}
	for _, rule := range provider.wireRules {
		if globMatch(rule.pattern, model) {
			return rule.wire
		}
	}
	if provider.catalogWires && provider.modelCatalog != nil && provider.Definition != nil {
		catalogID := ""
		if provider.CatalogID != nil {
			catalogID = *provider.CatalogID
		}
		if entry, found := provider.modelCatalog.Lookup(provider.Definition.ID, catalogID, model); found && entry.API != "" {
			if entry.API == "unsupported" {
				return ""
			}
			return parseWire(entry.API)
		}
	}
	return provider.Wire
}

// EndpointForModel combines the resolved model wire with the provider's normalized base URL.
func (provider *Provider) EndpointForModel(model string) string {
	if provider == nil {
		return ""
	}
	wire := provider.WireForModel(model)
	if path := wire.Path(); path != "" && provider.BaseURL != "" {
		return provider.BaseURL + path
	}
	return ""
}

// RequestReadsModelMetadata reports whether sending a request can benefit from waiting for the
// model catalog or selected-model probe. Pure Responses providers consult neither; protocol
// routing, Chat cache/reasoning policy, and Anthropic thinking policy can use metadata.
func (provider *Provider) RequestReadsModelMetadata() bool {
	return provider != nil && (provider.catalogWires || len(provider.wireRules) > 0 || provider.Wire != WireResponses)
}

// Credentials returns the resolved API key for request authentication. Keep it out of diagnostics.
func (provider *Provider) Credentials() string {
	if provider == nil {
		return ""
	}
	if provider.Definition != nil && provider.Definition.ID == "codex" {
		return provider.codexAuth.AccessToken
	}
	return string(provider.apiKey)
}

// AuthAccountID returns the account identifier supplied by an external auth source.
func (provider *Provider) AuthAccountID() string {
	if provider == nil {
		return ""
	}
	return provider.authAccountID
}

// AuthEmail returns the optional account email supplied by an external auth source.
func (provider *Provider) AuthEmail() string {
	if provider == nil {
		return ""
	}
	return provider.authEmail
}

// CodexAuth returns the resolved Codex credential details for its provider-specific operations.
func (provider *Provider) CodexAuth() *codex.Auth {
	if provider == nil {
		return nil
	}
	return &provider.codexAuth
}

// ConfiguredHeaders expands user-configured provider headers without adding runtime credentials.
// A request with no conversation (recording off, or a metadata/usage call) falls back to the
// process session id rather than expanding "{session_id}" to nothing.
func (provider *Provider) ConfiguredHeaders(sessionID string) []string {
	if provider == nil {
		return nil
	}
	if sessionID == "" {
		sessionID = ProcessSessionID()
	}
	return providerconfig.ExpandHeaders(provider.extraHeaders, sessionID)
}

func (provider *Provider) Warnings() []string {
	if provider == nil {
		return nil
	}
	return append([]string(nil), provider.warnings...)
}

// Headers builds request headers for the selected wire, expanding the stable session placeholder.
func (provider *Provider) Headers(wire Wire, sessionID string, streaming bool) []string {
	if provider == nil {
		return nil
	}
	headers := make([]string, 0, len(provider.extraHeaders)+4)
	if wire == WireAnthropic {
		if credentials := provider.Credentials(); credentials != "" {
			headers = append(headers, "x-api-key: "+credentials)
		}
		version := provider.Version
		if version == "" {
			version = "2023-06-01"
		}
		headers = append(headers, "anthropic-version: "+version)
	} else if credentials := provider.Credentials(); credentials != "" {
		headers = append(headers, "Authorization: Bearer "+credentials)
	}
	if provider.Definition != nil && provider.Definition.ID == "codex" && provider.authAccountID != "" {
		headers = append(headers, "chatgpt-account-id: "+provider.authAccountID)
	}
	if streaming {
		headers = append(headers, "Accept: text/event-stream", "Content-Type: application/json")
	}
	return append(headers, provider.ConfiguredHeaders(sessionID)...)
}

// MetadataHeaders selects auth headers for the configured /models dialect.
func (provider *Provider) MetadataHeaders(sessionID string) []string {
	if provider == nil {
		return nil
	}
	if provider.MetadataAPI == MetadataAnthropic {
		return provider.Headers(WireAnthropic, sessionID, false)
	}
	return provider.Headers(WireChat, sessionID, false)
}

func (provider *Provider) String() string {
	if provider == nil || provider.Definition == nil {
		return "Provider(<nil>)"
	}
	return fmt.Sprintf("Provider(%s)", provider.Definition.ID)
}

func (provider Provider) Format(state fmt.State, _ rune) {
	id := ""
	if provider.Definition != nil {
		id = provider.Definition.ID
	}
	_, _ = fmt.Fprintf(state, "Provider{ID:%q, Wire:%q, Availability:%q, HasAPIKey:%t}", id, provider.Wire, provider.Availability.State, provider.HasAPIKey())
}

func resolveWire(definition *registry.Definition, providerConfig *providerconfig.Config) (string, Wire) {
	api := definition.API
	if api == "" {
		api = string(WireChat)
	}
	configured := providerConfig.String("providers."+definition.ID, "api")
	if configured != nil && *configured != "" && !definition.Pinned {
		api = *configured
	}
	if strings.EqualFold(api, apiCatalog) {
		return api, WireChat
	}
	return api, parseWire(api)
}

func parseWire(api string) Wire {
	switch {
	case strings.EqualFold(api, string(WireChat)), strings.EqualFold(api, "chat"):
		return WireChat
	case strings.EqualFold(api, string(WireResponses)), strings.EqualFold(api, "responses"):
		return WireResponses
	case strings.EqualFold(api, string(WireAnthropic)):
		return WireAnthropic
	default:
		return ""
	}
}

func resolveWireRules(store *config.Store, providerID string, warn func(string)) []wireRule {
	key := "providers." + providerID + ".model_apis"
	if store == nil {
		return nil
	}
	data := bytes.TrimSpace(store.JSONNode(key))
	if len(data) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
		return nil
	}
	type member struct {
		pattern string
		value   json.RawMessage
	}
	members := make([]member, 0)
	indices := make(map[string]int)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
			return nil
		}
		pattern, ok := token.(string)
		if !ok {
			warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
			return nil
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
			return nil
		}
		if index, exists := indices[pattern]; exists {
			members[index].value = append(json.RawMessage(nil), value...)
		} else {
			indices[pattern] = len(members)
			members = append(members, member{pattern: pattern, value: append(json.RawMessage(nil), value...)})
		}
	}
	if _, err := decoder.Token(); err != nil {
		warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		warn(fmt.Sprintf("%s must be a JSON object of pattern/dialect members — ignoring it", key))
		return nil
	}
	rules := make([]wireRule, 0, len(members))
	for _, member := range members {
		trimmed := bytes.TrimSpace(member.value)
		var dialect string
		if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &dialect) != nil {
			dialect = ""
		}
		wire := parseWire(dialect)
		if wire == "" {
			warn(fmt.Sprintf("%s: '%s' needs a dialect value (openai-completions, openai-responses, anthropic-messages) — ignoring it", key, member.pattern))
			continue
		}
		rules = append(rules, wireRule{pattern: member.pattern, wire: wire})
	}
	return rules
}

func wireRulesDeclared(store *config.Store, providerID string) bool {
	if store == nil {
		return false
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(store.JSONNode("providers."+providerID+".model_apis"), &members) != nil || members == nil {
		return false
	}
	return len(members) > 0
}

// globMatch follows fnmatch-style wildcards: '*' and '?' can match slashes, and bracket
// expressions may contain literals, ranges, and leading '!' or '^' negation.
func globMatch(pattern, value string) bool {
	p, v := []rune(pattern), []rune(value)
	type position struct{ pattern, value int }
	memo := make(map[position]bool)
	done := make(map[position]bool)
	var match func(int, int) bool
	match = func(pi, vi int) bool {
		pos := position{pattern: pi, value: vi}
		if done[pos] {
			return memo[pos]
		}
		done[pos] = true
		result := false
		switch {
		case pi == len(p):
			result = vi == len(v)
		case p[pi] == '*':
			for pi < len(p) && p[pi] == '*' {
				pi++
			}
			result = match(pi, vi)
			for next := vi; !result && next < len(v); next++ {
				result = match(pi, next+1)
			}
		case vi < len(v) && p[pi] == '?':
			result = match(pi+1, vi+1)
		case vi < len(v) && p[pi] == '[':
			if end, matched, ok := globClass(p, pi, v[vi]); ok {
				result = matched && match(end, vi+1)
			} else {
				result = p[pi] == v[vi] && match(pi+1, vi+1)
			}
		case vi < len(v) && p[pi] == '\\' && pi+1 < len(p):
			result = p[pi+1] == v[vi] && match(pi+2, vi+1)
		case vi < len(v):
			result = p[pi] == v[vi] && match(pi+1, vi+1)
		}
		memo[pos] = result
		return result
	}
	return match(0, 0)
}

func globClass(pattern []rune, start int, value rune) (end int, matched, valid bool) {
	index := start + 1
	negated := false
	if index < len(pattern) && (pattern[index] == '!' || pattern[index] == '^') {
		negated = true
		index++
	}
	first := index
	if index < len(pattern) && pattern[index] == ']' {
		index++
	}
	for index < len(pattern) && pattern[index] != ']' {
		index++
	}
	if index == len(pattern) || index == first {
		return 0, false, false
	}
	end = index + 1
	classEnd := index
	index = first
	if index < classEnd && pattern[index] == ']' {
		if value == ']' {
			matched = true
		}
		index++
	}
	for index < classEnd {
		low := pattern[index]
		index++
		if index+1 < classEnd && pattern[index] == '-' {
			high := pattern[index+1]
			index += 2
			if low <= value && value <= high {
				matched = true
			}
		} else if low == value {
			matched = true
		}
	}
	if negated {
		matched = !matched
	}
	return end, matched, true
}

func resolveMetadataAPI(definition *registry.Definition, wire Wire, providerConfig *providerconfig.Config, warn func(string)) MetadataAPI {
	defaultAPI := MetadataOpenAI
	if definition.MetadataAPI != "" {
		if strings.EqualFold(definition.MetadataAPI, string(MetadataAnthropic)) {
			defaultAPI = MetadataAnthropic
		} else if strings.EqualFold(definition.MetadataAPI, string(MetadataOpenAI)) {
			defaultAPI = MetadataOpenAI
		}
	} else if wire == WireAnthropic {
		defaultAPI = MetadataAnthropic
	}
	configured := providerConfig.String("providers."+definition.ID, "metadata_api")
	if configured == nil || *configured == "" || strings.EqualFold(*configured, "auto") {
		return defaultAPI
	}
	switch {
	case strings.EqualFold(*configured, string(MetadataOpenAI)):
		return MetadataOpenAI
	case strings.EqualFold(*configured, string(MetadataAnthropic)):
		return MetadataAnthropic
	default:
		warn(fmt.Sprintf("unknown metadata_api '%s' (openai or anthropic) — using default", *configured))
		return defaultAPI
	}
}

func resolveBaseURL(definition *registry.Definition, store *config.Store, prefix string) string {
	base := definition.BaseURL
	if !definition.Pinned {
		configured := false
		if store != nil {
			if configuredBase := store.StringNonempty(prefix + ".base_url"); configuredBase != nil {
				base = *configuredBase
				configured = true
			}
		}
		if !configured && strings.Contains(base, "{port}") {
			port := definition.Port
			if store != nil {
				configuredPort := store.Int(prefix + ".port")
				if configuredPort >= 1 && configuredPort <= 65535 {
					port = configuredPort
				}
			}
			if port < 1 || port > 65535 {
				return ""
			}
			base = strings.ReplaceAll(base, "{port}", fmt.Sprint(port))
		}
	}
	return strings.TrimRight(base, "/")
}

func configuredOrDefault(value *string, fallback string) string {
	if value != nil && *value != "" {
		return *value
	}
	return fallback
}

func mergeExtraBody(defaults json.RawMessage, configured map[string]json.RawMessage) map[string]json.RawMessage {
	if len(defaults) == 0 && len(configured) == 0 {
		return nil
	}
	base := make(map[string]json.RawMessage)
	if len(defaults) > 0 {
		_ = json.Unmarshal(defaults, &base)
	}
	return mergeJSONObjects(base, configured)
}

func mergeJSONObjects(base, override map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(base)+len(override))
	for key, value := range base {
		merged[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range override {
		var baseObject, overrideObject map[string]json.RawMessage
		baseRaw := merged[key]
		baseTrimmed := bytes.TrimSpace(baseRaw)
		overrideTrimmed := bytes.TrimSpace(value)
		if len(baseTrimmed) > 0 && baseTrimmed[0] == '{' && len(overrideTrimmed) > 0 && overrideTrimmed[0] == '{' && json.Unmarshal(baseRaw, &baseObject) == nil && json.Unmarshal(value, &overrideObject) == nil && baseObject != nil && overrideObject != nil {
			encoded, err := json.Marshal(mergeJSONObjects(baseObject, overrideObject))
			if err == nil {
				merged[key] = encoded
				continue
			}
		}
		merged[key] = append(json.RawMessage(nil), value...)
	}
	return merged
}

func configStringNonempty(store *config.Store, key string) *string {
	if store == nil {
		return nil
	}
	return store.StringNonempty(key)
}

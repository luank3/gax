// Package client dispatches resolved provider requests to the matching HTTP wire.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/anthropic"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/openai"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

// BodyOptions carries the policy already resolved for each wire. Provider-specific options are
// kept separate because the same setting can have different meaning across protocols.
type BodyOptions struct {
	Chat      openai.BodyOptions
	Responses openai.BodyOptions
	Anthropic anthropic.BodyOptions
}

type StreamOptions struct {
	Body                *BodyOptions
	RetryPolicy         transport.RetryPolicy
	IdleTimeout         time.Duration
	LengthHint          string
	CacheWriteIsOneHour bool
}

type Request struct {
	Wire     runtime.Wire
	Endpoint string
	Headers  []string
	Body     []byte
}

// Format omits request bodies and redacts every header value so diagnostic formatting cannot
// disclose credentials, custom header secrets, or conversation content.
func (request Request) Format(state fmt.State, _ rune) {
	headers := make([]string, 0, len(request.Headers))
	for _, line := range request.Headers {
		name, _, ok := strings.Cut(line, ":")
		if ok {
			headers = append(headers, strings.TrimSpace(name)+": [redacted]")
		} else {
			headers = append(headers, "[invalid header]")
		}
	}
	_, _ = fmt.Fprintf(state, "Request{Wire:%q Endpoint:%q Headers:%q BodyBytes:%d}", request.Wire, request.Endpoint, headers, len(request.Body))
}

// Prepare resolves provider/model defaults, then builds the request selected by model-specific
// wire rules. Headers omit generic SSE headers because Stream adds them once at the transport.
func Prepare(provider *runtime.Provider, model string, context coreprovider.Context) (Request, error) {
	if provider == nil {
		return Request{}, fmt.Errorf("provider is unavailable")
	}
	settings := provider.RequestSettings(model, context.Effort, context.SessionID)
	context.Effort = settings.EffectiveEffort
	return PrepareWithOptions(provider, model, context, bodyOptionsFromSettings(settings))
}

// PrepareWithOptions builds a request with explicit wire policy overrides. Resolved provider
// extra_body still applies after the wire has built its protocol-owned fields.
func PrepareWithOptions(provider *runtime.Provider, model string, context coreprovider.Context, options BodyOptions) (Request, error) {
	if provider == nil || provider.Definition == nil {
		return Request{}, fmt.Errorf("provider is unavailable")
	}
	wire := provider.WireForModel(model)
	if wire == "" {
		return Request{}, fmt.Errorf("model %q requires a protocol this client does not support", model)
	}
	endpoint := provider.EndpointForModel(model)
	if endpoint == "" {
		return Request{}, fmt.Errorf("provider %q has no request endpoint for model %q", provider.Definition.ID, model)
	}
	extraBody, err := marshalExtraBody(provider.ExtraBody)
	if err != nil {
		return Request{}, fmt.Errorf("provider %q extra_body: %w", provider.Definition.ID, err)
	}
	sessionID := ""
	if context.SessionID != nil {
		sessionID = *context.SessionID
	}
	var body []byte
	switch wire {
	case runtime.WireChat:
		bodyOptions := options.Chat
		if len(extraBody) > 0 {
			bodyOptions.ExtraBody = extraBody
		}
		built, err := openai.BuildBody(context, provider.Definition.ID, model, bodyOptions)
		if err != nil {
			return Request{}, fmt.Errorf("build Chat Completions request: %w", err)
		}
		body, err = json.Marshal(built)
		if err != nil {
			return Request{}, fmt.Errorf("encode Chat Completions request: %w", err)
		}
	case runtime.WireResponses:
		bodyOptions := options.Responses
		if len(extraBody) > 0 {
			bodyOptions.ExtraBody = extraBody
		}
		built, err := openai.BuildResponsesBody(context, provider.Definition.ID, model, bodyOptions)
		if err != nil {
			return Request{}, fmt.Errorf("build Responses request: %w", err)
		}
		body, err = json.Marshal(built)
		if err != nil {
			return Request{}, fmt.Errorf("encode Responses request: %w", err)
		}
	case runtime.WireAnthropic:
		bodyOptions := options.Anthropic
		if len(extraBody) > 0 {
			bodyOptions.ExtraBody = extraBody
		}
		built, err := anthropic.BuildBody(context, provider.Definition.ID, model, bodyOptions)
		if err != nil {
			return Request{}, fmt.Errorf("build Anthropic Messages request: %w", err)
		}
		body, err = json.Marshal(built)
		if err != nil {
			return Request{}, fmt.Errorf("encode Anthropic Messages request: %w", err)
		}
	default:
		return Request{}, fmt.Errorf("provider %q selected unsupported wire %q", provider.Definition.ID, wire)
	}
	return Request{
		Wire: wire, Endpoint: endpoint,
		Headers: provider.Headers(wire, sessionID, false),
		Body:    body,
	}, nil
}

// Stream prepares a model-specific request and delegates to its protocol parser and the shared
// transport retry loop.
func Stream(ctx context.Context, httpClient *transport.Client, provider *runtime.Provider, model string,
	requestContext coreprovider.Context, options StreamOptions, tick func() bool,
	callback func(conversation.Event)) (transport.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if provider != nil && provider.Definition != nil && provider.Definition.ID == "mock" {
		mockProvider := provider.MockProvider()
		if mockProvider == nil {
			return transport.Response{}, fmt.Errorf("mock provider is unavailable")
		}
		return mockProvider.Stream(ctx, requestContext, tick, callback)
	}
	metadataTick := tick
	if done := ctx.Done(); done != nil {
		metadataTick = func() bool {
			select {
			case <-done:
				return true
			default:
				return tick != nil && tick()
			}
		}
	}
	if provider != nil {
		provider.PrefetchModelMetadata()
		if provider.RequestReadsModelMetadata() {
			provider.WaitModelMetadata(runtime.ModelMetadataWait, metadataTick)
		}
	}
	if provider != nil && provider.Definition != nil && provider.Definition.ID == "codex" {
		codex.PrepareRequestAuth(ctx, provider.CodexAuth(), httpClient, tick)
		provider.RestartDeferredModelProbe(ctx, model, httpClient)
	}
	var request Request
	var err error
	if options.Body == nil {
		if provider == nil {
			return transport.Response{}, fmt.Errorf("provider is unavailable")
		}
		settings := provider.RequestSettings(model, requestContext.Effort, requestContext.SessionID)
		requestContext.Effort = settings.EffectiveEffort
		if options.LengthHint == "" {
			options.LengthHint = settings.LengthHint
		}
		options.CacheWriteIsOneHour = settings.CacheWriteIsOneHour
		request, err = PrepareWithOptions(provider, model, requestContext, bodyOptionsFromSettings(settings))
	} else {
		request, err = PrepareWithOptions(provider, model, requestContext, *options.Body)
	}
	if err != nil {
		return transport.Response{}, err
	}
	var recoverUnauthorized func() ([]string, bool, string)
	if provider.Definition.ID == "codex" {
		auth := provider.CodexAuth()
		recoverUnauthorized = func() ([]string, bool, string) {
			retry, _, message := codex.RecoverUnauthorized(ctx, auth, httpClient, tick)
			return provider.Headers(request.Wire, sessionID(requestContext), false), retry, message
		}
	}
	switch request.Wire {
	case runtime.WireChat:
		return openai.Stream(ctx, httpClient, request.Endpoint, request.Headers, request.Body,
			openai.StreamOptions{
				RetryPolicy: options.RetryPolicy, IdleTimeout: options.IdleTimeout,
				LengthHint: options.LengthHint, CacheWriteIsOneHour: options.CacheWriteIsOneHour,
				RecoverUnauthorized: recoverUnauthorized,
			}, tick, callback)
	case runtime.WireResponses:
		return openai.StreamResponses(ctx, httpClient, request.Endpoint, request.Headers, request.Body,
			openai.StreamOptions{
				RetryPolicy: options.RetryPolicy, IdleTimeout: options.IdleTimeout,
				LengthHint: options.LengthHint, CacheWriteIsOneHour: options.CacheWriteIsOneHour,
				RecoverUnauthorized: recoverUnauthorized,
			}, tick, callback)
	case runtime.WireAnthropic:
		return anthropic.Stream(ctx, httpClient, request.Endpoint, request.Headers, request.Body,
			anthropic.StreamOptions{RetryPolicy: options.RetryPolicy, IdleTimeout: options.IdleTimeout}, tick, callback)
	default:
		return transport.Response{}, fmt.Errorf("provider selected unsupported wire %q", request.Wire)
	}
}

func sessionID(context coreprovider.Context) string {
	if context.SessionID == nil {
		return ""
	}
	return *context.SessionID
}

func bodyOptionsFromSettings(settings runtime.RequestSettings) BodyOptions {
	thinkingMode, ok := anthropic.ParseThinkingMode(settings.ThinkingMode)
	if !ok {
		thinkingMode = anthropic.ThinkingBudget
	}
	return BodyOptions{
		Chat: openai.BodyOptions{
			CacheMarkers: settings.ChatCacheMarkers, CacheTTL: settings.CacheTTL,
			SessionCacheKey: settings.SessionCacheKey, RequestCost: settings.RequestCost,
			ReasoningField:    settings.ReasoningField,
			ReasoningRequired: settings.ReasoningRequired,
			ReasoningFormat:   openai.ParseReasoningFormat(settings.ReasoningFormat, openai.ReasoningFlat),
		},
		Responses: openai.BodyOptions{SessionCacheKey: settings.SessionCacheKey},
		Anthropic: anthropic.BodyOptions{
			CacheMarkers: settings.AnthropicCacheMarkers, CacheTTL: settings.CacheTTL,
			MaxTokens: settings.MaxTokens, ThinkingMode: thinkingMode, ThinkingBudget: settings.ThinkingBudget,
			ShowReasoning: settings.ShowReasoning, AllowEmptySignature: settings.AllowEmptySignature,
		},
	}
}

func marshalExtraBody(extra map[string]json.RawMessage) (json.RawMessage, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

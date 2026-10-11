package openai

import (
	"context"
	"time"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

type StreamOptions struct {
	RetryPolicy         transport.RetryPolicy
	IdleTimeout         time.Duration
	LengthHint          string
	CacheWriteIsOneHour bool
	RecoverUnauthorized func() (headers []string, retry bool, message string)
}

// Stream sends an already-serialized Chat Completions request and translates its SSE response.
func Stream(ctx context.Context, client *transport.Client, endpoint string, headers []string, body []byte,
	opts StreamOptions, tick func() bool, callback func(conversation.Event)) (transport.Response, error) {
	return coreprovider.StreamSSE(ctx, client, endpoint, headers, body,
		coreprovider.SSEOptions{RetryPolicy: opts.RetryPolicy, IdleTimeout: opts.IdleTimeout, RecoverUnauthorized: opts.RecoverUnauthorized}, tick, callback,
		func(emit func(conversation.Event)) coreprovider.StreamParser {
			return NewChatEvents(func(event conversation.Event) bool {
				emit(event)
				return true
			}, opts.LengthHint, opts.CacheWriteIsOneHour)
		})
}

// StreamResponses sends a Responses API request using the same shared retry transport.
func StreamResponses(ctx context.Context, client *transport.Client, endpoint string, headers []string, body []byte,
	opts StreamOptions, tick func() bool, callback func(conversation.Event)) (transport.Response, error) {
	return coreprovider.StreamSSE(ctx, client, endpoint, headers, body,
		coreprovider.SSEOptions{RetryPolicy: opts.RetryPolicy, IdleTimeout: opts.IdleTimeout, RecoverUnauthorized: opts.RecoverUnauthorized}, tick, callback,
		func(emit func(conversation.Event)) coreprovider.StreamParser {
			return NewResponsesEvents(emit)
		})
}

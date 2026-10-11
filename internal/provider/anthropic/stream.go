package anthropic

import (
	"context"
	"time"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

type StreamOptions struct {
	RetryPolicy transport.RetryPolicy
	IdleTimeout time.Duration
}

// Stream sends an already-serialized Anthropic Messages request and translates its SSE response.
func Stream(ctx context.Context, client *transport.Client, endpoint string, headers []string, body []byte,
	opts StreamOptions, tick func() bool, callback func(conversation.Event)) (transport.Response, error) {
	return coreprovider.StreamSSE(ctx, client, endpoint, headers, body,
		coreprovider.SSEOptions{RetryPolicy: opts.RetryPolicy, IdleTimeout: opts.IdleTimeout}, tick, callback,
		func(emit func(conversation.Event)) coreprovider.StreamParser {
			return NewEvents(emit)
		})
}

package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/transport"
)

type StreamParser interface {
	Feed(string)
	Finalize()
	Complete() bool
	UsageSnapshot() *conversation.Usage
	ResponseSnapshot() *conversation.StreamResponse
}

type SSEOptions struct {
	RetryPolicy         transport.RetryPolicy
	IdleTimeout         time.Duration
	RecoverUnauthorized func() (headers []string, retry bool, message string)
}

// StreamSSE runs a provider parser over an SSE POST, retrying transient failures and incomplete
// 2xx streams while keeping wire-specific parsing outside the shared transport loop.
func StreamSSE(ctx context.Context, client *transport.Client, endpoint string, headers []string, body []byte,
	opts SSEOptions, tick func() bool, callback func(conversation.Event),
	newParser func(func(conversation.Event)) StreamParser) (transport.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	policy := opts.RetryPolicy
	if policy.MaxAttempts <= 0 {
		policy = transport.DefaultRetryPolicy(4, time.Second, 10*time.Minute)
	}
	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = policy.IdleTimeout
	}
	headers = append([]string{"Accept: text/event-stream", "Content-Type: application/json"}, headers...)
	if client == nil {
		client = transport.DefaultClient
	}

	var response transport.Response
	var requestErr error
	authRetried := false
	unauthorizedMessage := ""
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		parser := newParser(func(event conversation.Event) {
			if callback != nil {
				callback(event)
			}
		})
		response, requestErr = client.SSEPost(ctx, endpoint, headers, body, idleTimeout, func(event transport.SSEEvent) bool {
			parser.Feed(event.Data)
			// Completion classifies the response after the stream ends. Providers can send
			// usage or other trailing metadata after the finish event, so keep reading the SSE body.
			return true
		}, tick)
		if response.Cancelled || errors.Is(requestErr, transport.ErrCancelled) {
			return response, transport.ErrCancelled
		}

		complete := parser.Complete()
		status2xx := response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
		incomplete := status2xx && !complete
		if response.StatusCode == http.StatusUnauthorized && opts.RecoverUnauthorized != nil && !authRetried {
			authRetried = true
			authHeaders, retry, message := opts.RecoverUnauthorized()
			unauthorizedMessage = message
			if retry {
				headers = append([]string{"Accept: text/event-stream", "Content-Type: application/json"}, authHeaders...)
				attempt--
				continue
			}
		}
		shouldRetry := incomplete || transport.ShouldRetry(response.StatusCode, response.ErrorBody)
		if shouldRetry && attempt+1 < policy.MaxAttempts {
			delay := response.RetryAfter
			if delay <= 0 {
				delay = transport.Delay(policy, attempt)
			}
			if callback != nil {
				callback(conversation.Event{
					Kind:        conversation.EventRetry,
					Usage:       parser.UsageSnapshot(),
					HTTPStatus:  response.StatusCode,
					Attempt:     attempt + 1,
					MaxAttempts: policy.MaxAttempts,
					DelayMS:     delay.Milliseconds(),
				})
			}
			if transport.SleepWithTick(ctx, delay, tick) {
				response.Cancelled = true
				return response, transport.ErrCancelled
			}
			continue
		}

		if status2xx && (requestErr == nil || complete) {
			parser.Finalize()
		} else if callback != nil {
			var usage *conversation.Usage
			var responseInfo *conversation.StreamResponse
			if status2xx && incomplete {
				usage = parser.UsageSnapshot()
				responseInfo = parser.ResponseSnapshot()
			}
			errorMessage := transport.FormatAPIError(response.StatusCode, response.ErrorBody)
			if response.StatusCode == http.StatusUnauthorized && unauthorizedMessage != "" {
				errorMessage = unauthorizedMessage
			}
			callback(conversation.Event{
				Kind:         conversation.EventError,
				ErrorMessage: errorMessage,
				HTTPStatus:   response.StatusCode,
				Usage:        usage,
				Response:     responseInfo,
			})
		}
		return response, requestErr
	}
	return response, requestErr
}

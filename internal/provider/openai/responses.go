// Package openai implements the OpenAI Responses and Chat Completions wire protocols.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

// Config configures an OpenAI Responses or Chat Completions client.
type Config struct {
	ProviderID  string
	BaseURL     string
	APIKey      string
	HTTP        *transport.Client
	IdleTimeout time.Duration
	Headers     map[string]string
	RefreshAuth func(ctx context.Context) (apiKey string, headers map[string]string, err error)
}

// ResponsesClient implements provider.Client over the OpenAI Responses API.
type ResponsesClient struct {
	cfg Config
}

// NewResponsesClient returns a Responses API client.
func NewResponsesClient(cfg Config) *ResponsesClient {
	return &ResponsesClient{cfg: cfg}
}

func (c *ResponsesClient) ID() string { return c.cfg.ProviderID }

func (c *ResponsesClient) Stream(ctx context.Context, req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	bodyBytes, err := buildResponsesBody(c.cfg.ProviderID, req)
	if err != nil {
		return err
	}
	buildHeaders := func(apiKey string, extra map[string]string) http.Header {
		hdr := make(http.Header)
		hdr.Set("Content-Type", "application/json")
		hdr.Set("Accept", "text/event-stream")
		if apiKey != "" {
			hdr.Set("Authorization", "Bearer "+apiKey)
		}
		for k, v := range extra {
			hdr.Set(k, v)
		}
		provider.ApplyExtraHeaders(hdr, req.Options.ExtraHeaders, req.Options.SessionID)
		return hdr
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/responses"
	hdr := buildHeaders(c.cfg.APIKey, c.cfg.Headers)
	resp, err := c.cfg.HTTP.DoStream(ctx, http.MethodPost, url, hdr, bodyBytes, c.cfg.IdleTimeout, func(attempt int, delay time.Duration, reason string) {
		emit(provider.StreamEvent{Kind: provider.EventRetry, RetryAttempt: attempt, RetryDelay: delay, RetryReason: reason})
	})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.cfg.RefreshAuth != nil {
		if newKey, newHdrs, rerr := c.cfg.RefreshAuth(ctx); rerr == nil && newKey != "" {
			_ = resp.Body.Close()
			c.cfg.APIKey = newKey
			for k, v := range newHdrs {
				if c.cfg.Headers == nil {
					c.cfg.Headers = map[string]string{}
				}
				c.cfg.Headers[k] = v
			}
			hdr = buildHeaders(c.cfg.APIKey, c.cfg.Headers)
			resp, err = c.cfg.HTTP.DoStream(ctx, http.MethodPost, url, hdr, bodyBytes, c.cfg.IdleTimeout, nil)
			if err != nil {
				return err
			}
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		msg := extractAPIError(raw, resp.StatusCode)
		if provider.IsOverflowError(msg, req.Options.OverflowPhrases) {
			return &provider.OverflowError{Message: msg}
		}
		return fmt.Errorf("%s", msg)
	}

	return parseResponsesSSE(resp.Body, emit, req.Options.OverflowPhrases)
}

func buildResponsesBody(providerID string, req provider.StreamRequest) ([]byte, error) {
	input := make([]any, 0, len(req.Items))
	for _, it := range req.Items {
		switch it.Kind {
		case core.KindUser:
			if len(it.Images) > 0 {
				parts := []any{map[string]any{"type": "input_text", "text": it.Text}}
				for _, img := range it.Images {
					parts = append(parts, map[string]any{
						"type":      "input_image",
						"image_url": "data:" + img.MediaType + ";base64," + img.Data,
					})
				}
				input = append(input, map[string]any{"role": "user", "content": parts})
			} else {
				input = append(input, map[string]any{"role": "user", "content": it.Text})
			}
		case core.KindReasoning:
			if it.ReasoningJSON != "" {
				var rawItem any
				if json.Unmarshal([]byte(it.ReasoningJSON), &rawItem) == nil {
					input = append(input, rawItem)
				}
			}
		case core.KindAssistant:
			if it.Text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": it.Text})
			}
		case core.KindToolCall:
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   it.CallID,
				"name":      it.ToolName,
				"arguments": it.Arguments,
			})
		case core.KindToolResult:
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": it.CallID,
				"output":  it.Output,
			})
		}
	}

	body := map[string]any{
		"model":  req.Options.Model,
		"input":  input,
		"stream": true,
	}
	if providerID == "codex" {
		body["store"] = false
		body["instructions"] = req.SystemPrompt
		body["text"] = map[string]any{"verbosity": "low"}
	} else if req.SystemPrompt != "" {
		body["instructions"] = req.SystemPrompt
	}
	if req.Options.Effort != "" {
		body["reasoning"] = map[string]any{"effort": req.Options.Effort}
	}
	if req.Options.SendCacheKey && req.Options.SessionID != "" {
		body["prompt_cache_key"] = req.Options.SessionID
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(provider.BuildParametersSchema(t)),
			})
		}
		body["tools"] = tools
	}
	provider.MergeExtraBody(body, req.Options.ExtraBody, map[string]bool{
		"model": true, "input": true, "instructions": true, "tools": true, "stream": true,
	})
	return json.Marshal(body)
}

func parseResponsesSSE(r io.Reader, emit func(provider.StreamEvent), overflowPhrases []string) error {
	calls := make(map[string]struct {
		callID string
		name   string
		args   strings.Builder
	})
	var incompleteLength bool
	var usage *core.Usage

	err := transport.ParseSSE(r, func(ev transport.SSEEvent) error {
		if ev.Data == "" || ev.Data == "[DONE]" {
			return nil
		}
		var msg struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			ItemID string `json:"item_id"`
			Item   struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
				Args   string `json:"arguments"`
			} `json:"item"`
			Response struct {
				ID                string `json:"id"`
				Model             string `json:"model"`
				IncompleteDetails struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Usage struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
					InputDetails struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &msg); err != nil {
			return nil
		}
		switch msg.Type {
		case "response.output_text.delta":
			if msg.Delta != "" {
				emit(provider.StreamEvent{Kind: provider.EventText, Text: msg.Delta})
			}
		case "response.reasoning_summary_text.delta":
			if msg.Delta != "" {
				emit(provider.StreamEvent{Kind: provider.EventReasoning, Text: msg.Delta})
			}
		case "response.output_item.added":
			if msg.Item.Type == "function_call" {
				calls[msg.Item.ID] = struct {
					callID string
					name   string
					args   strings.Builder
				}{callID: msg.Item.CallID, name: msg.Item.Name}
				emit(provider.StreamEvent{Kind: provider.EventToolCallStart, CallID: msg.Item.CallID, ToolName: msg.Item.Name})
			}
		case "response.function_call_arguments.delta":
			if c, ok := calls[msg.ItemID]; ok {
				c.args.WriteString(msg.Delta)
				calls[msg.ItemID] = c
				emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, CallID: c.callID, ArgumentsDelta: msg.Delta})
			}
		case "response.output_item.done":
			if msg.Item.Type == "function_call" {
				args := msg.Item.Args
				if c, ok := calls[msg.Item.ID]; ok {
					if args == "" {
						args = c.args.String()
					}
					delete(calls, msg.Item.ID)
				}
				emit(provider.StreamEvent{
					Kind:      provider.EventToolCallDone,
					CallID:    msg.Item.CallID,
					ToolName:  msg.Item.Name,
					Arguments: args,
				})
			}
		case "response.incomplete":
			if msg.Response.IncompleteDetails.Reason == "max_output_tokens" {
				incompleteLength = true
			}
		case "response.completed":
			usage = &core.Usage{
				Input:       msg.Response.Usage.InputTokens,
				Output:      msg.Response.Usage.OutputTokens,
				Cached:      msg.Response.Usage.InputDetails.CachedTokens,
				InTokens:    max(0, msg.Response.Usage.InputTokens-msg.Response.Usage.InputDetails.CachedTokens),
				ServedModel: msg.Response.Model,
				ResponseID:  msg.Response.ID,
			}
		case "error", "response.failed":
			errMsg := msg.Error.Message
			if errMsg == "" {
				errMsg = "provider stream failed"
			}
			if provider.IsOverflowError(errMsg, overflowPhrases) {
				return &provider.OverflowError{Message: errMsg}
			}
			return fmt.Errorf("%s", errMsg)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if usage != nil {
		emit(provider.StreamEvent{Kind: provider.EventUsage, Usage: usage})
	}
	emit(provider.StreamEvent{Kind: provider.EventDone, IncompleteLength: incompleteLength})
	return nil
}

func extractAPIError(raw []byte, status int) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.Error.Message != "" {
			return fmt.Sprintf("HTTP %d: %s", status, body.Error.Message)
		}
		if body.Message != "" {
			return fmt.Sprintf("HTTP %d: %s", status, body.Message)
		}
	}
	return fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(raw)))
}

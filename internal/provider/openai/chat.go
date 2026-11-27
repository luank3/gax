package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

// ChatClient implements provider.Client over OpenAI Chat Completions.
type ChatClient struct {
	cfg Config
}

// NewChatClient returns a Chat Completions API client.
func NewChatClient(cfg Config) *ChatClient {
	return &ChatClient{cfg: cfg}
}

func (c *ChatClient) ID() string { return c.cfg.ProviderID }

func (c *ChatClient) Stream(ctx context.Context, req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	bodyBytes, err := buildChatBody(req)
	if err != nil {
		return err
	}
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		hdr.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.cfg.Headers {
		hdr.Set(k, v)
	}
	provider.ApplyExtraHeaders(hdr, req.Options.ExtraHeaders, req.Options.SessionID)

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	resp, err := c.cfg.HTTP.DoStream(ctx, http.MethodPost, url, hdr, bodyBytes, c.cfg.IdleTimeout, func(attempt int, delay time.Duration, reason string) {
		emit(provider.StreamEvent{Kind: provider.EventRetry, RetryAttempt: attempt, RetryDelay: delay, RetryReason: reason})
	})
	if err != nil {
		return err
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
	return parseChatSSE(resp.Body, emit, req.Options.OverflowPhrases)
}

func buildChatBody(req provider.StreamRequest) ([]byte, error) {
	msgs := make([]map[string]any, 0, len(req.Items)+1)
	if req.SystemPrompt != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.SystemPrompt})
	}

	var pendingAssistant map[string]any
	var pendingCalls []map[string]any
	flushAssistant := func() {
		if pendingAssistant == nil && len(pendingCalls) == 0 {
			return
		}
		if pendingAssistant == nil {
			pendingAssistant = map[string]any{"role": "assistant", "content": ""}
		}
		if len(pendingCalls) > 0 {
			pendingAssistant["tool_calls"] = pendingCalls
		}
		if req.Options.ReasoningRequired && req.Options.ReasoningRoundtrip != "off" {
			field := req.Options.ReasoningRoundtrip
			if field == "" || field == "auto" {
				field = "reasoning_content"
			}
			if _, ok := pendingAssistant[field]; !ok {
				pendingAssistant[field] = ""
			}
		}
		msgs = append(msgs, pendingAssistant)
		pendingAssistant = nil
		pendingCalls = nil
	}

	for _, it := range req.Items {
		switch it.Kind {
		case core.KindUser:
			flushAssistant()
			msgs = append(msgs, map[string]any{"role": "user", "content": it.Text})
		case core.KindReasoning:
			if req.Options.ReasoningRoundtrip == "off" {
				continue
			}
			if pendingAssistant == nil {
				pendingAssistant = map[string]any{"role": "assistant", "content": ""}
			}
			field := req.Options.ReasoningRoundtrip
			if field == "" || field == "auto" {
				field = it.ReasoningField
				if field == "" {
					field = "reasoning_content"
				}
			}
			if it.ReasoningText != "" {
				pendingAssistant[field] = it.ReasoningText
			}
		case core.KindAssistant:
			if pendingAssistant == nil {
				pendingAssistant = map[string]any{"role": "assistant", "content": it.Text}
			} else {
				pendingAssistant["content"] = it.Text
			}
		case core.KindToolCall:
			if pendingAssistant == nil {
				pendingAssistant = map[string]any{"role": "assistant", "content": ""}
			}
			pendingCalls = append(pendingCalls, map[string]any{
				"id":   it.CallID,
				"type": "function",
				"function": map[string]any{
					"name":      it.ToolName,
					"arguments": it.Arguments,
				},
			})
		case core.KindToolResult:
			flushAssistant()
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": it.CallID,
				"content":      it.Output,
			})
		case core.KindTurnBoundary:
			flushAssistant()
		}
	}
	flushAssistant()

	body := map[string]any{
		"model":          req.Options.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.Options.Effort != "" {
		if req.Options.ReasoningFormat == "nested" {
			body["reasoning"] = map[string]any{"effort": req.Options.Effort}
		} else {
			body["reasoning_effort"] = req.Options.Effort
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  json.RawMessage(provider.BuildParametersSchema(t)),
				},
			})
		}
		body["tools"] = tools
	}
	provider.MergeExtraBody(body, req.Options.ExtraBody, map[string]bool{
		"model": true, "messages": true, "tools": true, "stream": true,
	})
	return json.Marshal(body)
}

func parseChatSSE(r io.Reader, emit func(provider.StreamEvent), overflowPhrases []string) error {
	type callAccum struct {
		id   string
		name string
		args strings.Builder
	}
	calls := make(map[int]*callAccum)
	var incompleteLength bool
	var usage *core.Usage

	err := transport.ParseSSE(r, func(ev transport.SSEEvent) error {
		if ev.Data == "" || ev.Data == "[DONE]" {
			return nil
		}
		var chunk struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				PromptDetails    struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			return nil
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			if provider.IsOverflowError(chunk.Error.Message, overflowPhrases) {
				return &provider.OverflowError{Message: chunk.Error.Message}
			}
			return fmt.Errorf("%s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = &core.Usage{
				Input:       chunk.Usage.PromptTokens,
				Output:      chunk.Usage.CompletionTokens,
				Cached:      chunk.Usage.PromptDetails.CachedTokens,
				InTokens:    max(0, chunk.Usage.PromptTokens-chunk.Usage.PromptDetails.CachedTokens),
				ServedModel: chunk.Model,
				ResponseID:  chunk.ID,
			}
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		ch := chunk.Choices[0]
		if ch.Delta.ReasoningContent != "" {
			emit(provider.StreamEvent{Kind: provider.EventReasoning, Text: ch.Delta.ReasoningContent, ReasoningField: "reasoning_content"})
		} else if ch.Delta.Reasoning != "" {
			emit(provider.StreamEvent{Kind: provider.EventReasoning, Text: ch.Delta.Reasoning, ReasoningField: "reasoning"})
		}
		if ch.Delta.Content != "" {
			emit(provider.StreamEvent{Kind: provider.EventText, Text: ch.Delta.Content})
		}
		for _, tc := range ch.Delta.ToolCalls {
			acc, ok := calls[tc.Index]
			if !ok {
				acc = &callAccum{id: tc.ID, name: tc.Function.Name}
				calls[tc.Index] = acc
				emit(provider.StreamEvent{Kind: provider.EventToolCallStart, CallID: acc.id, ToolName: acc.name})
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.args.WriteString(tc.Function.Arguments)
				emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, CallID: acc.id, ArgumentsDelta: tc.Function.Arguments})
			}
		}
		switch ch.FinishReason {
		case "length":
			incompleteLength = true
		case "error", "network_error":
			return fmt.Errorf("upstream stream ended with finish_reason=%s", ch.FinishReason)
		}
		return nil
	})
	if err != nil {
		return err
	}

	indices := make([]int, 0, len(calls))
	for idx := range calls {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		acc := calls[idx]
		emit(provider.StreamEvent{
			Kind:      provider.EventToolCallDone,
			CallID:    acc.id,
			ToolName:  acc.name,
			Arguments: acc.args.String(),
		})
	}
	if usage != nil {
		emit(provider.StreamEvent{Kind: provider.EventUsage, Usage: usage})
	}
	emit(provider.StreamEvent{Kind: provider.EventDone, IncompleteLength: incompleteLength})
	return nil
}

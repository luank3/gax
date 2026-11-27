// Package anthropic implements the Anthropic Messages wire protocol.
package anthropic

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

// Config configures an Anthropic Messages client.
type Config struct {
	ProviderID  string
	BaseURL     string
	APIKey      string
	Version     string
	HTTP        *transport.Client
	IdleTimeout time.Duration
	Headers     map[string]string
}

// Client implements provider.Client over Anthropic Messages.
type Client struct {
	cfg Config
}

// NewClient returns an Anthropic Messages client.
func NewClient(cfg Config) *Client {
	if cfg.Version == "" {
		cfg.Version = "2023-06-01"
	}
	return &Client{cfg: cfg}
}

func (c *Client) ID() string { return c.cfg.ProviderID }

func (c *Client) Stream(ctx context.Context, req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	bodyBytes, err := buildBody(req)
	if err != nil {
		return err
	}
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "text/event-stream")
	hdr.Set("anthropic-version", c.cfg.Version)
	if c.cfg.APIKey != "" {
		hdr.Set("x-api-key", c.cfg.APIKey)
	}
	for k, v := range c.cfg.Headers {
		hdr.Set(k, v)
	}
	provider.ApplyExtraHeaders(hdr, req.Options.ExtraHeaders, req.Options.SessionID)

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/messages"
	resp, err := c.cfg.HTTP.DoStream(ctx, http.MethodPost, url, hdr, bodyBytes, c.cfg.IdleTimeout, func(attempt int, delay time.Duration, reason string) {
		emit(provider.StreamEvent{Kind: provider.EventRetry, RetryAttempt: attempt, RetryDelay: delay, RetryReason: reason})
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		if provider.IsOverflowError(msg, req.Options.OverflowPhrases) {
			return &provider.OverflowError{Message: msg}
		}
		return fmt.Errorf("%s", msg)
	}
	return parseSSE(resp.Body, emit, req.Options.OverflowPhrases)
}

func buildBody(req provider.StreamRequest) ([]byte, error) {
	maxTokens := req.Options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 32000
	}

	msgs := make([]map[string]any, 0, len(req.Items))
	appendRoleBlock := func(role string, block any) {
		if len(msgs) > 0 && msgs[len(msgs)-1]["role"] == role {
			blocks := msgs[len(msgs)-1]["content"].([]any)
			msgs[len(msgs)-1]["content"] = append(blocks, block)
			return
		}
		msgs = append(msgs, map[string]any{
			"role":    role,
			"content": []any{block},
		})
	}

	for _, it := range req.Items {
		switch it.Kind {
		case core.KindUser:
			if it.Text != "" {
				appendRoleBlock("user", map[string]any{"type": "text", "text": it.Text})
			}
			for _, img := range it.Images {
				appendRoleBlock("user", map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": img.MediaType,
						"data":       img.Data,
					},
				})
			}
		case core.KindReasoning:
			if it.ReasoningText != "" && it.ReasoningJSON != "" {
				appendRoleBlock("assistant", map[string]any{
					"type":      "thinking",
					"thinking":  it.ReasoningText,
					"signature": it.ReasoningJSON,
				})
			}
		case core.KindAssistant:
			if it.Text != "" {
				appendRoleBlock("assistant", map[string]any{"type": "text", "text": it.Text})
			}
		case core.KindToolCall:
			var inputObj any = map[string]any{}
			if it.Arguments != "" {
				_ = json.Unmarshal([]byte(it.Arguments), &inputObj)
			}
			appendRoleBlock("assistant", map[string]any{
				"type":  "tool_use",
				"id":    it.CallID,
				"name":  it.ToolName,
				"input": inputObj,
			})
		case core.KindToolResult:
			var content any = it.Output
			if len(it.Images) > 0 {
				parts := []any{map[string]any{"type": "text", "text": it.Output}}
				for _, img := range it.Images {
					parts = append(parts, map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": img.MediaType,
							"data":       img.Data,
						},
					})
				}
				content = parts
			}
			appendRoleBlock("user", map[string]any{
				"type":        "tool_result",
				"tool_use_id": it.CallID,
				"content":     content,
				"is_error":    it.IsError,
			})
		}
	}

	body := map[string]any{
		"model":      req.Options.Model,
		"max_tokens": maxTokens,
		"messages":   msgs,
		"stream":     true,
	}

	mode := req.Options.ThinkingMode
	if mode == "" || mode == "auto" {
		if req.Options.Effort != "" {
			mode = "adaptive"
		}
	} else if mode == "prefer-adaptive" && req.Options.Effort != "" {
		mode = "adaptive"
	}
	switch mode {
	case "adaptive":
		body["thinking"] = map[string]any{"type": "adaptive"}
		if req.Options.Effort != "" {
			body["output_config"] = map[string]any{"effort": req.Options.Effort}
		}
	case "budget":
		budget := req.Options.ThinkingBudget
		if budget <= 0 || budget >= maxTokens {
			budget = max(1024, maxTokens-1)
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}

	if req.SystemPrompt != "" {
		sysBlock := map[string]any{"type": "text", "text": req.SystemPrompt}
		if req.Options.Cache != "off" {
			ttl := req.Options.CacheTTL
			if ttl == "" {
				ttl = "1h"
			}
			sysBlock["cache_control"] = map[string]any{"type": "ephemeral", "ttl": ttl}
		}
		body["system"] = []any{sysBlock}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": json.RawMessage(provider.BuildParametersSchema(t)),
			})
		}
		body["tools"] = tools
	}
	provider.MergeExtraBody(body, req.Options.ExtraBody, map[string]bool{
		"model": true, "messages": true, "system": true, "tools": true, "stream": true,
	})
	return json.Marshal(body)
}

func parseSSE(r io.Reader, emit func(provider.StreamEvent), overflowPhrases []string) error {
	type blockAccum struct {
		kind string
		id   string
		name string
		args strings.Builder
		sig  strings.Builder
	}
	blocks := make(map[int]*blockAccum)
	var incompleteLength bool
	var usage core.Usage

	err := transport.ParseSSE(r, func(ev transport.SSEEvent) error {
		if ev.Data == "" {
			return nil
		}
		var msg struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens      int64 `json:"input_tokens"`
					CacheReadTokens  int64 `json:"cache_read_input_tokens"`
					CacheWriteTokens int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &msg); err != nil {
			return nil
		}
		switch msg.Type {
		case "message_start":
			usage.ResponseID = msg.Message.ID
			usage.ServedModel = msg.Message.Model
			usage.InTokens = msg.Message.Usage.InputTokens
			usage.Cached = msg.Message.Usage.CacheReadTokens
			usage.CacheWrite = msg.Message.Usage.CacheWriteTokens
			usage.Input = usage.InTokens + usage.Cached + usage.CacheWrite
		case "content_block_start":
			b := &blockAccum{kind: msg.ContentBlock.Type, id: msg.ContentBlock.ID, name: msg.ContentBlock.Name}
			blocks[msg.Index] = b
			if b.kind == "tool_use" {
				emit(provider.StreamEvent{Kind: provider.EventToolCallStart, CallID: b.id, ToolName: b.name})
			}
		case "content_block_delta":
			switch msg.Delta.Type {
			case "text_delta":
				emit(provider.StreamEvent{Kind: provider.EventText, Text: msg.Delta.Text})
			case "thinking_delta":
				emit(provider.StreamEvent{Kind: provider.EventReasoning, Text: msg.Delta.Thinking})
			case "signature_delta":
				if b, ok := blocks[msg.Index]; ok {
					b.sig.WriteString(msg.Delta.Signature)
				}
			case "input_json_delta":
				if b, ok := blocks[msg.Index]; ok {
					b.args.WriteString(msg.Delta.PartialJSON)
					emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, CallID: b.id, ArgumentsDelta: msg.Delta.PartialJSON})
				}
			}
		case "content_block_stop":
			if b, ok := blocks[msg.Index]; ok {
				if b.kind == "tool_use" {
					args := b.args.String()
					if args == "" {
						args = "{}"
					}
					emit(provider.StreamEvent{Kind: provider.EventToolCallDone, CallID: b.id, ToolName: b.name, Arguments: args})
				} else if b.kind == "thinking" && b.sig.Len() > 0 {
					emit(provider.StreamEvent{Kind: provider.EventReasoning, ReasoningJSON: b.sig.String()})
				}
			}
		case "message_delta":
			usage.Output = msg.Usage.OutputTokens
			if msg.Delta.StopReason == "max_tokens" {
				incompleteLength = true
			}
		case "error":
			if provider.IsOverflowError(msg.Error.Message, overflowPhrases) {
				return &provider.OverflowError{Message: msg.Error.Message}
			}
			return fmt.Errorf("%s", msg.Error.Message)
		}
		return nil
	})
	if err != nil {
		return err
	}
	emit(provider.StreamEvent{Kind: provider.EventUsage, Usage: &usage})
	emit(provider.StreamEvent{Kind: provider.EventDone, IncompleteLength: incompleteLength})
	return nil
}

// Package provider defines the consumer-facing interfaces, wire-independent
// stream events, and shared helpers for model backends.
package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luank3/gax/internal/core"
)

// StreamEventKind identifies a single event emitted by Client.Stream.
type StreamEventKind uint8

const (
	EventText StreamEventKind = iota
	EventReasoning
	EventToolCallStart
	EventToolCallDelta
	EventToolCallDone
	EventUsage
	EventRetry
	EventDone
)

// StreamEvent is one provider-independent streaming event.
type StreamEvent struct {
	Kind             StreamEventKind
	Text             string
	ReasoningField   string
	ReasoningJSON    string
	CallID           string
	ToolName         string
	ArgumentsDelta   string
	Arguments        string
	Usage            *core.Usage
	IncompleteLength bool
	RetryAttempt     int
	RetryDelay       time.Duration
	RetryReason      string
}

// StreamOptions configures per-request provider behavior.
type StreamOptions struct {
	Model              string
	Effort             string
	SessionID          string
	MaxTokens          int64
	ThinkingMode       string
	ThinkingBudget     int64
	ReasoningFormat    string
	ReasoningRoundtrip string
	ReasoningRequired  bool
	SendCacheKey       bool
	Cache              string
	CacheTTL           string
	ExtraBody          map[string]any
	ExtraHeaders       map[string]string
	OverflowPhrases    []string
}

// StreamRequest is the provider-independent input to Client.Stream.
type StreamRequest struct {
	SystemPrompt string
	Items        []core.Item
	Tools        []core.ToolDef
	Options      StreamOptions
}

// Client streams a single model turn.
type Client interface {
	ID() string
	Stream(ctx context.Context, req StreamRequest, emit func(StreamEvent)) error
}

// ModelEntry describes one model returned by a provider's model listing.
type ModelEntry struct {
	ID           string
	DisplayName  string
	ContextLimit int64
	OutputLimit  int64
	Loaded       bool
	ImageInput   bool
}

// ModelLister is optionally implemented by providers that expose /models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelEntry, error)
}

// UsageReport holds account/subscription usage returned by /usage.
type UsageReport struct {
	Summary string
	Lines   []string
}

// UsageQuerier is optionally implemented by providers with a /usage endpoint.
type UsageQuerier interface {
	QueryUsage(ctx context.Context) (UsageReport, error)
}

// MergeExtraBody merges extra into dst, extending nested objects without
// overwriting core conversation/model/stream fields owned by gax.
func MergeExtraBody(dst, extra map[string]any, protected map[string]bool) []string {
	if len(extra) == 0 {
		return nil
	}
	var warnings []string
	for k, v := range extra {
		if protected[k] {
			warnings = append(warnings, "extra_body key "+k+" is owned by gax and ignored")
			continue
		}
		if vMap, ok := v.(map[string]any); ok {
			if dstMap, ok := dst[k].(map[string]any); ok {
				merged := maps.Clone(dstMap)
				maps.Copy(merged, vMap)
				dst[k] = merged
				continue
			}
		}
		dst[k] = v
	}
	return warnings
}

// ApplyExtraHeaders resolves $VAR / $$ and {session_id} in extra and applies
// them to hdr, returning any resolved secrets for redaction and warnings.
func ApplyExtraHeaders(hdr http.Header, extra map[string]string, sessionID string) (secrets []string, warnings []string) {
	for k, raw := range extra {
		if raw == "" {
			hdr.Del(k)
			continue
		}
		val := raw
		if strings.HasPrefix(raw, "$$") {
			val = raw[1:]
		} else if strings.HasPrefix(raw, "$") && len(raw) > 1 {
			envName := raw[1:]
			v, ok := os.LookupEnv(envName)
			if !ok || v == "" {
				warnings = append(warnings, "extra_headers "+k+": environment variable "+envName+" is unset")
				continue
			}
			secrets = append(secrets, v)
			val = v
		}
		if sessionID != "" {
			val = strings.ReplaceAll(val, "{session_id}", sessionID)
		}
		hdr.Set(k, val)
	}
	return secrets, warnings
}

// IsOverflowError reports whether an API error message indicates context overflow.
func IsOverflowError(msg string, extraPhrases []string) bool {
	low := strings.ToLower(msg)
	for _, phrase := range []string{
		"context_length_exceeded",
		"context length exceeded",
		"maximum context length",
		"exceeds the available context",
		"prompt is too long",
		"request_too_large",
		"too many tokens",
		"context window",
	} {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	for _, p := range extraPhrases {
		if p != "" && strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// OverflowError wraps a provider error that indicates the prompt exceeded the context window.
type OverflowError struct {
	Message string
}

func (e *OverflowError) Error() string { return e.Message }

// BuildParametersSchema builds a JSON Schema object for ToolDef.
// Only parameters marked Required: true appear in "required" (hax v0.6.1 fix).
func BuildParametersSchema(def core.ToolDef) json.RawMessage {
	if len(def.RawSchema) > 0 {
		return def.RawSchema
	}
	props := make(map[string]any, len(def.Params))
	var required []string
	for _, p := range def.Params {
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if p.Minimum != nil {
			prop["minimum"] = *p.Minimum
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return b
}

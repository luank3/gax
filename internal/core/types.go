// Package core defines the shared, UI-agnostic domain types and event stream
// used across providers, tools, sessions, the agent loop, and frontends.
package core

// ItemKind identifies the kind of record in a conversation log.
type ItemKind string

const (
	KindUser         ItemKind = "user"
	KindAssistant    ItemKind = "assistant"
	KindToolCall     ItemKind = "tool_call"
	KindToolResult   ItemKind = "tool_result"
	KindReasoning    ItemKind = "reasoning"
	KindTurnBoundary ItemKind = "turn_boundary"
	KindTurnUsage    ItemKind = "turn_usage"
)

// ItemOrigin marks synthetic conversation items.
type ItemOrigin string

const (
	OriginNone        ItemOrigin = ""
	OriginCompactSeed ItemOrigin = "compact_seed"
	OriginContinue    ItemOrigin = "continuation"
	OriginInterrupted ItemOrigin = "interrupted"
	OriginSkipped     ItemOrigin = "skipped"
	OriginRefused     ItemOrigin = "refused"
	OriginSummarized  ItemOrigin = "summarized"
	OriginTaskNote    ItemOrigin = "task_note"
	OriginCompaction  ItemOrigin = "compaction"
)

// ImageAttachment carries an inline image attached to a message or tool result.
type ImageAttachment struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Usage records token counts, latency, cost, and routing provenance for one provider round-trip.
type Usage struct {
	Input         int64   `json:"input,omitempty"`
	Output        int64   `json:"output,omitempty"`
	Cached        int64   `json:"cached,omitempty"`
	CacheWrite    int64   `json:"cache_write,omitempty"`
	InTokens      int64   `json:"in_tokens,omitempty"`
	ElapsedMS     int64   `json:"elapsed_ms,omitempty"`
	CostInput     float64 `json:"cost_input,omitempty"`
	CostOutput    float64 `json:"cost_output,omitempty"`
	CostCached    float64 `json:"cost_cached,omitempty"`
	CostTotal     float64 `json:"cost_total,omitempty"`
	CostEstimated bool    `json:"cost_estimated,omitempty"`
	ServedModel   string  `json:"served_model,omitempty"`
	Route         string  `json:"route,omitempty"`
	ResponseID    string  `json:"response_id,omitempty"`
}

// Item is one canonical conversation record in a session or model context.
type Item struct {
	Kind           ItemKind          `json:"kind"`
	Origin         ItemOrigin        `json:"origin,omitempty"`
	Inherited      bool              `json:"inherited,omitempty"`
	Source         string            `json:"source,omitempty"`
	Text           string            `json:"text,omitempty"`
	Images         []ImageAttachment `json:"images,omitempty"`
	CallID         string            `json:"call_id,omitempty"`
	ToolName       string            `json:"tool_name,omitempty"`
	Arguments      string            `json:"arguments,omitempty"`
	Output         string            `json:"output,omitempty"`
	IsError        bool              `json:"is_error,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	Model          string            `json:"model,omitempty"`
	ReasoningJSON  string            `json:"reasoning_json,omitempty"`
	ReasoningText  string            `json:"reasoning_text,omitempty"`
	ReasoningField string            `json:"reasoning_field,omitempty"`
	Usage          *Usage            `json:"usage,omitempty"`
}

// ToolParam describes a single parameter in a tool schema.
type ToolParam struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Minimum     *int     `json:"minimum,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ToolDef describes a tool advertised to a model provider.
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Params      []ToolParam `json:"params,omitempty"`
	RawSchema   []byte      `json:"-"`
}

// Message is a provider-independent input message built from session items.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
}

// SessionHeader holds the metadata from the first JSONL line of a session file.
type SessionHeader struct {
	Version    int    `json:"version"`
	GaxVersion string `json:"hax_version,omitempty"`
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp,omitempty"`
	CWD        string `json:"cwd"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Preset     string `json:"preset,omitempty"`
	ModelLabel string `json:"model_label,omitempty"`
	GitBranch  string `json:"git_branch,omitempty"`
	GitCommit  string `json:"git_commit,omitempty"`
	GitSubject string `json:"git_subject,omitempty"`
	ForkedFrom string `json:"forked_from,omitempty"`
}

// Outcome describes how an agent loop or turn ended.
type Outcome string

const (
	OutcomeComplete    Outcome = "complete"
	OutcomeError       Outcome = "error"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomePaused      Outcome = "paused"
	OutcomeMaxTurns    Outcome = "max_turns"
)

package conversation

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Kind string

const (
	KindUser         Kind = "user"
	KindAssistant    Kind = "assistant"
	KindToolCall     Kind = "tool_call"
	KindToolResult   Kind = "tool_result"
	KindReasoning    Kind = "reasoning"
	KindTurnBoundary Kind = "turn_boundary"
	KindTurnUsage    Kind = "turn_usage"
)

type Origin string

const (
	OriginCompactSeed  Origin = "compact_seed"
	OriginContinuation Origin = "continuation"
	OriginInterrupted  Origin = "interrupted"
	OriginSkipped      Origin = "skipped"
	OriginRefused      Origin = "refused"
	OriginSummarized   Origin = "summarized"
	OriginTaskNote     Origin = "task_note"
	OriginCompaction   Origin = "compaction"
)

type Image struct {
	MIME   string `json:"mime,omitempty"`
	Data   string `json:"data,omitempty"`
	Width  int64  `json:"width,omitempty"`
	Height int64  `json:"height,omitempty"`
}

// Usage records the provider's accounting for one response. Nil values mean the
// provider did not report them; pointers preserve reported zero values.
type Usage struct {
	InputTokens        *int64   `json:"input,omitempty"`
	OutputTokens       *int64   `json:"output,omitempty"`
	CachedTokens       *int64   `json:"cached,omitempty"`
	CacheWriteTokens   *int64   `json:"cache_write,omitempty"`
	CacheWrite1HTokens *int64   `json:"cache_write_1h,omitempty"`
	Cost               *float64 `json:"cost,omitempty"`
	ElapsedMS          *int64   `json:"elapsed_ms,omitempty"`
	UncachedInput      *int64   `json:"in_tokens,omitempty"`
	CostInput          *float64 `json:"cost_in,omitempty"`
	CostCacheRead      *float64 `json:"cost_cache_read,omitempty"`
	CostCacheWrite     *float64 `json:"cost_cache_write,omitempty"`
	CostOutput         *float64 `json:"cost_out,omitempty"`
	CostTotal          *float64 `json:"cost_total,omitempty"`
	CostEstimated      bool     `json:"cost_estimated,omitempty"`
	ProviderLabel      *string  `json:"provider_label,omitempty"`
	ModelLabel         *string  `json:"model_label,omitempty"`
	Effort             *string  `json:"effort,omitempty"`
	ServedModel        *string  `json:"served_model,omitempty"`
	Route              *string  `json:"route,omitempty"`
	ResponseID         *string  `json:"response_id,omitempty"`
}

func (usage Usage) MarshalJSON() ([]byte, error) {
	type usageJSON Usage
	copy := usage
	if copy.InputTokens != nil && *copy.InputTokens < 0 {
		copy.InputTokens = nil
	}
	if copy.OutputTokens != nil && *copy.OutputTokens < 0 {
		copy.OutputTokens = nil
	}
	if copy.CachedTokens != nil && *copy.CachedTokens < 0 {
		copy.CachedTokens = nil
	}
	if copy.CacheWriteTokens != nil && *copy.CacheWriteTokens < 0 {
		copy.CacheWriteTokens = nil
	}
	if copy.CacheWrite1HTokens != nil && *copy.CacheWrite1HTokens < 0 {
		copy.CacheWrite1HTokens = nil
	}
	if copy.ElapsedMS != nil && *copy.ElapsedMS < 0 {
		copy.ElapsedMS = nil
	}
	if copy.UncachedInput != nil && *copy.UncachedInput < 0 {
		copy.UncachedInput = nil
	}
	if copy.Cost != nil && *copy.Cost < 0 {
		copy.Cost = nil
	}
	if copy.CostInput != nil && *copy.CostInput < 0 {
		copy.CostInput = nil
	}
	if copy.CostCacheRead != nil && *copy.CostCacheRead < 0 {
		copy.CostCacheRead = nil
	}
	if copy.CostCacheWrite != nil && *copy.CostCacheWrite < 0 {
		copy.CostCacheWrite = nil
	}
	if copy.CostOutput != nil && *copy.CostOutput < 0 {
		copy.CostOutput = nil
	}
	if copy.CostTotal != nil && *copy.CostTotal < 0 {
		copy.CostTotal = nil
	}
	return json.Marshal(usageJSON(copy))
}

type Item struct {
	Kind             Kind    `json:"kind,omitempty"`
	Text             *string `json:"text,omitempty"`
	CallID           *string `json:"call_id,omitempty"`
	ToolName         *string `json:"tool_name,omitempty"`
	Arguments        *string `json:"arguments,omitempty"`
	Output           *string `json:"output,omitempty"`
	OutputHiddenTail int64   `json:"output_hidden_tail,omitempty"`
	ReasoningJSON    *string `json:"reasoning_json,omitempty"`
	ReasoningText    *string `json:"reasoning_text,omitempty"`
	ReasoningField   string  `json:"reasoning_field,omitempty"`
	Provider         *string `json:"provider,omitempty"`
	Model            *string `json:"model,omitempty"`
	Origin           Origin  `json:"origin,omitempty"`
	Usage            *Usage  `json:"usage,omitempty"`
	Inherited        bool    `json:"inherited,omitempty"`
	Images           []Image `json:"images,omitempty"`
}

var errInvalidKind = errors.New("invalid conversation item kind")

func (k Kind) valid() bool {
	switch k {
	case KindUser, KindAssistant, KindToolCall, KindToolResult, KindReasoning, KindTurnBoundary, KindTurnUsage:
		return true
	default:
		return false
	}
}

func (o Origin) valid() bool {
	switch o {
	case "", OriginCompactSeed, OriginContinuation, OriginInterrupted, OriginSkipped,
		OriginRefused, OriginSummarized, OriginTaskNote, OriginCompaction:
		return true
	default:
		return false
	}
}

// DecodeItem matches the session reader's tolerant handling of future fields,
// unknown origins, malformed image parts, and usage on non-usage items.
func DecodeItem(data []byte) (Item, error) {
	if !utf8.Valid(data) {
		return Item{}, errors.New("invalid UTF-8 in conversation item")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return Item{}, err
	}
	kind := decodeString(object["kind"])
	if kind == nil {
		return Item{}, errInvalidKind
	}
	item := Item{Kind: Kind(*kind)}
	if !item.Kind.valid() {
		return Item{}, errInvalidKind
	}
	item.Text = decodeString(object["text"])
	item.CallID = decodeString(object["call_id"])
	item.ToolName = decodeString(object["tool_name"])
	item.Arguments = decodeString(object["arguments"])
	item.Output = decodeString(object["output"])
	item.ReasoningJSON = decodeString(object["reasoning_json"])
	item.ReasoningText = decodeString(object["reasoning_text"])
	if reasoningField := decodeString(object["reasoning_field"]); reasoningField != nil {
		item.ReasoningField = *reasoningField
	}
	item.Provider = decodeString(object["provider"])
	item.Model = decodeString(object["model"])
	if origin := decodeString(object["origin"]); origin != nil {
		item.Origin = Origin(*origin)
	}
	if !item.Origin.valid() {
		item.Origin = ""
	}
	if value, ok := decodeInteger(object["output_hidden_tail"]); ok && value > 0 {
		item.OutputHiddenTail = value
	}
	if item.Kind == KindTurnUsage {
		item.Usage = decodeUsage(object["usage"])
	}
	item.Inherited = string(object["inherited"]) == "true"
	if raw := object["images"]; len(raw) > 0 && raw[0] == '[' {
		var images []json.RawMessage
		if json.Unmarshal(raw, &images) == nil {
			for _, rawImage := range images {
				var imageObject map[string]json.RawMessage
				if json.Unmarshal(rawImage, &imageObject) != nil {
					continue
				}
				mime := decodeString(imageObject["mime"])
				data := decodeString(imageObject["data"])
				if mime == nil || *mime == "" || data == nil || *data == "" {
					continue
				}
				image := Image{MIME: *mime, Data: *data}
				if value, ok := decodeInteger(imageObject["width"]); ok {
					if value > 0 {
						image.Width = value
					}
				}
				if value, ok := decodeInteger(imageObject["height"]); ok {
					if value > 0 {
						image.Height = value
					}
				}
				item.Images = append(item.Images, image)
			}
		}
	}
	return item, nil
}

func decodeString(raw json.RawMessage) *string {
	if len(raw) == 0 || raw[0] != '"' {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func decodeInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] == '"' || strings.ContainsAny(string(raw), ".eE") {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil
}

func decodeReal(raw json.RawMessage) *float64 {
	if len(raw) == 0 || !(raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9') {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func decodeOptionalInteger(object map[string]json.RawMessage, key string) *int64 {
	value, ok := decodeInteger(object[key])
	if !ok {
		return nil
	}
	return &value
}

func decodeUsage(raw json.RawMessage) *Usage {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	usage := &Usage{
		InputTokens:        decodeOptionalInteger(object, "input"),
		OutputTokens:       decodeOptionalInteger(object, "output"),
		CachedTokens:       decodeOptionalInteger(object, "cached"),
		CacheWriteTokens:   decodeOptionalInteger(object, "cache_write"),
		CacheWrite1HTokens: decodeOptionalInteger(object, "cache_write_1h"),
		Cost:               decodeReal(object["cost"]),
		ElapsedMS:          decodeOptionalInteger(object, "elapsed_ms"),
		UncachedInput:      decodeOptionalInteger(object, "in_tokens"),
		CostInput:          decodeReal(object["cost_in"]),
		CostCacheRead:      decodeReal(object["cost_cache_read"]),
		CostCacheWrite:     decodeReal(object["cost_cache_write"]),
		CostOutput:         decodeReal(object["cost_out"]),
		CostTotal:          decodeReal(object["cost_total"]),
		CostEstimated:      string(object["cost_estimated"]) == "true",
		ProviderLabel:      decodeString(object["provider_label"]),
		ModelLabel:         decodeString(object["model_label"]),
		Effort:             decodeString(object["effort"]),
		ServedModel:        decodeString(object["served_model"]),
		Route:              decodeString(object["route"]),
		ResponseID:         decodeString(object["response_id"]),
	}
	if usage.UncachedInput == nil || *usage.UncachedInput < 0 {
		input := int64(-1)
		cached := int64(0)
		written := int64(0)
		if usage.InputTokens != nil {
			input = *usage.InputTokens
		}
		if usage.CachedTokens != nil && *usage.CachedTokens > 0 {
			cached = *usage.CachedTokens
		}
		if usage.CacheWriteTokens != nil && *usage.CacheWriteTokens > 0 {
			written = *usage.CacheWriteTokens
		}
		value := input - cached - written
		if value < 0 {
			value = 0
		}
		usage.UncachedInput = &value
	}
	return usage
}

func String(value string) *string    { return &value }
func Int64(value int64) *int64       { return &value }
func Float64(value float64) *float64 { return &value }

func IsTypedPrompt(item Item) bool {
	return item.Kind == KindUser && item.Origin == ""
}

func ContextFloor(items []Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == KindUser && items[i].Origin == OriginCompactSeed {
			return i
		}
	}
	return 0
}

func UserTurnCut(items []Item, keep int) int {
	if keep <= 0 {
		return 0
	}
	seen := 0
	for i, item := range items {
		if !IsTypedPrompt(item) {
			continue
		}
		if seen == keep {
			if i > 0 && items[i-1].Kind == KindTurnBoundary {
				return i - 1
			}
			return i
		}
		seen++
	}
	return len(items)
}

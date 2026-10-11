// Package agent contains provider-independent conversation and turn state.
package agent

import (
	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/session"
)

const (
	InterruptMarker = "[interrupted]"
	ContinueMarker  = "[continue]"
)

type ResumeTail uint8

const (
	ResumeTailEmpty ResumeTail = iota
	ResumeTailMarked
	ResumeTailUser
	ResumeTailClean
)

type ResumeReason uint8

const (
	ResumeReasonNone ResumeReason = iota
	ResumeReasonPaused
	ResumeReasonMaxTurns
	ResumeReasonInterrupted
	ResumeReasonError
)

// Session holds the canonical item history and its provider-facing settings. Item slices are
// mutable through the methods below; Context returns a view that remains valid until mutation.
type Session struct {
	Model              *string
	ModelLabel         *string
	Effort             *string
	ProviderID         *string
	SystemPrompt       string
	Tools              []coreprovider.ToolDefinition
	RawMode            bool
	Items              []conversation.Item
	Retired            []conversation.Item
	WorkedMS           int64
	LastUserTurnMS     int64
	CompactionDeferred bool
	ResumeReason       ResumeReason
}

type AbsorbResult struct {
	ItemsFrom   int
	HadToolCall bool
}

func NewSession() *Session { return &Session{LastUserTurnMS: -1} }

func (state *Session) Append(item conversation.Item) {
	if state == nil {
		return
	}
	state.Items = append(state.Items, item)
}

func (state *Session) Reset() {
	if state == nil {
		return
	}
	clear(state.Items)
	state.Items = state.Items[:0]
	clear(state.Retired)
	state.Retired = state.Retired[:0]
	state.WorkedMS = 0
	state.LastUserTurnMS = -1
	state.CompactionDeferred = false
	state.ResumeReason = ResumeReasonNone
}

func (state *Session) Retire(from int) {
	if state == nil || from < 0 || from >= len(state.Items) {
		return
	}
	state.Retired = append(state.Retired, state.Items[from:]...)
	clear(state.Items[from:])
	state.Items = state.Items[:from]
	state.LastUserTurnMS = -1
	state.CompactionDeferred = false
	state.ResumeReason = ResumeReasonNone
}

// Adopt transfers the loaded conversation arrays and timing into the session, leaving the
// provider settings intact and clearing ownership from loaded.
func (state *Session) Adopt(loaded *session.Loaded) {
	if state == nil || loaded == nil {
		return
	}
	state.Reset()
	state.Items = loaded.Items
	state.Retired = loaded.Retired
	state.WorkedMS = loaded.WorkedMS
	state.LastUserTurnMS = -1
	state.CompactionDeferred = false
	state.ResumeReason = ResumeReasonNone
	if loaded.LastUserTurnMS != nil {
		state.LastUserTurnMS = *loaded.LastUserTurnMS
	}
	loaded.Items = nil
	loaded.Retired = nil
	loaded.WorkedMS = 0
	loaded.LastUserTurnMS = nil
}

func (state *Session) AddWorked(elapsedMS int64) {
	if state == nil || elapsedMS < 0 {
		return
	}
	state.WorkedMS += elapsedMS
	state.LastUserTurnMS = elapsedMS
}

func (state *Session) Context() coreprovider.Context {
	if state == nil {
		return coreprovider.Context{ImageInput: -1}
	}
	floor := conversation.ContextFloor(state.Items)
	return coreprovider.Context{
		SystemPrompt: state.SystemPrompt,
		Items:        state.Items[floor:],
		Tools:        state.Tools,
		Effort:       state.Effort,
		ImageInput:   -1,
	}
}

func (state *Session) AddUser(text string) {
	if state == nil {
		return
	}
	state.AddBoundary()
	state.Append(conversation.Item{Kind: conversation.KindUser, Text: conversation.String(text)})
}

func (state *Session) AddContinuation() {
	if state == nil {
		return
	}
	state.AddBoundary()
	state.Append(conversation.Item{
		Kind: conversation.KindUser, Text: conversation.String(ContinueMarker), Origin: conversation.OriginContinuation,
	})
}

func (state *Session) AddBoundary() {
	state.Append(conversation.Item{Kind: conversation.KindTurnBoundary})
}

func (state *Session) MarkInterrupt() {
	if state == nil {
		return
	}
	index := len(state.Items)
	for index > 0 {
		kind := state.Items[index-1].Kind
		if kind != conversation.KindTurnUsage && kind != conversation.KindTurnBoundary {
			break
		}
		index--
	}
	if index > 0 {
		origin := state.Items[index-1].Origin
		if origin == conversation.OriginInterrupted || origin == conversation.OriginSkipped {
			return
		}
	}
	state.Append(conversation.Item{
		Kind: conversation.KindAssistant, Text: conversation.String(InterruptMarker), Origin: conversation.OriginInterrupted,
	})
}

func (state *Session) ResumeTail() ResumeTail {
	if state == nil {
		return ResumeTailEmpty
	}
	for index := len(state.Items) - 1; index >= 0; index-- {
		item := state.Items[index]
		if item.Kind == conversation.KindTurnUsage || item.Kind == conversation.KindTurnBoundary {
			continue
		}
		if item.Origin == conversation.OriginInterrupted || item.Origin == conversation.OriginSkipped {
			return ResumeTailMarked
		}
		if item.Kind == conversation.KindUser && item.Origin != conversation.OriginCompactSeed {
			return ResumeTailUser
		}
		return ResumeTailClean
	}
	return ResumeTailEmpty
}

// ResumeFromTail restores the REPL's empty-submit action after loading a conversation.
func (state *Session) ResumeFromTail() {
	if state == nil {
		return
	}
	switch state.ResumeTail() {
	case ResumeTailMarked, ResumeTailUser:
		state.ResumeReason = ResumeReasonInterrupted
	default:
		state.ResumeReason = ResumeReasonNone
	}
}

func (reason ResumeReason) Placeholder() string {
	switch reason {
	case ResumeReasonPaused:
		return "paused — enter to continue"
	case ResumeReasonMaxTurns:
		return "max turns reached — enter to continue"
	case ResumeReasonInterrupted:
		return "enter to continue"
	case ResumeReasonError:
		return "enter to retry"
	default:
		return ""
	}
}

func (state *Session) LastContextTokens() int64 {
	if state == nil {
		return -1
	}
	items := state.Context().Items
	floor := 0
	if len(items) > 0 && items[0].Kind == conversation.KindUser && items[0].Origin == conversation.OriginCompactSeed {
		floor = 1
		for floor < len(items) && items[floor].Kind == conversation.KindTurnUsage {
			floor++
		}
	}
	for index := len(items) - 1; index >= floor; index-- {
		item := items[index]
		if item.Kind != conversation.KindTurnUsage || item.Usage == nil || item.Origin == conversation.OriginCompaction {
			continue
		}
		if item.Usage.InputTokens != nil && item.Usage.OutputTokens != nil &&
			*item.Usage.InputTokens >= 0 && *item.Usage.OutputTokens >= 0 {
			return *item.Usage.InputTokens + *item.Usage.OutputTokens
		}
	}
	return -1
}

func (state *Session) HasReportedUsage() bool {
	if state == nil {
		return false
	}
	for _, item := range state.Items {
		if reportsTokens(item) {
			return true
		}
	}
	for _, item := range state.Retired {
		if !item.Inherited && reportsTokens(item) {
			return true
		}
	}
	return false
}

// Absorb transfers assembled turn items into history and stamps reasoning provenance.
func (state *Session) Absorb(turn *conversation.Turn) AbsorbResult {
	if state == nil || turn == nil {
		return AbsorbResult{}
	}
	result := AbsorbResult{ItemsFrom: len(state.Items)}
	items := turn.TakeItems()
	for _, item := range items {
		if item.Kind == conversation.KindToolCall {
			result.HadToolCall = true
		}
		if item.Kind == conversation.KindReasoning {
			item.Provider = cloneString(state.ProviderID)
			item.Model = cloneString(state.Model)
		}
		state.Append(item)
	}
	return result
}

func reportsTokens(item conversation.Item) bool {
	if item.Kind != conversation.KindTurnUsage || item.Usage == nil {
		return false
	}
	return item.Usage.InputTokens != nil && *item.Usage.InputTokens >= 0 ||
		item.Usage.OutputTokens != nil && *item.Usage.OutputTokens >= 0
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

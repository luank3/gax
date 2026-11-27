// Package agent implements the UI-agnostic turn loop, session context windowing,
// system-prompt assembly, usage accounting, and compaction with overflow recovery.
package agent

import (
	"sync"

	"github.com/luank3/gax/internal/core"
)

// Session holds the in-memory conversation item log and computes model-visible windows.
type Session struct {
	mu     sync.RWMutex
	Header core.SessionHeader
	Items  []core.Item
	onItem func(core.Item)
}

// NewSession creates an empty Session with the given header.
func NewSession(header core.SessionHeader) *Session {
	return &Session{Header: header}
}

// SetOnAppend registers an optional hook called whenever an item is appended (e.g., JSONL persistence).
func (s *Session) SetOnAppend(fn func(core.Item)) {
	s.mu.Lock()
	s.onItem = fn
	s.mu.Unlock()
}

// Append adds items to the session log.
func (s *Session) Append(items ...core.Item) {
	s.mu.Lock()
	s.Items = append(s.Items, items...)
	cb := s.onItem
	s.mu.Unlock()
	if cb != nil {
		for _, it := range items {
			cb(it)
		}
	}
}

// ContextWindow returns the active model-visible items starting from the latest
// compaction seed (or the beginning if never compacted).
func (s *Session) ContextWindow() []core.Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	start := 0
	for i := len(s.Items) - 1; i >= 0; i-- {
		if s.Items[i].Kind == core.KindUser && s.Items[i].Origin == core.OriginCompactSeed {
			start = i
			break
		}
	}
	out := make([]core.Item, len(s.Items)-start)
	copy(out, s.Items[start:])
	return out
}

// EstimateTokens estimates the token count of items (~4 bytes per token).
func EstimateTokens(systemPrompt string, items []core.Item) int64 {
	bytes := len(systemPrompt)
	for _, it := range items {
		bytes += len(it.Text) + len(it.Arguments) + len(it.Output) + len(it.ReasoningText)
	}
	return int64(bytes+3) / 4
}

// Stats summarizes token and cost totals for the session.
type Stats struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	CostTotal    float64
	Turns        int
}

// Totals aggregates usage across all recorded turn_usage items.
func (s *Session) Totals() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var st Stats
	for _, it := range s.Items {
		if it.Kind != core.KindTurnUsage || it.Usage == nil || it.Inherited {
			continue
		}
		st.Turns++
		st.InputTokens += it.Usage.Input
		st.OutputTokens += it.Usage.Output
		st.CachedTokens += it.Usage.Cached
		st.CostTotal += it.Usage.CostTotal
	}
	return st
}

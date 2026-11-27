package core

import "time"

// Event is a typed notification emitted by the agent loop to UI frontends.
type Event interface {
	isAgentEvent()
}

// EventSink receives events from an agent run. Implementations must be safe
// for synchronous invocation from the agent loop goroutine.
type EventSink interface {
	Emit(Event)
}

// EventFunc adapts a function to EventSink.
type EventFunc func(Event)

// Emit calls the underlying function.
func (f EventFunc) Emit(ev Event) {
	if f != nil {
		f(ev)
	}
}

type TurnStarted struct {
	Turn     int
	Provider string
	Model    string
	Effort   string
}

func (TurnStarted) isAgentEvent() {}

type TextDelta struct {
	Text string
}

func (TextDelta) isAgentEvent() {}

type ReasoningDelta struct {
	Text string
}

func (ReasoningDelta) isAgentEvent() {}

type ToolCallSeen struct {
	CallID string
	Name   string
}

func (ToolCallSeen) isAgentEvent() {}

type ToolCallStarted struct {
	CallID      string
	Name        string
	ArgsSummary string
}

func (ToolCallStarted) isAgentEvent() {}

type ToolOutputDelta struct {
	CallID string
	Stream string
	Chunk  string
}

func (ToolOutputDelta) isAgentEvent() {}

type ToolCallFinished struct {
	CallID      string
	Name        string
	ArgsSummary string
	Summary     string
	DurationMS  int64
	IsError     bool
}

func (ToolCallFinished) isAgentEvent() {}

type TaskNote struct {
	Text string
}

func (TaskNote) isAgentEvent() {}

type CompactStarted struct {
	Auto bool
}

func (CompactStarted) isAgentEvent() {}

type CompactFinished struct {
	BeforeTokens int64
	AfterTokens  int64
	Err          error
}

func (CompactFinished) isAgentEvent() {}

type TurnUsage struct {
	Usage Usage
}

func (TurnUsage) isAgentEvent() {}

type RetryScheduled struct {
	Attempt int
	Delay   time.Duration
	Reason  string
}

func (RetryScheduled) isAgentEvent() {}

type TurnFinished struct {
	Outcome Outcome
	Error   string
}

func (TurnFinished) isAgentEvent() {}

package conversation

import "strings"

type EventKind uint8

const (
	EventTextDelta EventKind = iota
	EventToolCallStart
	EventToolCallDelta
	EventToolCallEnd
	EventReasoningItem
	EventReasoningDelta
	EventRetry
	EventProgress
	EventDone
	EventError
)

type Event struct {
	Kind         EventKind
	Text         string
	ID           *string
	Name         string
	Delta        string
	JSON         *string
	StopReason   *string
	Usage        *Usage
	Response     *StreamResponse
	ErrorMessage string
	HTTPStatus   int
	Attempt      int
	MaxAttempts  int
	DelayMS      int64
	Processed    int64
	Total        int64
	Cache        int64
}

type StreamResponse struct {
	ID    *string
	Model *string
	Route *string
}

type TurnState uint8

const (
	TurnStreaming TurnState = iota
	TurnDone
	TurnFailed
)

type pendingCall struct {
	id        string
	name      string
	arguments strings.Builder
}

// Turn assembles provider stream events into provider-independent items.
type Turn struct {
	Items        []Item
	Text         strings.Builder
	HasText      bool
	Reasoning    strings.Builder
	HasReasoning bool
	pendingCalls []pendingCall
	State        TurnState
}

func (t *Turn) Reset() {
	*t = Turn{State: TurnStreaming}
}

func (t *Turn) append(item Item) { t.Items = append(t.Items, item) }

func (t *Turn) flushText() {
	if !t.HasText {
		return
	}
	t.append(Item{Kind: KindAssistant, Text: String(t.Text.String())})
	t.Text.Reset()
	t.HasText = false
}

func (t *Turn) flushReasoning() {
	if !t.HasReasoning {
		return
	}
	t.append(Item{Kind: KindReasoning, ReasoningText: String(t.Reasoning.String())})
	t.Reasoning.Reset()
	t.HasReasoning = false
}

func (t *Turn) FlushText(suffix string) {
	if !t.HasText {
		return
	}
	if suffix != "" {
		t.Text.WriteString(suffix)
	}
	t.flushText()
}

func (t *Turn) DiscardReasoning() {
	t.Reasoning.Reset()
	t.HasReasoning = false
}

func (t *Turn) KeepText() {
	t.DiscardReasoning()
	kept := t.Items[:0]
	for _, item := range t.Items {
		if item.Kind == KindAssistant {
			kept = append(kept, item)
		}
	}
	t.Items = kept
}

func (t *Turn) findCall(id *string) int {
	if id == nil {
		return -1
	}
	for i := range t.pendingCalls {
		if t.pendingCalls[i].id == *id {
			return i
		}
	}
	return -1
}

func (t *Turn) consume(event Event) {
	switch event.Kind {
	case EventTextDelta:
		t.flushReasoning()
		t.Text.WriteString(event.Text)
		t.HasText = true
	case EventToolCallStart:
		t.flushReasoning()
		t.flushText()
		if event.ID != nil {
			t.pendingCalls = append(t.pendingCalls, pendingCall{id: *event.ID, name: event.Name})
		}
	case EventToolCallDelta:
		if index := t.findCall(event.ID); index >= 0 {
			t.pendingCalls[index].arguments.WriteString(event.Delta)
		}
	case EventToolCallEnd:
		if index := t.findCall(event.ID); index >= 0 {
			call := &t.pendingCalls[index]
			t.append(Item{
				Kind:      KindToolCall,
				CallID:    String(call.id),
				ToolName:  String(call.name),
				Arguments: String(call.arguments.String()),
			})
			copy(t.pendingCalls[index:], t.pendingCalls[index+1:])
			t.pendingCalls = t.pendingCalls[:len(t.pendingCalls)-1]
		}
	case EventReasoningDelta:
		if event.Text != "" {
			t.Reasoning.WriteString(event.Text)
			t.HasReasoning = true
		}
	case EventReasoningItem:
		t.flushText()
		item := Item{Kind: KindReasoning, ReasoningJSON: event.JSON}
		if t.HasReasoning {
			item.ReasoningText = String(t.Reasoning.String())
			t.Reasoning.Reset()
		}
		t.HasReasoning = false
		t.append(item)
	case EventRetry:
		t.Reset()
	case EventProgress:
	case EventDone:
		t.flushReasoning()
		t.flushText()
		t.State = TurnDone
	case EventError:
		t.State = TurnFailed
	}
}

func (t *Turn) Consume(event Event) {
	if t.State != TurnStreaming {
		return
	}
	t.consume(event)
}

func (t *Turn) TakeItems() []Item {
	items := t.Items
	t.Items = nil
	return items
}

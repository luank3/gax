package app

import (
	"testing"

	"github.com/luank3/gax/internal/conversation"
	coretools "github.com/luank3/gax/internal/tools"
)

type recordingHeaders struct{ started []string }

func (recording *recordingHeaders) ToolStart(call conversation.Item, _ coretools.ToolPresentation) error {
	if call.CallID == nil {
		recording.started = append(recording.started, "")
		return nil
	}
	recording.started = append(recording.started, *call.CallID)
	return nil
}

func toolCall(id string) conversation.Item {
	name := "read"
	return conversation.Item{Kind: conversation.KindToolCall, CallID: &id, ToolName: &name}
}

// TestToolHeadersStartAtDispatch pins the batch/one-at-a-time contract: the loop announces every
// call in a batch before dispatching any of them, and the renderer must still see one header per
// call, in dispatch order, with no header before its own dispatch.
func TestToolHeadersStartAtDispatch(t *testing.T) {
	recording := &recordingHeaders{}
	headers := newToolHeaders(recording)
	headers.reset()

	first, second := toolCall("call-1"), toolCall("call-2")
	headers.seen(first)
	headers.seen(second)
	if len(recording.started) != 0 {
		t.Fatalf("announcing a batch wrote headers: %v", recording.started)
	}

	headers.start(first, coretools.ToolPresentation{})
	if got := recording.started; len(got) != 1 || got[0] != "call-1" {
		t.Fatalf("headers after the first dispatch = %v, want [call-1]", got)
	}
	headers.start(second, coretools.ToolPresentation{})
	if got := recording.started; len(got) != 2 || got[1] != "call-2" {
		t.Fatalf("headers after the second dispatch = %v, want [call-1 call-2]", got)
	}

	// A call whose dispatch never began keeps its header outstanding only until the next turn.
	headers.start(first, coretools.ToolPresentation{})
	if got := recording.started; len(got) != 2 {
		t.Fatalf("a call started twice wrote %v, want two headers", got)
	}
	headers.seen(first)
	headers.reset()
	headers.start(first, coretools.ToolPresentation{})
	if got := recording.started; len(got) != 2 {
		t.Fatalf("a reset dropped an announced call: %v", got)
	}
}

// TestToolHeadersLeaveUnannouncedCallsToTheResult pins that a call the loop never announced, such as
// one restored from a session, is left for the result path to open.
func TestToolHeadersLeaveUnannouncedCallsToTheResult(t *testing.T) {
	recording := &recordingHeaders{}
	headers := newToolHeaders(recording)
	headers.start(toolCall("call-9"), coretools.ToolPresentation{})
	if len(recording.started) != 0 {
		t.Fatalf("an unannounced call wrote a header: %v", recording.started)
	}
}

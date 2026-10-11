package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/runtime"
	coretools "github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transport"
)

func TestRunTurnAssemblesEventsAndObservesStream(t *testing.T) {
	var observed []conversation.Event
	state := agent.Session{Model: conversation.String("m"), SystemPrompt: "sys"}
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, model string,
		requestContext coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		if model != "m" || requestContext.SystemPrompt != "sys" {
			t.Errorf("session request context was not forwarded: model=%q context=%+v", model, requestContext)
		}
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"})
		callback(conversation.Event{Kind: conversation.EventDone, Usage: &conversation.Usage{InputTokens: conversation.Int64(4), OutputTokens: conversation.Int64(2)}})
		return transport.Response{StatusCode: 200}, nil
	})
	turn := agent.RunTurn(context.Background(), agent.TurnOptions{
		Session: statePointer(state), Stream: stream,
		Observe: func(event conversation.Event) { observed = append(observed, event) },
	})
	if turn.Assembly.State != conversation.TurnDone || len(turn.Assembly.Items) != 1 ||
		turn.Assembly.Items[0].Text == nil || *turn.Assembly.Items[0].Text != "answer" {
		t.Fatalf("turn output was not assembled: %+v", turn.Assembly)
	}
	if turn.Usage == nil || *turn.Usage.InputTokens != 4 || *turn.Usage.OutputTokens != 2 || len(turn.Attempts) != 1 {
		t.Fatalf("terminal usage was not captured: %+v", turn)
	}
	if len(observed) != 2 || observed[0].Kind != conversation.EventTextDelta || observed[1].Kind != conversation.EventDone {
		t.Fatalf("stream observer did not receive events in order: %+v", observed)
	}
}

func TestRunTurnRetryDropsPartialAssemblyButKeepsAttemptUsage(t *testing.T) {
	state := agent.NewSession()
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "discard me"})
		callback(conversation.Event{Kind: conversation.EventRetry, Usage: &conversation.Usage{InputTokens: conversation.Int64(10)}, DelayMS: 1})
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "keep me"})
		callback(conversation.Event{Kind: conversation.EventDone, Usage: &conversation.Usage{InputTokens: conversation.Int64(20), OutputTokens: conversation.Int64(3)}})
		return transport.Response{StatusCode: 200}, nil
	})
	turn := agent.RunTurn(context.Background(), agent.TurnOptions{Session: state, Stream: stream})
	if turn.Assembly.State != conversation.TurnDone || len(turn.Assembly.Items) != 1 ||
		turn.Assembly.Items[0].Text == nil || *turn.Assembly.Items[0].Text != "keep me" {
		t.Fatalf("retry should reset partial content: %+v", turn.Assembly)
	}
	if len(turn.Attempts) != 2 || turn.Attempts[0].Usage == nil || *turn.Attempts[0].Usage.InputTokens != 10 ||
		turn.Attempts[1].Usage == nil || *turn.Attempts[1].Usage.InputTokens != 20 || !turn.HasUsage() {
		t.Fatalf("retry and terminal usage were not retained separately: %+v", turn.Attempts)
	}
}

func TestRunTurnCapsAttemptDetailsAndFoldsOverflowUsage(t *testing.T) {
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		for index := 0; index < 10; index++ {
			usage := &conversation.Usage{
				InputTokens: conversation.Int64(1), OutputTokens: conversation.Int64(1),
				CachedTokens: conversation.Int64(1), CacheWriteTokens: conversation.Int64(1),
				CacheWrite1HTokens: conversation.Int64(1),
			}
			if index != 8 {
				usage.Cost = conversation.Float64(0.01)
			}
			callback(conversation.Event{Kind: conversation.EventRetry, Usage: usage})
		}
		callback(conversation.Event{
			Kind: conversation.EventDone, Usage: &conversation.Usage{
				InputTokens: conversation.Int64(1), OutputTokens: conversation.Int64(1),
				CachedTokens: conversation.Int64(1), CacheWriteTokens: conversation.Int64(1),
				CacheWrite1HTokens: conversation.Int64(1),
				Cost:               conversation.Float64(0.01),
			}, Response: &conversation.StreamResponse{Model: conversation.String("terminal-model")},
		})
		return transport.Response{StatusCode: 200}, nil
	})

	turn := agent.RunTurn(context.Background(), agent.TurnOptions{Stream: stream})
	if len(turn.Attempts) != 8 {
		t.Fatalf("attempt detail count = %d, want upstream cap of 8", len(turn.Attempts))
	}
	for index := 0; index < 7; index++ {
		if turn.Attempts[index].Usage == nil || *turn.Attempts[index].Usage.InputTokens != 1 {
			t.Fatalf("attempt %d was changed by overflow aggregation: %+v", index, turn.Attempts[index])
		}
	}
	last := turn.Attempts[7]
	if last.Usage == nil || last.Usage.InputTokens == nil || *last.Usage.InputTokens != 4 ||
		last.Usage.OutputTokens == nil || *last.Usage.OutputTokens != 4 ||
		last.Usage.CachedTokens == nil || *last.Usage.CachedTokens != 4 ||
		last.Usage.CacheWriteTokens == nil || *last.Usage.CacheWriteTokens != 4 ||
		last.Usage.CacheWrite1HTokens == nil || *last.Usage.CacheWrite1HTokens != 4 {
		t.Fatalf("overflow attempt usage = %+v, want four attempts folded into the last record", last.Usage)
	}
	if last.Usage.Cost == nil || *last.Usage.Cost >= 0 {
		t.Fatalf("mixed priced and unpriced overflow should not retain a partial exact cost: %+v", last.Usage)
	}
	if last.Response != nil {
		t.Fatalf("overflow aggregation replaced the capped record's response: %+v", last.Response)
	}
}

func TestAbsorbAbortKeepsPartialTextMarkedAndDropsUnrunCallOnProviderError(t *testing.T) {
	state := agent.NewSession()
	turn := completedStreamTurn(
		conversation.Event{Kind: conversation.EventTextDelta, Text: "partial"},
		conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"},
		conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")},
	)
	outcome := agent.AbsorbAbort(state, &turn, agent.AbortProviderError)
	if !outcome.MarkerPlaced || len(state.Items) != 1 || state.Items[0].Kind != conversation.KindAssistant ||
		state.Items[0].Text == nil || *state.Items[0].Text != "partial\n"+agent.InterruptMarker ||
		state.Items[0].Origin != conversation.OriginInterrupted {
		t.Fatalf("provider failure repair should retain only marked assistant text: outcome=%+v items=%+v", outcome, state.Items)
	}
	if outcome.ItemsFrom != 0 || outcome.ItemsTo != 1 {
		t.Fatalf("streamed range should exclude synthesized repair: %+v", outcome)
	}
}

func TestAbsorbAbortPairsCompletedCallsAndEvaporatesThinkingOnlyCancel(t *testing.T) {
	state := agent.NewSession()
	turn := completedStreamTurn(
		conversation.Event{Kind: conversation.EventReasoningItem, JSON: conversation.String(`{"sig":"x"}`)},
		conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"},
		conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")},
	)
	outcome := agent.AbsorbAbort(state, &turn, agent.AbortUserCancel)
	if !outcome.MarkerPlaced || len(state.Items) != 3 || state.Items[0].Kind != conversation.KindReasoning ||
		state.Items[1].Kind != conversation.KindToolCall || state.Items[2].Kind != conversation.KindToolResult ||
		state.Items[2].CallID == nil || *state.Items[2].CallID != "c1" || state.Items[2].Output == nil ||
		*state.Items[2].Output != agent.InterruptMarker || state.Items[2].Origin != conversation.OriginSkipped {
		t.Fatalf("cancel repair must preserve sealed reasoning and pair calls: outcome=%+v items=%+v", outcome, state.Items)
	}
	if outcome.ItemsTo != 2 {
		t.Fatalf("range must stop before synthesized tool result: %+v", outcome)
	}

	thinking := completedStreamTurn(conversation.Event{Kind: conversation.EventReasoningDelta, Text: "unfinished"})
	thinkingState := agent.NewSession()
	thinkingOutcome := agent.AbsorbAbort(thinkingState, &thinking, agent.AbortUserCancel)
	if thinkingOutcome.MarkerPlaced || len(thinkingState.Items) != 0 || thinkingOutcome.ItemsFrom != thinkingOutcome.ItemsTo {
		t.Fatalf("thinking-only cancellation should leave no history: outcome=%+v items=%+v", thinkingOutcome, thinkingState.Items)
	}
}

func TestRunTurnErrorCapturesDiagnosticAndPartialUsage(t *testing.T) {
	state := agent.NewSession()
	streamFailure := errors.New("stream failed")
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "partial"})
		callback(conversation.Event{Kind: conversation.EventError, ErrorMessage: "provider unavailable", Usage: &conversation.Usage{InputTokens: conversation.Int64(8)}})
		return transport.Response{}, streamFailure
	})
	turn := agent.RunTurn(context.Background(), agent.TurnOptions{Session: state, Stream: stream})
	if turn.Assembly.State != conversation.TurnFailed || turn.ErrorMessage != "provider unavailable" || !turn.HasUsage() || turn.Usage == nil || *turn.Usage.InputTokens != 8 {
		t.Fatalf("provider error state was not captured: %+v", turn)
	}
}

func TestRunLoopRefusesUndeclaredToolsThenContinuesToFinalAnswer(t *testing.T) {
	state := agent.NewSession()
	state.AddUser("read a file")
	var calls int
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		calls++
		if calls == 1 {
			callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
			callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
		} else {
			callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "finished"})
		}
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{Session: state, Stream: stream})
	if result.Outcome != agent.LoopComplete || result.Turns != 2 || calls != 2 {
		t.Fatalf("loop did not continue after a refused tool: result=%+v calls=%d", result, calls)
	}
	refused := false
	final := false
	for _, item := range state.Items {
		refused = refused || item.Kind == conversation.KindToolResult && item.Origin == conversation.OriginRefused && item.Output != nil
		final = final || item.Kind == conversation.KindAssistant && item.Text != nil && *item.Text == "finished"
	}
	if !refused || !final {
		t.Fatalf("tool call/result pairing or final answer missing: %+v", state.Items)
	}
}

func TestRunLoopInjectsFinishedTaskNotesBeforeTheNextRequest(t *testing.T) {
	state := agent.NewSession()
	state.AddUser("start")
	note := "[task t1 finished (exit 0) after 51ms; 8B output pending]"
	collectCalls, noteCalls, earlyFlushes := 0, 0, 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		requestContext coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		found := false
		for _, item := range requestContext.Items {
			if item.Origin == conversation.OriginTaskNote && item.Text != nil && *item.Text == note {
				found = true
			}
		}
		if !found {
			t.Fatalf("task note was missing from provider context: %+v", requestContext.Items)
		}
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "done"})
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream, MaxTurns: 1,
		CollectTaskNotes: func() string {
			collectCalls++
			if collectCalls == 1 {
				return note
			}
			return ""
		},
		TaskNote: func(got string) {
			if got != note {
				t.Errorf("task note callback = %q, want %q", got, note)
			}
			noteCalls++
		},
		Flush: func() {
			for _, item := range state.Items {
				if item.Origin == conversation.OriginTaskNote {
					earlyFlushes++
					break
				}
			}
		},
	})
	if result.Outcome != agent.LoopComplete || noteCalls != 1 || earlyFlushes < 1 {
		t.Fatalf("task note loop outcome=%v note callbacks=%d pre-request flushes=%d", result.Outcome, noteCalls, earlyFlushes)
	}
	noteIndex, answerIndex := -1, -1
	for index, item := range state.Items {
		if item.Origin == conversation.OriginTaskNote {
			noteIndex = index
		}
		if item.Kind == conversation.KindAssistant && item.Text != nil && *item.Text == "done" {
			answerIndex = index
		}
	}
	if noteIndex < 0 || answerIndex <= noteIndex {
		t.Fatalf("task note and response ordering = %+v", state.Items)
	}
}

func TestRunLoopExecutesAdvertisedToolAndStopsAtMaxTurns(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	state.AddUser("read a file")
	var calls int
	toolRuns := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		calls++
		callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
		callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream, MaxTurns: 1,
		ToolCall: func(call conversation.Item, action agent.ToolAction, _ int) conversation.Item {
			toolRuns++
			if action != agent.ToolRun || call.CallID == nil || *call.CallID != "c1" {
				t.Errorf("unexpected tool dispatch: call=%+v action=%v", call, action)
			}
			return agent.ToolResult(call, "read result", conversation.Origin(""))
		},
	})
	if result.Outcome != agent.LoopMaxTurns || result.Turns != 1 || calls != 1 || toolRuns != 1 {
		t.Fatalf("max-turn boundary or tool execution differs: result=%+v calls=%d tools=%d", result, calls, toolRuns)
	}
	if result.FinalItemsFrom != 2 || result.FinalItemsTo != 3 {
		t.Fatalf("final streamed range should include only the call: %+v", result)
	}
}

func TestRunLoopExecutesBuiltInFileToolRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolRegistry := coretools.New(coretools.Options{})
	state := agent.NewSession()
	state.Tools = toolRegistry.Definitions()
	state.AddUser("read the file")
	arguments, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		requestContext coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		calls++
		if calls == 1 {
			callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("read-1"), Name: "read"})
			callback(conversation.Event{Kind: conversation.EventToolCallDelta, ID: conversation.String("read-1"), Delta: string(arguments)})
			callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("read-1")})
		} else {
			found := false
			for _, item := range requestContext.Items {
				if item.Kind == conversation.KindToolResult && item.Output != nil && *item.Output == "     1→hello\n" {
					found = true
				}
			}
			if !found {
				t.Errorf("built-in read result missing from next request: %+v", requestContext.Items)
			}
			callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "done"})
		}
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{Session: state, Stream: stream, ToolRegistry: toolRegistry})
	if result.Outcome != agent.LoopComplete || result.Turns != 2 || calls != 2 {
		t.Fatalf("built-in tool did not participate in the loop: result=%+v calls=%d", result, calls)
	}
}

func TestRunLoopPauseAfterPreemptedRequestLeavesContinuedTailUntouched(t *testing.T) {
	state := agent.NewSession()
	state.AddUser("continue")
	original := len(state.Items)
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, _ func(conversation.Event),
	) (transport.Response, error) {
		return transport.Response{Cancelled: true}, transport.ErrCancelled
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream, Continued: true,
		Checkpoint: func() agent.Signal { return agent.SignalPause },
	})
	if result.Outcome != agent.LoopPaused || len(state.Items) != original || result.AbortMarkerPlaced {
		t.Fatalf("empty paused continuation should be reissued without a boundary or marker: result=%+v items=%+v", result, state.Items)
	}
}

func TestRunLoopProviderFailureRepairsPartialTextAndUsage(t *testing.T) {
	state := agent.NewSession()
	state.AddUser("start")
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "partial"})
		callback(conversation.Event{Kind: conversation.EventError, ErrorMessage: "failed", Usage: &conversation.Usage{InputTokens: conversation.Int64(3)}})
		return transport.Response{}, errors.New("failed")
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{Session: state, Stream: stream, Continued: true})
	if result.Outcome != agent.LoopProviderError || result.ErrorMessage != "failed" || !result.AbortMarkerPlaced {
		t.Fatalf("provider failure was not repaired: %+v", result)
	}
	if len(state.Items) < 4 || state.Items[2].Kind != conversation.KindTurnBoundary || state.Items[3].Origin != conversation.OriginInterrupted {
		t.Fatalf("continued partial turn should be separated and marked: %+v", state.Items)
	}
	if state.Items[len(state.Items)-1].Kind != conversation.KindTurnUsage {
		t.Fatalf("reported attempt usage should be appended: %+v", state.Items)
	}
}

func statePointer(state agent.Session) *agent.Session { return &state }

func completedStreamTurn(events ...conversation.Event) agent.LoopTurn {
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		for _, event := range events {
			callback(event)
		}
		callback(conversation.Event{Kind: conversation.EventError, ErrorMessage: "stop"})
		return transport.Response{}, errors.New("stop")
	})
	return agent.RunTurn(context.Background(), agent.TurnOptions{Session: agent.NewSession(), Stream: stream})
}

func TestRunLoopAbortSkipsRestOfToolBatchWithoutDuplicateMarker(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	var actions []agent.ToolAction
	checkpointCalls := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		for _, id := range []string{"c1", "c2"} {
			callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String(id), Name: "read"})
			callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String(id)})
		}
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream,
		Checkpoint: func() agent.Signal {
			checkpointCalls++
			if checkpointCalls == 2 {
				return agent.SignalAbort
			}
			return agent.SignalNone
		},
		ToolCall: func(call conversation.Item, action agent.ToolAction, _ int) conversation.Item {
			actions = append(actions, action)
			return agent.ToolResult(call, agent.SkippedResult, conversation.OriginSkipped)
		},
	})
	if result.Outcome != agent.LoopInterrupted || !result.AbortMarkerPlaced || len(actions) != 2 || actions[0] != agent.ToolSkip || actions[1] != agent.ToolSkip {
		t.Fatalf("abort should skip the entire remaining batch: result=%+v actions=%v", result, actions)
	}
	markers := 0
	for _, item := range state.Items {
		if item.Origin == conversation.OriginInterrupted && item.Text != nil && *item.Text == agent.InterruptMarker {
			markers++
		}
	}
	if markers != 0 {
		t.Fatalf("skipped tool results already mark the interrupted tail; got extra marker: %+v", state.Items)
	}
}

func TestRunLoopPauseFinishesToolBatchBeforeReturning(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	calls := 0
	checkpoints := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		calls++
		callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
		callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream,
		Checkpoint: func() agent.Signal {
			checkpoints++
			if checkpoints == 1 {
				return agent.SignalPause
			}
			return agent.SignalNone
		},
		ToolCall: func(call conversation.Item, action agent.ToolAction, _ int) conversation.Item {
			return agent.ToolResult(call, "completed", conversation.Origin(""))
		},
	})
	if result.Outcome != agent.LoopPaused || calls != 1 || len(state.Items) != 3 ||
		state.Items[1].Kind != conversation.KindToolResult || state.Items[1].Output == nil || *state.Items[1].Output != "completed" {
		t.Fatalf("pause should land after the full tool batch: result=%+v items=%+v", result, state.Items)
	}
}

func TestRunLoopCompactsAtToolSeamAndReportsLatestContext(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	calls := 0
	compactions := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		calls++
		if calls == 1 {
			callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
			callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
			callback(conversation.Event{Kind: conversation.EventDone, Usage: &conversation.Usage{InputTokens: conversation.Int64(2), OutputTokens: conversation.Int64(3)}})
		} else {
			callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "done"})
			callback(conversation.Event{Kind: conversation.EventDone})
		}
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Provider: &runtime.Provider{}, Stream: stream,
		ToolCall: func(call conversation.Item, _ agent.ToolAction, _ int) conversation.Item {
			return agent.ToolResult(call, "ok", conversation.Origin(""))
		},
		ShouldCompact: func(tokens, _ int64) bool { return tokens == 5 },
		Compact:       func() { compactions++ },
	})
	if result.Outcome != agent.LoopComplete || result.Turns != 2 || calls != 2 || compactions != 1 || result.LastContextTokens != -1 {
		t.Fatalf("tool seam compaction or latest context differs: result=%+v calls=%d compactions=%d", result, calls, compactions)
	}
}

func TestRunLoopObservesEveryToolCallBeforeDispatchingAny(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	var order []string
	turnCalls := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		turnCalls++
		if turnCalls == 1 {
			for _, id := range []string{"c1", "c2"} {
				callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String(id), Name: "read"})
				callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String(id)})
			}
		} else {
			callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "finished"})
		}
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream,
		ToolSeen: func(call conversation.Item) { order = append(order, "seen:"+*call.CallID) },
		ToolCall: func(call conversation.Item, _ agent.ToolAction, _ int) conversation.Item {
			order = append(order, "run:"+*call.CallID)
			return agent.ToolResult(call, "ok", conversation.Origin(""))
		},
	})
	want := []string{"seen:c1", "seen:c2", "run:c1", "run:c2"}
	if result.Outcome != agent.LoopComplete || !reflect.DeepEqual(order, want) {
		t.Fatalf("tool observation order = %v, want %v (result=%+v)", order, want, result)
	}
}

// Upstream repairs a provider failure with turn_keep_text, which drops every non-assistant item,
// so the observed repair range never holds a tool call and no skipped result is synthesized.
func TestRunLoopProviderFailureDropsUnrunCallBeforeObservation(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	var seen []string
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "partial"})
		callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
		callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
		callback(conversation.Event{Kind: conversation.EventError, ErrorMessage: "boom"})
		return transport.Response{}, errors.New("boom")
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream,
		ToolSeen: func(call conversation.Item) { seen = append(seen, *call.CallID) },
	})
	if result.Outcome != agent.LoopProviderError || len(seen) != 0 {
		t.Fatalf("provider repair keeps text only, so no call may be observed: result=%+v seen=%v", result, seen)
	}
	for _, item := range state.Items {
		if item.Kind == conversation.KindToolCall || item.Kind == conversation.KindToolResult {
			t.Fatalf("provider repair must drop the unrun call and synthesize no result: %+v", state.Items)
		}
	}
	if result.FinalItemsFrom >= result.FinalItemsTo {
		t.Fatalf("kept partial text should occupy the repaired range: from=%d to=%d",
			result.FinalItemsFrom, result.FinalItemsTo)
	}
	for index := result.FinalItemsFrom; index < result.FinalItemsTo; index++ {
		if state.Items[index].Kind != conversation.KindAssistant {
			t.Fatalf("repaired range holds a non-assistant item: %+v", state.Items[index])
		}
	}
}

func TestRunLoopObservesToolCallsOnUserCancelWithoutDispatching(t *testing.T) {
	state := agent.NewSession()
	state.Tools = []coreprovider.ToolDefinition{{Name: "read"}}
	var seen []string
	dispatched := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("c1"), Name: "read"})
		callback(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("c1")})
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream,
		Checkpoint: func() agent.Signal { return agent.SignalAbort },
		ToolSeen:   func(call conversation.Item) { seen = append(seen, *call.CallID) },
		ToolCall: func(call conversation.Item, _ agent.ToolAction, _ int) conversation.Item {
			dispatched++
			return agent.ToolResult(call, agent.SkippedResult, conversation.OriginSkipped)
		},
	})
	if result.Outcome != agent.LoopInterrupted || !result.AbortMarkerPlaced || dispatched != 0 {
		t.Fatalf("cancelled turn must not dispatch tools: result=%+v dispatched=%d", result, dispatched)
	}
	if len(seen) != 1 || seen[0] != "c1" {
		t.Fatalf("cancelled turn should still report its streamed call: seen=%v", seen)
	}
}

func TestRunLoopObservesNoCallsForToolFreeTurn(t *testing.T) {
	state := agent.NewSession()
	seen := 0
	stream := agent.StreamFunc(func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		_ coreprovider.Context, _ client.StreamOptions, _ func() bool, callback func(conversation.Event),
	) (transport.Response, error) {
		callback(conversation.Event{Kind: conversation.EventTextDelta, Text: "just text"})
		callback(conversation.Event{Kind: conversation.EventDone})
		return transport.Response{StatusCode: 200}, nil
	})
	result := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session: state, Stream: stream, ToolSeen: func(conversation.Item) { seen++ },
	})
	if result.Outcome != agent.LoopComplete || seen != 0 {
		t.Fatalf("tool-free turn reported calls: result=%+v seen=%d", result, seen)
	}
}

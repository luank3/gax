package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

func TestCompactConversationAddsSummarySeedAndCompactionUsage(t *testing.T) {
	state := &agent.Session{
		Model: conversation.String("model-a"), ProviderID: conversation.String("provider-a"),
		CompactionDeferred: true, ResumeReason: agent.ResumeReasonMaxTurns,
		Tools: []coreprovider.ToolDefinition{{Name: "bash"}},
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("fix the bug")},
			{Kind: conversation.KindAssistant, Text: conversation.String("I will inspect the code")},
		},
	}
	provider := &runtime.Provider{}
	streamCalls := 0
	stream := func(_ context.Context, _ *transport.Client, _ *runtime.Provider, model string,
		request coreprovider.Context, _ client.StreamOptions, _ func() bool,
		observe func(conversation.Event),
	) (transport.Response, error) {
		streamCalls++
		if model != "model-a" || len(request.Items) == 0 || request.Items[len(request.Items)-1].Text == nil ||
			!strings.Contains(*request.Items[len(request.Items)-1].Text, "Additional focus for this summary:\nkeep exact identifiers") ||
			len(request.Tools) != 1 {
			t.Fatalf("summary request did not preserve context, tools, focus, and model: %+v", request)
		}
		observe(conversation.Event{Kind: conversation.EventTextDelta, Text: "## Goal\n- Fix the bug"})
		observe(conversation.Event{Kind: conversation.EventDone,
			Usage: &conversation.Usage{InputTokens: conversation.Int64(80), OutputTokens: conversation.Int64(20)}})
		return transport.Response{}, nil
	}
	result := agent.CompactConversation(agent.CompactOptions{
		Context: context.Background(), Session: state, Provider: provider,
		Model: "model-a", Focus: "keep exact identifiers", Stream: stream,
	})
	if result.Outcome != agent.CompactComplete || result.Attempts != 1 || streamCalls != 1 {
		t.Fatalf("compact result = %+v, calls=%d", result, streamCalls)
	}
	if len(state.Items) != 5 || state.Items[2].Kind != conversation.KindTurnBoundary ||
		state.Items[3].Kind != conversation.KindUser || state.Items[3].Origin != conversation.OriginCompactSeed ||
		state.Items[3].Text == nil || !strings.Contains(*state.Items[3].Text, "## Goal\n- Fix the bug") ||
		state.Items[4].Kind != conversation.KindTurnUsage || state.Items[4].Origin != conversation.OriginCompaction {
		t.Fatalf("compaction did not append a boundary, summary seed, and usage footer: %+v", state.Items)
	}
	if state.CompactionDeferred || state.ResumeReason != agent.ResumeReasonNone {
		t.Fatalf("successful compaction did not clear deferred state: %+v", state)
	}
}

func TestCompactOverThresholdUsesCeilingAndValidatesInputs(t *testing.T) {
	for _, test := range []struct {
		contextTokens int64
		contextLimit  int64
		threshold     int
		want          bool
	}{
		{contextTokens: 84, contextLimit: 100, threshold: 85, want: false},
		{contextTokens: 85, contextLimit: 100, threshold: 85, want: true},
		{contextTokens: 9, contextLimit: 1000, threshold: 1, want: false},
		{contextTokens: 10, contextLimit: 1000, threshold: 1, want: true},
		{contextTokens: -1, contextLimit: 1000, threshold: 85, want: false},
		{contextTokens: 100, contextLimit: 0, threshold: 85, want: false},
		{contextTokens: 100, contextLimit: 1000, threshold: 0, want: false},
	} {
		if got := agent.CompactOverThreshold(test.contextTokens, test.contextLimit, test.threshold); got != test.want {
			t.Fatalf("CompactOverThreshold(%d, %d, %d) = %t, want %t", test.contextTokens, test.contextLimit, test.threshold, got, test.want)
		}
	}
}

func TestCompactConversationRejectsToolCallsAndRetriesWithoutExecutingThem(t *testing.T) {
	state := &agent.Session{
		Model: conversation.String("model-a"), Tools: []coreprovider.ToolDefinition{{Name: "bash"}},
		Items: []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("inspect")}},
	}
	streamCalls := 0
	stream := func(_ context.Context, _ *transport.Client, _ *runtime.Provider, _ string,
		request coreprovider.Context, _ client.StreamOptions, _ func() bool,
		observe func(conversation.Event),
	) (transport.Response, error) {
		streamCalls++
		if streamCalls == 1 {
			observe(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("call_1"), Name: "bash"})
			observe(conversation.Event{Kind: conversation.EventToolCallDelta, ID: conversation.String("call_1"), Delta: `{"command":"printf SHOULD_NOT_EXECUTE"}`})
			observe(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("call_1")})
			observe(conversation.Event{Kind: conversation.EventDone})
			return transport.Response{}, nil
		}
		foundRejection := false
		for _, item := range request.Items {
			if item.Kind == conversation.KindTurnBoundary {
				t.Fatalf("compact retry inserted a turn boundary: %+v", request.Items)
			}
			if item.Kind == conversation.KindToolResult && item.Output != nil && strings.Contains(*item.Output, "Tool calls are disabled while summarizing") {
				foundRejection = true
			}
		}
		if !foundRejection {
			t.Fatalf("retry omitted the tool rejection: %+v", request.Items)
		}
		observe(conversation.Event{Kind: conversation.EventTextDelta, Text: "summary"})
		observe(conversation.Event{Kind: conversation.EventDone,
			Usage: &conversation.Usage{InputTokens: conversation.Int64(20), OutputTokens: conversation.Int64(5)}})
		return transport.Response{}, nil
	}
	result := agent.CompactConversation(agent.CompactOptions{
		Session: state, Provider: &runtime.Provider{}, Model: "model-a", Stream: stream,
	})
	if result.Outcome != agent.CompactComplete || result.Attempts != 2 || streamCalls != 2 {
		t.Fatalf("compact retry result = %+v, calls=%d", result, streamCalls)
	}
	if len(state.Items) != 5 || state.Items[1].Kind != conversation.KindTurnUsage ||
		state.Items[1].Origin != conversation.OriginCompaction || state.Items[2].Kind != conversation.KindTurnBoundary ||
		state.Items[3].Origin != conversation.OriginCompactSeed || state.Items[4].Origin != conversation.OriginCompaction {
		t.Fatalf("rejected scratch response leaked or summary was not committed: %+v", state.Items)
	}
}

func TestCompactConversationReportsPreconditionsAndProviderFailure(t *testing.T) {
	state := &agent.Session{Model: conversation.String("model-a"), Items: []conversation.Item{{Kind: conversation.KindUser}}}
	if result := agent.CompactConversation(agent.CompactOptions{Session: state}); result.Outcome != agent.CompactNoProvider {
		t.Fatalf("missing provider outcome = %+v", result)
	}
	if result := agent.CompactConversation(agent.CompactOptions{Session: &agent.Session{Items: state.Items}, Provider: &runtime.Provider{}}); result.Outcome != agent.CompactNoModel {
		t.Fatalf("missing model outcome = %+v", result)
	}
	state.Model = conversation.String("model-a")
	if result := agent.CompactConversation(agent.CompactOptions{Session: &agent.Session{Model: state.Model}, Provider: &runtime.Provider{}}); result.Outcome != agent.CompactEmpty {
		t.Fatalf("empty session outcome = %+v", result)
	}
	providerErr := errors.New("stream failed")
	state.Items = []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("prompt")}}
	result := agent.CompactConversation(agent.CompactOptions{
		Session: state, Provider: &runtime.Provider{}, Model: "model-a",
		Stream: func(context.Context, *transport.Client, *runtime.Provider, string, coreprovider.Context,
			client.StreamOptions, func() bool, func(conversation.Event),
		) (transport.Response, error) {
			return transport.Response{}, providerErr
		},
	})
	if result.Outcome != agent.CompactProviderError || result.ErrorMessage != providerErr.Error() || len(state.Items) != 1 {
		t.Fatalf("provider failure altered the conversation or lost its error: result=%+v items=%+v", result, state.Items)
	}
}

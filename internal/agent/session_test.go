package agent_test

import (
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/session"
)

func TestSessionContextStartsAtNewestCompactionSeed(t *testing.T) {
	state := agent.Session{
		SystemPrompt: "system",
		Effort:       conversation.String("high"),
		Tools:        []coreprovider.ToolDefinition{{Name: "read"}},
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("old")},
			{Kind: conversation.KindAssistant, Text: conversation.String("old answer")},
			{Kind: conversation.KindUser, Text: conversation.String("seed"), Origin: conversation.OriginCompactSeed},
			{Kind: conversation.KindAssistant, Text: conversation.String("new answer")},
		},
	}
	context := state.Context()
	if context.SystemPrompt != "system" || context.ImageInput != -1 || len(context.Tools) != 1 ||
		context.Effort == nil || *context.Effort != "high" || len(context.Items) != 2 ||
		context.Items[0].Text == nil || *context.Items[0].Text != "seed" {
		t.Fatalf("provider context does not match the compacted session: %+v", context)
	}
}

func TestSessionAddsTypedPromptsAndContinuationBoundaries(t *testing.T) {
	state := agent.Session{}
	state.AddUser("hello")
	state.AddBoundary()
	state.AddContinuation()
	if len(state.Items) != 5 || state.Items[0].Kind != conversation.KindTurnBoundary ||
		!conversation.IsTypedPrompt(state.Items[1]) || state.Items[2].Kind != conversation.KindTurnBoundary ||
		state.Items[3].Kind != conversation.KindTurnBoundary || state.Items[4].Origin != conversation.OriginContinuation {
		t.Fatalf("session additions have wrong boundary structure: %+v", state.Items)
	}
	if got := state.ResumeTail(); got != agent.ResumeTailUser {
		t.Fatalf("continuation tail should resume as a user turn, got %v", got)
	}
}

func TestSessionRetireMovesItemsAndResetClearsHistory(t *testing.T) {
	state := agent.Session{
		LastUserTurnMS: 80, WorkedMS: 140, CompactionDeferred: true, ResumeReason: agent.ResumeReasonMaxTurns,
	}
	state.AddUser("one")
	state.AddUser("two")
	state.Retire(conversation.UserTurnCut(state.Items, 1))
	if len(state.Items) != 2 || len(state.Retired) != 2 || state.LastUserTurnMS != -1 ||
		state.CompactionDeferred || state.ResumeReason != agent.ResumeReasonNone {
		t.Fatalf("retirement did not move the removed suffix: live=%+v retired=%+v last=%d", state.Items, state.Retired, state.LastUserTurnMS)
	}
	state.Reset()
	if len(state.Items) != 0 || len(state.Retired) != 0 || state.WorkedMS != 0 || state.LastUserTurnMS != -1 ||
		state.CompactionDeferred || state.ResumeReason != agent.ResumeReasonNone {
		t.Fatalf("reset did not clear conversation accounting: %+v", state)
	}
}

func TestSessionAdoptTakesLoadedItemsAndAccounting(t *testing.T) {
	loaded := session.Loaded{
		Items:          []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("prompt")}},
		Retired:        []conversation.Item{{Kind: conversation.KindAssistant, Text: conversation.String("retired")}},
		WorkedMS:       123,
		LastUserTurnMS: conversation.Int64(45),
	}
	state := agent.Session{Items: []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("old")}}}
	state.Adopt(&loaded)
	if len(loaded.Items) != 0 || len(loaded.Retired) != 0 || len(state.Items) != 1 || len(state.Retired) != 1 ||
		state.WorkedMS != 123 || state.LastUserTurnMS != 45 {
		t.Fatalf("session adopt did not take loaded state: state=%+v loaded=%+v", state, loaded)
	}
}

func TestSessionMarksInterruptOnceAndClassifiesResumeTail(t *testing.T) {
	state := agent.Session{}
	if state.ResumeTail() != agent.ResumeTailEmpty {
		t.Fatal("empty session should have empty resume tail")
	}
	state.AddUser("prompt")
	if state.ResumeTail() != agent.ResumeTailUser {
		t.Fatalf("unanswered user turn should resume from user input: %+v", state.Items)
	}
	state.ResumeFromTail()
	if state.ResumeReason != agent.ResumeReasonInterrupted || state.ResumeReason.Placeholder() != "enter to continue" {
		t.Fatalf("unanswered prompt did not restore empty-submit behavior: reason=%v", state.ResumeReason)
	}
	state.MarkInterrupt()
	state.Items = append(state.Items, conversation.Item{Kind: conversation.KindTurnUsage}, conversation.Item{Kind: conversation.KindTurnBoundary})
	state.MarkInterrupt()
	markers := 0
	for _, item := range state.Items {
		if item.Origin == conversation.OriginInterrupted && item.Text != nil && *item.Text == agent.InterruptMarker {
			markers++
		}
	}
	if markers != 1 || state.ResumeTail() != agent.ResumeTailMarked {
		t.Fatalf("interrupt marker was duplicated or not recognized: %+v", state.Items)
	}
}

func TestSessionAbsorbAddsReasoningProvenanceAndReportsCalls(t *testing.T) {
	turn := &conversation.Turn{}
	turn.Reset()
	turn.Consume(conversation.Event{Kind: conversation.EventReasoningItem, JSON: conversation.String(`{"secret":true}`)})
	turn.Consume(conversation.Event{Kind: conversation.EventToolCallStart, ID: conversation.String("call-1"), Name: "read"})
	turn.Consume(conversation.Event{Kind: conversation.EventToolCallEnd, ID: conversation.String("call-1")})
	state := agent.Session{ProviderID: conversation.String("stable-provider"), Model: conversation.String("model-a")}
	result := state.Absorb(turn)
	if !result.HadToolCall || result.ItemsFrom != 0 || len(state.Items) != 2 || len(turn.Items) != 0 {
		t.Fatalf("turn items were not transferred: result=%+v state=%+v turn=%+v", result, state.Items, turn.Items)
	}
	if state.Items[0].Provider == nil || *state.Items[0].Provider != "stable-provider" ||
		state.Items[0].Model == nil || *state.Items[0].Model != "model-a" {
		t.Fatalf("reasoning provenance is missing: %+v", state.Items[0])
	}
}

func TestSessionUsageQueriesIgnoreCompactionFootersAndInheritedRetiredUsage(t *testing.T) {
	input, output := int64(100), int64(50)
	state := agent.Session{Items: []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("seed"), Origin: conversation.OriginCompactSeed},
		{Kind: conversation.KindTurnUsage, Origin: conversation.OriginCompaction, Usage: &conversation.Usage{InputTokens: &input, OutputTokens: &output}},
		{Kind: conversation.KindUser, Text: conversation.String("new")},
		{Kind: conversation.KindTurnUsage, Usage: &conversation.Usage{InputTokens: &input, OutputTokens: &output}},
	}}
	if got := state.LastContextTokens(); got != 150 || !state.HasReportedUsage() {
		t.Fatalf("latest live usage was not selected: tokens=%d reported=%t", got, state.HasReportedUsage())
	}
	state.Retired = []conversation.Item{{Kind: conversation.KindTurnUsage, Inherited: true, Usage: &conversation.Usage{InputTokens: &input}}}
	state.Items = state.Items[:2]
	if state.LastContextTokens() != -1 || !state.HasReportedUsage() {
		t.Fatalf("compaction usage remains reportable but must not become the context snapshot: tokens=%d reported=%t", state.LastContextTokens(), state.HasReportedUsage())
	}
	state.Items = nil
	if state.HasReportedUsage() {
		t.Fatal("an inherited retired footer should not count as this session's usage")
	}
}

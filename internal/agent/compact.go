package agent

import (
	"context"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

const compactPrompt = "Summarize the conversation so far into a structured context checkpoint that lets the work continue without access to the full history above.\n" +
	"\n" +
	"CRITICAL: Respond with the summary text ONLY. Do not call any tools and do not continue the task — your entire reply is the summary.\n" +
	"\n" +
	"Use this exact Markdown structure, keeping every section even when empty:\n" +
	"\n" +
	"## Goal\n" +
	"- [what the user is ultimately trying to accomplish]\n" +
	"\n" +
	"## Constraints & Preferences\n" +
	"- [explicit user constraints, preferences, or requirements, or \"(none)\"]\n" +
	"\n" +
	"## Progress\n" +
	"### Done\n" +
	"- [completed work, or \"(none)\"]\n" +
	"### In Progress\n" +
	"- [work underway right now, or \"(none)\"]\n" +
	"### Blocked\n" +
	"- [blockers, or \"(none)\"]\n" +
	"\n" +
	"## Key Decisions\n" +
	"- [decision: brief rationale, or \"(none)\"]\n" +
	"\n" +
	"## Files\n" +
	"- [path: why it matters / what changed, or \"(none)\"]\n" +
	"\n" +
	"## Next Steps\n" +
	"- [ordered next actions, or \"(none)\"]\n" +
	"\n" +
	"## Critical Context\n" +
	"- [important technical facts, error strings, identifiers, commands, open questions, or \"(none)\"]\n" +
	"\n" +
	"Rules:\n" +
	"- Be terse: bullets, not prose paragraphs.\n" +
	"- Preserve exact file paths, function names, commands, and error strings.\n" +
	"- Do not mention that a summary was produced or that context was compacted."

const compactSeedPreamble = "The earlier part of this conversation was condensed to free up context. The summary below captures everything that happened before this point — treat it as established context and continue the work from here."
const compactToolRejection = "[rejected] Tool calls are disabled while summarizing. Respond with the summary text only — do not call any tools."
const compactMaxAttempts = 4

type CompactOutcome uint8

const (
	CompactComplete CompactOutcome = iota
	CompactCancelled
	CompactProviderError
	CompactNoSummary
	CompactNoProvider
	CompactNoModel
	CompactEmpty
)

type CompactOptions struct {
	Context       context.Context
	Session       *Session
	Provider      *runtime.Provider
	HTTPClient    *transport.Client
	Model         string
	SessionID     *string
	Focus         string
	StreamOptions client.StreamOptions
	Tick          func() bool
	Observe       func(conversation.Event)
	Stream        StreamFunc
}

type CompactResult struct {
	Outcome      CompactOutcome
	Attempts     int
	ErrorMessage string
}

// CompactOverThreshold reports whether usage reaches a percentage of the model's context window.
func CompactOverThreshold(contextTokens, contextLimit int64, thresholdPercent int) bool {
	if contextTokens < 0 || contextLimit <= 0 || thresholdPercent < 1 || thresholdPercent > 100 {
		return false
	}
	threshold := contextLimit / 100 * int64(thresholdPercent)
	remainder := contextLimit % 100 * int64(thresholdPercent)
	threshold += remainder / 100
	if remainder%100 != 0 {
		threshold++
	}
	return contextTokens >= threshold
}

// CompactConversation summarizes the current history without exposing its temporary request or
// rejected tool calls to the live session. A successful summary becomes the new context seed.
func CompactConversation(options CompactOptions) CompactResult {
	result := CompactResult{}
	if options.Provider == nil {
		result.Outcome = CompactNoProvider
		return result
	}
	if options.Session == nil {
		result.Outcome = CompactEmpty
		return result
	}
	model := options.Model
	if model == "" && options.Session.Model != nil {
		model = *options.Session.Model
	}
	if model == "" {
		result.Outcome = CompactNoModel
		return result
	}
	if len(options.Session.Items) == 0 {
		result.Outcome = CompactEmpty
		return result
	}

	working := *options.Session
	working.Model = conversation.String(model)
	working.Items = append([]conversation.Item(nil), options.Session.Items...)
	working.Retired = nil
	prompt := compactPrompt
	if options.Focus != "" {
		prompt += "\n\nAdditional focus for this summary:\n" + options.Focus
	}
	working.Append(conversation.Item{Kind: conversation.KindUser, Text: conversation.String(prompt)})
	baseCount := len(working.Items)

	loopResult := RunLoop(options.Context, LoopOptions{
		Session: &working, Provider: options.Provider, HTTPClient: options.HTTPClient,
		Model: model, SessionID: options.SessionID, StreamOptions: options.StreamOptions,
		MaxTurns: compactMaxAttempts, SuppressBoundaries: true, Tick: options.Tick, Observe: options.Observe,
		Stream: options.Stream,
		ToolCall: func(call conversation.Item, _ ToolAction, _ int) conversation.Item {
			return conversation.Item{
				Kind: conversation.KindToolResult, CallID: cloneString(call.CallID),
				ToolName: cloneString(call.ToolName), Output: conversation.String(compactToolRejection),
			}
		},
	})
	result.Attempts = loopResult.Turns
	result.ErrorMessage = loopResult.ErrorMessage

	var summary string
	if loopResult.Outcome == LoopComplete {
		for index := loopResult.FinalItemsFrom; index < loopResult.FinalItemsTo && index < len(working.Items); index++ {
			item := working.Items[index]
			if item.Kind == conversation.KindAssistant && item.Text != nil && *item.Text != "" {
				summary = *item.Text
			}
		}
	}

	var usage []conversation.Item
	for _, item := range working.Items[baseCount:] {
		if item.Kind == conversation.KindTurnUsage {
			item.Origin = conversation.OriginCompaction
			usage = append(usage, item)
		}
	}
	if summary == "" {
		options.Session.Items = append(options.Session.Items, usage...)
		switch loopResult.Outcome {
		case LoopInterrupted:
			result.Outcome = CompactCancelled
		case LoopProviderError:
			result.Outcome = CompactProviderError
		default:
			result.Outcome = CompactNoSummary
		}
		return result
	}

	if len(usage) > 1 {
		options.Session.Items = append(options.Session.Items, usage[:len(usage)-1]...)
	}
	options.Session.AddBoundary()
	options.Session.Append(conversation.Item{
		Kind: conversation.KindUser, Text: conversation.String(compactSeedPreamble + "\n\n" + summary),
		Origin: conversation.OriginCompactSeed,
	})
	if len(usage) > 0 {
		options.Session.Items = append(options.Session.Items, usage[len(usage)-1])
	}
	options.Session.CompactionDeferred = false
	options.Session.ResumeReason = ResumeReasonNone
	result.Outcome = CompactComplete
	return result
}

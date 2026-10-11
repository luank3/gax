package agent

import (
	"context"
	"errors"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/runtime"
	coretools "github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transport"
)

const (
	RefusedResult = "error: tool calls are disabled in this session"
	SkippedResult = InterruptMarker
)

type Signal uint8

const (
	SignalNone Signal = iota
	SignalPause
	SignalAbort
)

type ToolAction uint8

const (
	ToolRun ToolAction = iota
	ToolRefuse
	ToolSkip
)

type LoopOutcome uint8

const (
	LoopComplete LoopOutcome = iota
	LoopProviderError
	LoopInterrupted
	LoopPaused
	LoopMaxTurns
)

type LoopOptions struct {
	Session            *Session
	Provider           *runtime.Provider
	HTTPClient         *transport.Client
	Model              string
	SessionID          *string
	StreamOptions      client.StreamOptions
	Stream             StreamFunc
	MaxTurns           int
	Continued          bool
	SuppressBoundaries bool
	Observe            func(conversation.Event)
	Tick               func() bool
	TurnBegin          func()
	Checkpoint         func() Signal
	ToolSeen           func(conversation.Item)
	ToolCall           func(conversation.Item, ToolAction, int) conversation.Item
	CollectTaskNotes   func() string
	TaskNote           func(string)
	ToolRegistry       *coretools.Registry
	ShouldCompact      func(lastContextTokens, contextLimit int64) bool
	Compact            func()
	Flush              func()
}

type LoopResult struct {
	Outcome           LoopOutcome
	Turns             int
	LastContextTokens int64
	FinalItemsFrom    int
	FinalItemsTo      int
	AbortMarkerPlaced bool
	ErrorMessage      string
}

// RunLoop continues an already-appended user turn through provider responses and tool calls.
// Frontend behavior is supplied through hooks; the history and replay invariants stay here.
func RunLoop(ctx context.Context, options LoopOptions) LoopResult {
	result := LoopResult{Outcome: LoopMaxTurns, LastContextTokens: -1}
	if options.Session == nil {
		result.Outcome = LoopProviderError
		result.ErrorMessage = "agent session is unavailable"
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for turnNumber := 0; options.MaxTurns <= 0 || turnNumber < options.MaxTurns; turnNumber++ {
		owesBoundary := !options.SuppressBoundaries && (turnNumber > 0 || options.Continued)
		if options.CollectTaskNotes != nil {
			if note := options.CollectTaskNotes(); note != "" {
				options.Session.Append(conversation.Item{
					Kind: conversation.KindUser, Text: conversation.String(note), Origin: conversation.OriginTaskNote,
				})
				flushLoop(options)
				if options.TaskNote != nil {
					options.TaskNote(note)
				}
			}
		}
		if options.TurnBegin != nil {
			options.TurnBegin()
		}
		turn := RunTurn(ctx, TurnOptions{
			Session: options.Session, Provider: options.Provider, HTTPClient: options.HTTPClient,
			Model: options.Model, SessionID: options.SessionID, StreamOptions: options.StreamOptions,
			Tick: options.Tick, Observe: options.Observe, Stream: options.Stream,
		})
		result.Turns++
		turnContext := usageContext(turn.Usage)
		result.LastContextTokens = turnContext

		if turn.Assembly.State == conversation.TurnFailed {
			if owesBoundary && (assemblyHasText(turn.Assembly) || turn.HasUsage()) {
				options.Session.AddBoundary()
			}
			outcome := AbsorbAbort(options.Session, &turn, AbortProviderError)
			result.FinalItemsFrom = outcome.ItemsFrom
			result.FinalItemsTo = outcome.ItemsTo
			result.AbortMarkerPlaced = outcome.MarkerPlaced
			result.ErrorMessage = turn.ErrorMessage
			appendAttemptUsage(options.Session, options.Provider, turn.Attempts, true)
			flushLoop(options)
			result.Outcome = LoopProviderError
			return result
		}

		pausePending := false
		signal := loopCheckpoint(options)
		if errors.Is(turn.Err, transport.ErrCancelled) && signal == SignalNone {
			signal = SignalAbort
		}
		if signal == SignalAbort {
			if owesBoundary && (assemblySurvivesCancel(turn.Assembly) || turn.HasUsage()) {
				options.Session.AddBoundary()
			}
			outcome := AbsorbAbort(options.Session, &turn, AbortUserCancel)
			result.FinalItemsFrom = outcome.ItemsFrom
			result.FinalItemsTo = outcome.ItemsTo
			result.AbortMarkerPlaced = outcome.MarkerPlaced
			appendAttemptUsage(options.Session, options.Provider, turn.Attempts, true)
			flushLoop(options)
			result.Outcome = LoopInterrupted
			return result
		}
		if signal == SignalPause {
			pausePending = true
		}

		pausedEmpty := pausePending && turn.Assembly.State == conversation.TurnStreaming && len(turn.Assembly.Items) == 0
		if owesBoundary && (!pausedEmpty || turn.HasUsage()) {
			options.Session.AddBoundary()
		}
		absorbed := options.Session.Absorb(turn.Assembly)
		streamedTo := len(options.Session.Items)
		result.FinalItemsFrom = absorbed.ItemsFrom
		result.FinalItemsTo = streamedTo

		if !absorbed.HadToolCall {
			appendAttemptUsage(options.Session, options.Provider, turn.Attempts, pausedEmpty)
			flushLoop(options)
			if pausedEmpty || pausePending {
				result.Outcome = LoopPaused
			} else {
				result.Outcome = LoopComplete
			}
			return result
		}

		abortBatch := false
		for index := absorbed.ItemsFrom; index < streamedTo; index++ {
			call := options.Session.Items[index]
			if call.Kind != conversation.KindToolCall {
				continue
			}
			signal = loopCheckpoint(options)
			if signal == SignalPause {
				pausePending = true
			}
			if signal == SignalAbort {
				abortBatch = true
			}
			action := ToolRun
			if len(options.Session.Tools) == 0 {
				action = ToolRefuse
			} else if abortBatch {
				action = ToolSkip
			}
			if options.ToolSeen != nil {
				options.ToolSeen(call)
				if loopCheckpoint(options) == SignalAbort {
					abortBatch = true
					action = ToolSkip
				}
			}
			options.Session.Append(runTool(options, call, action))
		}

		signal = loopCheckpoint(options)
		if signal == SignalAbort || abortBatch {
			options.Session.MarkInterrupt()
		}
		appendAttemptUsage(options.Session, options.Provider, turn.Attempts, false)
		flushLoop(options)

		if signal == SignalAbort || abortBatch {
			result.Outcome = LoopInterrupted
			result.AbortMarkerPlaced = true
			return result
		}
		if signal == SignalPause {
			pausePending = true
		}
		if pausePending {
			result.Outcome = LoopPaused
			return result
		}
		if options.Compact != nil && options.ShouldCompact != nil && options.Provider != nil {
			contextLimit := options.Provider.ContextWindowForModel(loopModel(options))
			if options.ShouldCompact(turnContext, contextLimit) {
				options.Compact()
				signal = loopCheckpoint(options)
				if signal == SignalAbort {
					options.Session.MarkInterrupt()
					flushLoop(options)
					result.Outcome = LoopInterrupted
					result.AbortMarkerPlaced = true
					return result
				}
				if signal == SignalPause {
					result.Outcome = LoopPaused
					return result
				}
			}
		}
	}
	return result
}

func loopCheckpoint(options LoopOptions) Signal {
	if options.Checkpoint == nil {
		return SignalNone
	}
	return options.Checkpoint()
}

func loopModel(options LoopOptions) string {
	if options.Model != "" {
		return options.Model
	}
	if options.Session.Model != nil {
		return *options.Session.Model
	}
	return ""
}

func usageContext(usage *conversation.Usage) int64 {
	if usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil || *usage.InputTokens < 0 || *usage.OutputTokens < 0 {
		return -1
	}
	return *usage.InputTokens + *usage.OutputTokens
}

func appendAttemptUsage(state *Session, provider *runtime.Provider, attempts []Attempt, aborted bool) {
	for _, attempt := range attempts {
		if aborted && !usageReported(attempt.Usage) {
			continue
		}
		state.AddTurnUsage(provider, attempt.Usage, attempt.ElapsedMS, attempt.Response, conversation.Origin(""))
	}
}

func runTool(options LoopOptions, call conversation.Item, action ToolAction) conversation.Item {
	imageInput := -1
	if options.Provider != nil {
		imageInput = options.Provider.ImageInputForModel(loopModel(options))
	}
	if options.ToolCall != nil {
		return options.ToolCall(call, action, imageInput)
	}
	if options.ToolRegistry != nil && action == ToolRun {
		return options.ToolRegistry.Execute(call, imageInput)
	}
	switch action {
	case ToolRefuse:
		return ToolResult(call, RefusedResult, conversation.OriginRefused)
	case ToolSkip:
		return ToolResult(call, SkippedResult, conversation.OriginSkipped)
	default:
		return ToolResult(call, "error: no tool executor is configured", conversation.Origin(""))
	}
}

func ToolResult(call conversation.Item, output string, origin conversation.Origin) conversation.Item {
	return conversation.Item{
		Kind: conversation.KindToolResult, CallID: cloneString(call.CallID),
		ToolName: cloneString(call.ToolName), Output: conversation.String(output), Origin: origin,
	}
}

func flushLoop(options LoopOptions) {
	if options.Flush != nil {
		options.Flush()
	}
}

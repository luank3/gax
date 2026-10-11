package agent

import (
	"context"
	"errors"
	"time"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/client"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/transport"
)

const maxAttemptDetails = 8

type StreamFunc func(context.Context, *transport.Client, *runtime.Provider, string,
	coreprovider.Context, client.StreamOptions, func() bool, func(conversation.Event),
) (transport.Response, error)

type TurnOptions struct {
	Session       *Session
	Provider      *runtime.Provider
	HTTPClient    *transport.Client
	Model         string
	SessionID     *string
	StreamOptions client.StreamOptions
	Tick          func() bool
	Observe       func(conversation.Event)
	Stream        StreamFunc
}

type Attempt struct {
	Usage     *conversation.Usage
	Response  *conversation.StreamResponse
	ElapsedMS int64
}

type LoopTurn struct {
	Assembly     *conversation.Turn
	Usage        *conversation.Usage
	Attempts     []Attempt
	ErrorMessage string
	Response     transport.Response
	Err          error
	attemptStart time.Time
}

// RunTurn streams one provider round-trip, forwarding events to an optional observer while
// assembling provider-independent output and retaining the usage reported by each attempt.
func RunTurn(ctx context.Context, options TurnOptions) LoopTurn {
	if ctx == nil {
		ctx = context.Background()
	}
	turn := LoopTurn{Assembly: &conversation.Turn{}, attemptStart: time.Now()}
	turn.Assembly.Reset()
	model := options.Model
	requestContext := coreprovider.Context{ImageInput: -1}
	if options.Session != nil {
		if model == "" && options.Session.Model != nil {
			model = *options.Session.Model
		}
		requestContext = options.Session.Context()
	}
	if options.Provider != nil {
		requestContext.ImageInput = options.Provider.ImageInputForModel(model)
	}
	requestContext.SessionID = cloneString(options.SessionID)
	stream := options.Stream
	if stream == nil {
		stream = client.Stream
	}
	turn.Response, turn.Err = stream(ctx, options.HTTPClient, options.Provider, model,
		requestContext, options.StreamOptions, options.Tick, func(event conversation.Event) {
			if options.Observe != nil {
				options.Observe(event)
			}
			turn.recordEvent(event)
			turn.Assembly.Consume(event)
		})
	if turn.Assembly.State == conversation.TurnStreaming && !errors.Is(turn.Err, transport.ErrCancelled) {
		turn.Assembly.State = conversation.TurnFailed
		if turn.ErrorMessage == "" {
			if turn.Err != nil {
				turn.ErrorMessage = turn.Err.Error()
			} else {
				turn.ErrorMessage = "provider stream ended before completion"
			}
		}
	}
	return turn
}

func (turn *LoopTurn) recordEvent(event conversation.Event) {
	switch event.Kind {
	case conversation.EventRetry:
		if usageReported(event.Usage) {
			turn.recordAttempt(Attempt{
				Usage: cloneUsage(event.Usage), ElapsedMS: turn.elapsedMS(),
			})
		}
		turn.attemptStart = time.Now().Add(time.Duration(event.DelayMS) * time.Millisecond)
	case conversation.EventDone:
		turn.Usage = cloneUsage(event.Usage)
		turn.recordAttempt(Attempt{
			Usage: cloneUsage(event.Usage), Response: cloneResponse(event.Response), ElapsedMS: turn.elapsedMS(),
		})
	case conversation.EventError:
		if turn.ErrorMessage == "" && event.ErrorMessage != "" {
			turn.ErrorMessage = event.ErrorMessage
		}
		if event.Usage != nil {
			turn.Usage = cloneUsage(event.Usage)
			turn.recordAttempt(Attempt{
				Usage: cloneUsage(event.Usage), Response: cloneResponse(event.Response), ElapsedMS: turn.elapsedMS(),
			})
		}
	}
}

func (turn *LoopTurn) recordAttempt(attempt Attempt) {
	if len(turn.Attempts) < maxAttemptDetails {
		turn.Attempts = append(turn.Attempts, attempt)
		return
	}

	last := &turn.Attempts[maxAttemptDetails-1]
	last.Usage = mergeAttemptUsage(last.Usage, attempt.Usage)
	last.ElapsedMS += attempt.ElapsedMS
}

func mergeAttemptUsage(sum, extra *conversation.Usage) *conversation.Usage {
	if sum == nil && extra == nil {
		return nil
	}
	merged := cloneUsage(sum)
	if merged == nil {
		merged = &conversation.Usage{}
	}
	var additional conversation.Usage
	if extra != nil {
		additional = *extra
	}

	// Match agent_usage_add: a token report without an exact cost makes the total
	// unpriceable, even if another folded attempt did report a cost.
	unpriced := hasUnpricedTokens(sum) || hasUnpricedTokens(extra)
	merged.InputTokens = addReportedTokens(merged.InputTokens, additional.InputTokens)
	merged.OutputTokens = addReportedTokens(merged.OutputTokens, additional.OutputTokens)
	merged.CachedTokens = addReportedTokens(merged.CachedTokens, additional.CachedTokens)
	merged.CacheWriteTokens = addReportedTokens(merged.CacheWriteTokens, additional.CacheWriteTokens)
	merged.CacheWrite1HTokens = addReportedTokens(merged.CacheWrite1HTokens, additional.CacheWrite1HTokens)

	if unpriced {
		merged.Cost = conversation.Float64(-1)
	} else if extraCost := additional.Cost; extraCost != nil && *extraCost >= 0 {
		cost := float64(0)
		if sum != nil && sum.Cost != nil && *sum.Cost >= 0 {
			cost = *sum.Cost
		}
		merged.Cost = conversation.Float64(cost + *extraCost)
	}
	return merged
}

func hasUnpricedTokens(usage *conversation.Usage) bool {
	if usage == nil || usage.Cost != nil && *usage.Cost >= 0 {
		return false
	}
	return tokenReported(usage.InputTokens) || tokenReported(usage.OutputTokens)
}

func addReportedTokens(sum, extra *int64) *int64 {
	if extra == nil || *extra < 0 {
		return clonePointer(sum)
	}
	total := *extra
	if sum != nil && *sum >= 0 {
		total += *sum
	}
	return conversation.Int64(total)
}

func (turn *LoopTurn) elapsedMS() int64 {
	elapsed := time.Since(turn.attemptStart).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (turn *LoopTurn) HasUsage() bool {
	if turn == nil {
		return false
	}
	for _, attempt := range turn.Attempts {
		if usageReported(attempt.Usage) {
			return true
		}
	}
	return false
}

type AbortReason uint8

const (
	AbortProviderError AbortReason = iota
	AbortUserCancel
)

type AbortOutcome struct {
	MarkerPlaced bool
	ItemsFrom    int
	ItemsTo      int
}

// AbsorbAbort repairs a failed or cancelled partial turn so the next provider request sees
// replayable history. Provider failures keep assistant text only; user cancellation also keeps
// completed reasoning and tool calls, pairing each call with a skipped result.
func AbsorbAbort(state *Session, turn *LoopTurn, reason AbortReason) AbortOutcome {
	if state == nil || turn == nil {
		return AbortOutcome{}
	}
	if turn.Assembly == nil {
		end := len(state.Items)
		return AbortOutcome{ItemsFrom: end, ItemsTo: end}
	}
	assembly := turn.Assembly
	if reason == AbortProviderError {
		assembly.KeepText()
	} else {
		assembly.DiscardReasoning()
		if !assemblySurvivesCancel(assembly) {
			assembly.Reset()
			end := len(state.Items)
			return AbortOutcome{ItemsFrom: end, ItemsTo: end}
		}
	}
	keptText := assemblyHasText(assembly)
	hadPartialText := assembly.HasText
	if hadPartialText {
		assembly.FlushText("\n" + InterruptMarker)
	}

	absorbed := state.Absorb(assembly)
	itemsFrom := absorbed.ItemsFrom
	itemsTo := len(state.Items)
	markerPlaced := hadPartialText
	if hadPartialText {
		for index := itemsTo - 1; index >= itemsFrom; index-- {
			if state.Items[index].Kind == conversation.KindAssistant {
				state.Items[index].Origin = conversation.OriginInterrupted
				break
			}
		}
	}
	for index := itemsFrom; index < itemsTo; index++ {
		call := state.Items[index]
		if call.Kind != conversation.KindToolCall {
			continue
		}
		state.Append(conversation.Item{
			Kind: conversation.KindToolResult, CallID: cloneString(call.CallID),
			ToolName: cloneString(call.ToolName), Output: conversation.String(InterruptMarker),
			Origin: conversation.OriginSkipped,
		})
		markerPlaced = true
	}

	if !markerPlaced && (reason == AbortUserCancel || keptText) {
		lastAssistant := -1
		for index := itemsTo - 1; index >= itemsFrom; index-- {
			if state.Items[index].Kind == conversation.KindAssistant {
				lastAssistant = index
				break
			}
		}
		if lastAssistant >= 0 && state.Items[lastAssistant].Text != nil {
			marked := *state.Items[lastAssistant].Text + "\n" + InterruptMarker
			state.Items[lastAssistant].Text = &marked
			state.Items[lastAssistant].Origin = conversation.OriginInterrupted
		} else {
			state.Append(conversation.Item{
				Kind: conversation.KindAssistant, Text: conversation.String(InterruptMarker),
				Origin: conversation.OriginInterrupted,
			})
		}
		markerPlaced = true
	}
	return AbortOutcome{MarkerPlaced: markerPlaced, ItemsFrom: itemsFrom, ItemsTo: itemsTo}
}

func assemblyHasText(assembly *conversation.Turn) bool {
	for _, item := range assembly.Items {
		if item.Kind == conversation.KindAssistant {
			return true
		}
	}
	return assembly.HasText
}

func assemblySurvivesCancel(assembly *conversation.Turn) bool {
	for _, item := range assembly.Items {
		if item.Kind == conversation.KindAssistant || item.Kind == conversation.KindToolCall {
			return true
		}
	}
	return assembly.HasText
}

func usageReported(usage *conversation.Usage) bool {
	if usage == nil {
		return false
	}
	return usage.InputTokens != nil && *usage.InputTokens >= 0 ||
		usage.OutputTokens != nil && *usage.OutputTokens >= 0 ||
		usage.Cost != nil && *usage.Cost >= 0
}

func cloneUsage(usage *conversation.Usage) *conversation.Usage {
	if usage == nil {
		return nil
	}
	copy := *usage
	copy.InputTokens = clonePointer(usage.InputTokens)
	copy.OutputTokens = clonePointer(usage.OutputTokens)
	copy.CachedTokens = clonePointer(usage.CachedTokens)
	copy.CacheWriteTokens = clonePointer(usage.CacheWriteTokens)
	copy.CacheWrite1HTokens = clonePointer(usage.CacheWrite1HTokens)
	copy.Cost = clonePointer(usage.Cost)
	copy.ElapsedMS = clonePointer(usage.ElapsedMS)
	copy.UncachedInput = clonePointer(usage.UncachedInput)
	copy.CostInput = clonePointer(usage.CostInput)
	copy.CostCacheRead = clonePointer(usage.CostCacheRead)
	copy.CostCacheWrite = clonePointer(usage.CostCacheWrite)
	copy.CostOutput = clonePointer(usage.CostOutput)
	copy.CostTotal = clonePointer(usage.CostTotal)
	copy.ProviderLabel = clonePointer(usage.ProviderLabel)
	copy.ModelLabel = clonePointer(usage.ModelLabel)
	copy.Effort = clonePointer(usage.Effort)
	copy.ServedModel = clonePointer(usage.ServedModel)
	copy.Route = clonePointer(usage.Route)
	copy.ResponseID = clonePointer(usage.ResponseID)
	return &copy
}

func cloneResponse(response *conversation.StreamResponse) *conversation.StreamResponse {
	if response == nil {
		return nil
	}
	return &conversation.StreamResponse{ID: cloneString(response.ID), Model: cloneString(response.Model), Route: cloneString(response.Route)}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Ensure the default stream API remains assignable to StreamFunc as its request contract evolves.
var _ StreamFunc = client.Stream

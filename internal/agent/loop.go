package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/tools"
)

// Signal requests pause or immediate abort from a frontend.
type Signal uint8

const (
	SignalNone Signal = iota
	SignalPause
	SignalAbort
)

// LoopOptions configures one RunLoop execution.
type LoopOptions struct {
	Session          *Session
	Provider         provider.Client
	Tools            *tools.Registry
	SystemPrompt     string
	Model            string
	Effort           string
	SessionID        string
	Catalog          *catalog.Store
	CatalogID        string
	MaxTurns         int
	ContextLimit     int64
	CompactAuto      bool
	CompactThreshold int64
	StreamOptions    provider.StreamOptions
	Sink             core.EventSink
	CheckSignal      func() Signal
}

// LoopResult summarizes how RunLoop terminated.
type LoopResult struct {
	Outcome           core.Outcome
	Turns             int
	LastContextTokens int64
	ContextLimit      int64
	FinalText         string
	ErrorMessage      string
}

const maxCompactFailures = 2

// RunLoop executes model turns and tool calls until completion, pause, abort, or error.
func RunLoop(ctx context.Context, opts LoopOptions) LoopResult {
	res := LoopResult{Outcome: core.OutcomeMaxTurns, LastContextTokens: -1}
	if opts.Session == nil || opts.Provider == nil {
		res.Outcome = core.OutcomeError
		res.ErrorMessage = "agent session or provider is unavailable"
		return res
	}
	if opts.Tools != nil {
		opts.Tools.SetActiveItems(opts.Session.ContextWindow)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	compactFailures := 0
	autoCompact := opts.CompactAuto
	if opts.ContextLimit <= 0 && opts.Catalog != nil && opts.Model != "" {
		if info, ok := opts.Catalog.Lookup(opts.Provider.ID(), opts.CatalogID, opts.Model); ok && info.ContextLimit > 0 {
			opts.ContextLimit = info.ContextLimit
		}
	}
	res.ContextLimit = opts.ContextLimit

	for turn := 0; opts.MaxTurns <= 0 || turn < opts.MaxTurns; turn++ {
		if opts.Tools != nil {
			if note := opts.Tools.CollectTaskNotes(); note != "" {
				opts.Session.Append(core.Item{
					Kind:   core.KindUser,
					Origin: core.OriginTaskNote,
					Text:   note,
				})
				emitEvent(opts.Sink, core.TaskNote{Text: note})
			}
		}

		if turn > 0 {
			opts.Session.Append(core.Item{Kind: core.KindTurnBoundary})
		}

		if autoCompact && res.LastContextTokens > 0 && ShouldAutoCompact(res.LastContextTokens, opts.ContextLimit, opts.CompactThreshold) {
			if err := Compact(ctx, opts.Session, opts.Provider, opts.Model, "", true, opts.Sink); err != nil {
				compactFailures++
				if compactFailures >= maxCompactFailures {
					autoCompact = false
				}
			} else {
				compactFailures = 0
			}
			if opts.CheckSignal != nil && opts.CheckSignal() == SignalPause {
				res.Outcome = core.OutcomePaused
				emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomePaused})
				return res
			}
		}

		emitEvent(opts.Sink, core.TurnStarted{
			Turn:     turn + 1,
			Provider: opts.Provider.ID(),
			Model:    opts.Model,
			Effort:   opts.Effort,
		})

		reqOpts := opts.StreamOptions
		reqOpts.Model = opts.Model
		reqOpts.Effort = opts.Effort
		reqOpts.SessionID = opts.SessionID

		var toolDefs []core.ToolDef
		if opts.Tools != nil {
			toolDefs = opts.Tools.Definitions()
		}

		var textBuf strings.Builder
		var reasonBuf strings.Builder
		var reasonField string
		var toolCalls []core.Item
		var turnUsage *core.Usage
		var incompleteLength bool

		t0 := time.Now()
		err := opts.Provider.Stream(ctx, provider.StreamRequest{
			SystemPrompt: opts.SystemPrompt,
			Items:        opts.Session.ContextWindow(),
			Tools:        toolDefs,
			Options:      reqOpts,
		}, func(ev provider.StreamEvent) {
			switch ev.Kind {
			case provider.EventText:
				textBuf.WriteString(ev.Text)
				emitEvent(opts.Sink, core.TextDelta{Text: ev.Text})
			case provider.EventReasoning:
				reasonBuf.WriteString(ev.Text)
				if ev.ReasoningField != "" {
					reasonField = ev.ReasoningField
				}
				emitEvent(opts.Sink, core.ReasoningDelta{Text: ev.Text})
			case provider.EventToolCallStart:
				emitEvent(opts.Sink, core.ToolCallSeen{CallID: ev.CallID, Name: ev.ToolName})
			case provider.EventToolCallDone:
				toolCalls = append(toolCalls, core.Item{
					Kind:      core.KindToolCall,
					CallID:    ev.CallID,
					ToolName:  ev.ToolName,
					Arguments: ev.Arguments,
				})
			case provider.EventUsage:
				turnUsage = ev.Usage
			case provider.EventRetry:
				emitEvent(opts.Sink, core.RetryScheduled{
					Attempt: ev.RetryAttempt,
					Delay:   ev.RetryDelay,
					Reason:  ev.RetryReason,
				})
			case provider.EventDone:
				incompleteLength = ev.IncompleteLength
			}
		})
		res.Turns++

		if err != nil {
			if errors.Is(err, context.Canceled) || (opts.CheckSignal != nil && opts.CheckSignal() == SignalAbort) {
				// Discard unfinished reasoning; keep any already-streamed assistant text.
				if textBuf.Len() > 0 {
					opts.Session.Append(core.Item{Kind: core.KindAssistant, Text: textBuf.String()})
				}
				res.Outcome = core.OutcomeInterrupted
				emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomeInterrupted})
				return res
			}
			var ov *provider.OverflowError
			if errors.As(err, &ov) && autoCompact && compactFailures < maxCompactFailures {
				if cerr := Compact(ctx, opts.Session, opts.Provider, opts.Model, "", true, opts.Sink); cerr == nil {
					compactFailures = 0
					continue
				}
				compactFailures++
				if compactFailures >= maxCompactFailures {
					autoCompact = false
				}
			}
			res.Outcome = core.OutcomeError
			res.ErrorMessage = err.Error()
			emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomeError, Error: err.Error()})
			return res
		}

		// Recover when a reply fills a known context window (hax [Unreleased] aca988f).
		if incompleteLength && autoCompact && compactFailures < maxCompactFailures {
			if cerr := Compact(ctx, opts.Session, opts.Provider, opts.Model, "", true, opts.Sink); cerr == nil {
				compactFailures = 0
				continue
			}
			compactFailures++
		}

		if reasonBuf.Len() > 0 {
			opts.Session.Append(core.Item{
				Kind:           core.KindReasoning,
				Provider:       opts.Provider.ID(),
				Model:          opts.Model,
				ReasoningText:  reasonBuf.String(),
				ReasoningField: reasonField,
			})
		}
		if textBuf.Len() > 0 {
			res.FinalText = textBuf.String()
			opts.Session.Append(core.Item{Kind: core.KindAssistant, Text: res.FinalText})
		}
		if turnUsage != nil {
			turnUsage.ElapsedMS = time.Since(t0).Milliseconds()
			if turnUsage.CostTotal == 0 && opts.Catalog != nil && opts.Model != "" {
				if info, ok := opts.Catalog.Lookup(opts.Provider.ID(), opts.CatalogID, opts.Model); ok {
					turnUsage.CostInput = float64(turnUsage.InTokens) * info.CostInput / 1e6
					turnUsage.CostOutput = float64(turnUsage.Output) * info.CostOutput / 1e6
					turnUsage.CostCached = float64(turnUsage.Cached) * info.CostCacheRead / 1e6
					turnUsage.CostTotal = turnUsage.CostInput + turnUsage.CostOutput + turnUsage.CostCached
					if turnUsage.CostTotal > 0 {
						turnUsage.CostEstimated = true
					}
				}
			}
			res.LastContextTokens = turnUsage.Input + turnUsage.Output
			opts.Session.Append(core.Item{
				Kind:     core.KindTurnUsage,
				Provider: opts.Provider.ID(),
				Model:    opts.Model,
				Usage:    turnUsage,
			})
			emitEvent(opts.Sink, core.TurnUsage{Usage: *turnUsage})
		}

		if len(toolCalls) == 0 {
			res.Outcome = core.OutcomeComplete
			emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomeComplete})
			return res
		}

		opts.Session.Append(toolCalls...)
		for _, tc := range toolCalls {
			if opts.CheckSignal != nil && opts.CheckSignal() == SignalAbort {
				opts.Session.Append(core.Item{
					Kind:     core.KindToolResult,
					CallID:   tc.CallID,
					ToolName: tc.ToolName,
					Origin:   core.OriginInterrupted,
					Output:   "interrupted",
					IsError:  true,
				})
				continue
			}
			argsSummary := formatToolArgsSummary(tc.ToolName, tc.Arguments)
			emitEvent(opts.Sink, core.ToolCallStarted{CallID: tc.CallID, Name: tc.ToolName, ArgsSummary: argsSummary})
			callT0 := time.Now()
			var toolRes tools.Result
			if opts.Tools != nil {
				toolRes = opts.Tools.Execute(ctx, tc.ToolName, tc.Arguments, func(stream, chunk string) {
					emitEvent(opts.Sink, core.ToolOutputDelta{CallID: tc.CallID, Stream: stream, Chunk: chunk})
				})
			} else {
				toolRes = tools.Result{Text: "error: tools are disabled", IsError: true}
			}
			opts.Session.Append(core.Item{
				Kind:     core.KindToolResult,
				CallID:   tc.CallID,
				ToolName: tc.ToolName,
				Output:   toolRes.Text,
				Images:   toolRes.Images,
				IsError:  toolRes.IsError,
			})
			emitEvent(opts.Sink, core.ToolCallFinished{
				CallID:      tc.CallID,
				Name:        tc.ToolName,
				ArgsSummary: argsSummary,
				Summary:     summarizeToolResult(tc.ToolName, toolRes.Text),
				DurationMS:  time.Since(callT0).Milliseconds(),
				IsError:     toolRes.IsError,
			})
		}

		if opts.CheckSignal != nil {
			switch opts.CheckSignal() {
			case SignalPause:
				res.Outcome = core.OutcomePaused
				emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomePaused})
				return res
			case SignalAbort:
				res.Outcome = core.OutcomeInterrupted
				emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomeInterrupted})
				return res
			}
		}
	}

	emitEvent(opts.Sink, core.TurnFinished{Outcome: core.OutcomeMaxTurns})
	return res
}

func emitEvent(sink core.EventSink, ev core.Event) {
	if sink != nil {
		sink.Emit(ev)
	}
}

func formatToolArgsSummary(name, rawArgs string) string {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" || rawArgs == "{}" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(rawArgs), &m) != nil {
		return collapsePreview(rawArgs, 80)
	}
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "bash":
		if c := str("command"); c != "" {
			return "$ " + collapsePreview(c, 80)
		}
	case "read":
		p := str("path")
		if p != "" {
			if off, ok := m["offset"].(float64); ok && off > 0 {
				if lim, ok := m["limit"].(float64); ok && lim > 0 {
					return fmt.Sprintf("%s:%d+%d", p, int(off), int(lim))
				}
				return fmt.Sprintf("%s:%d", p, int(off))
			}
			return p
		}
	case "write", "edit":
		if p := str("path"); p != "" {
			return p
		}
	case "web_fetch":
		if u := str("url"); u != "" {
			return collapsePreview(u, 80)
		}
	case "web_search":
		if q := str("query"); q != "" {
			return collapsePreview(q, 80)
		}
	case "skill":
		if s := str("name"); s != "" {
			return s
		}
	case "task_wait", "task_stop", "task_output":
		if id := str("id"); id != "" {
			return id
		}
	}
	for _, k := range []string{"path", "command", "query", "url", "name", "id"} {
		if v := str(k); v != "" {
			return collapsePreview(v, 80)
		}
	}
	return collapsePreview(rawArgs, 80)
}

func collapsePreview(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) > maxRunes && maxRunes > 3 {
		return string(rs[:maxRunes-3]) + "..."
	}
	return s
}

func summarizeToolResult(name, text string) string {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if name == "edit" || name == "write" {
		if len(lines) > 20 {
			lines = append(lines[:20], fmt.Sprintf("... (%d more diff lines)", len(lines)-20))
		}
		out := strings.Join(lines, "\n")
		if len(out) > 1536 {
			return out[:1533] + "..."
		}
		return out
	}
	var nonEmpty []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if l != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	if len(nonEmpty) > 6 {
		omitted := len(nonEmpty) - 6
		preview := make([]string, 0, 7)
		preview = append(preview, nonEmpty[:3]...)
		preview = append(preview, fmt.Sprintf("... (%d more lines)", omitted))
		preview = append(preview, nonEmpty[len(nonEmpty)-3:]...)
		nonEmpty = preview
	}
	out := strings.Join(nonEmpty, "\n")
	if len(out) > 1024 {
		return out[:1021] + "..."
	}
	return out
}

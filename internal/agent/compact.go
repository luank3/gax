package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
)

// ShouldAutoCompact reports whether current context usage warrants compaction.
func ShouldAutoCompact(lastTokens, limit, thresholdPct int64) bool {
	if limit <= 0 || lastTokens <= 0 {
		return false
	}
	if thresholdPct <= 0 {
		thresholdPct = 85
	}
	headroom := min(int64(16000), limit/4)
	if lastTokens >= limit-headroom {
		return true
	}
	return lastTokens*100 >= limit*thresholdPct
}

// Compact summarizes the active context window into a compact_seed item.
// If the full window overflows the summarization request, it retries with a
// shortened copy dropping older items first (hax v0.7.0).
func Compact(ctx context.Context, sess *Session, prov provider.Client, model, focus string, auto bool, sink core.EventSink) error {
	if sess == nil || prov == nil {
		return fmt.Errorf("compaction requires session and provider")
	}
	window := sess.ContextWindow()
	if len(window) == 0 {
		return nil
	}
	beforeTokens := EstimateTokens("", window)
	if sink != nil {
		sink.Emit(core.CompactStarted{Auto: auto})
	}

	instruction := "Summarize the conversation so far concisely, preserving key decisions, file paths, symbol names, errors, and remaining work."
	if strings.TrimSpace(focus) != "" {
		instruction += " Focus especially on: " + strings.TrimSpace(focus)
	}

	var summary string
	var err error
	candidates := [][]core.Item{window}
	if len(window) > 4 {
		candidates = append(candidates, window[len(window)/2:])
	}
	if len(window) > 8 {
		candidates = append(candidates, window[len(window)*3/4:])
	}

	for _, slice := range candidates {
		reqItems := append(append([]core.Item(nil), slice...), core.Item{
			Kind: core.KindUser,
			Text: instruction,
		})
		var b strings.Builder
		var usage *core.Usage
		err = prov.Stream(ctx, provider.StreamRequest{
			Items:   reqItems,
			Options: provider.StreamOptions{Model: model},
		}, func(ev provider.StreamEvent) {
			if ev.Kind == provider.EventText {
				b.WriteString(ev.Text)
			}
			if ev.Kind == provider.EventUsage && ev.Usage != nil {
				usage = ev.Usage
			}
		})
		if err == nil {
			summary = strings.TrimSpace(b.String())
			if usage != nil {
				sess.Append(core.Item{
					Kind:     core.KindTurnUsage,
					Origin:   core.OriginCompaction,
					Provider: prov.ID(),
					Model:    model,
					Usage:    usage,
				})
			}
			break
		}
		var ov *provider.OverflowError
		if !errors.As(err, &ov) {
			break
		}
	}

	if err != nil {
		if sink != nil {
			sink.Emit(core.CompactFinished{BeforeTokens: beforeTokens, Err: err})
		}
		return err
	}
	if summary == "" {
		summary = "(empty compaction summary)"
	}
	seed := core.Item{
		Kind:   core.KindUser,
		Origin: core.OriginCompactSeed,
		Text:   "[Compacted conversation summary]\n" + summary,
	}
	sess.Append(seed)
	afterTokens := EstimateTokens("", []core.Item{seed})
	if sink != nil {
		sink.Emit(core.CompactFinished{BeforeTokens: beforeTokens, AfterTokens: afterTokens})
	}
	return nil
}

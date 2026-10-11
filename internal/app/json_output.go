package app

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
)

type jsonResultRecord struct {
	Type          string   `json:"type"`
	Outcome       string   `json:"outcome"`
	Text          string   `json:"text,omitempty"`
	Error         string   `json:"error,omitempty"`
	Turns         int      `json:"turns"`
	ElapsedMS     int64    `json:"elapsed_ms"`
	ContextTokens *int64   `json:"context_tokens,omitempty"`
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated,omitempty"`
	SessionID     *string  `json:"session_id,omitempty"`
}

func finishRun(result agent.LoopResult, answer string, jsonOutput *jsonRun, stdout, stderr io.Writer,
	store *config.Store, usage agent.UsageTotals) int {
	if result.Outcome != agent.LoopComplete {
		if result.Outcome == agent.LoopPaused {
			fmt.Fprintln(stderr, "gax: paused at a turn boundary")
		}
		if jsonOutput != nil {
			if err := jsonOutput.finish(result, "", usage); err != nil {
				fmt.Fprintf(stderr, "gax: write JSON result: %v\n", err)
				return 1
			}
		} else if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "gax: %s\n", result.ErrorMessage)
		}
		if result.Outcome == agent.LoopInterrupted {
			return 130
		}
		return 1
	}
	if jsonOutput != nil {
		if err := jsonOutput.finish(result, answer, usage); err != nil {
			fmt.Fprintf(stderr, "gax: write JSON output: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeAssistantAnswer(stdout, answer, store); err != nil {
		fmt.Fprintf(stderr, "gax: write output: %v\n", err)
		return 1
	}
	return 0
}

func collectAssistantText(items []conversation.Item, from, to int) string {
	if from < 0 {
		from = 0
	}
	if to > len(items) {
		to = len(items)
	}
	var output strings.Builder
	for index := from; index < to; index++ {
		item := items[index]
		if item.Kind != conversation.KindAssistant || item.Text == nil || *item.Text == "" {
			continue
		}
		if output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
			output.WriteByte('\n')
		}
		output.WriteString(*item.Text)
	}
	return output.String()
}

type jsonRun struct {
	writer    io.Writer
	cursor    int
	started   time.Time
	sessionID *string
	err       error
}

func loopOutcomeName(outcome agent.LoopOutcome) string {
	switch outcome {
	case agent.LoopComplete:
		return "complete"
	case agent.LoopProviderError:
		return "error"
	case agent.LoopInterrupted:
		return "interrupted"
	case agent.LoopPaused:
		return "paused"
	case agent.LoopMaxTurns:
		return "max_turns"
	default:
		return "error"
	}
}

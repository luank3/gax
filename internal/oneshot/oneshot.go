// Package oneshot implements the -p / --print non-interactive frontend.
package oneshot

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/tools"
)

// Options configures a single one-shot run.
type Options struct {
	Session          *agent.Session
	SessionID        string
	Provider         provider.Client
	Tools            *tools.Registry
	SystemPrompt     string
	Model            string
	Effort           string
	MaxTurns         int
	ContextLimit     int64
	CompactAuto      bool
	CompactThreshold int64
	StreamOptions    provider.StreamOptions
	Stdout           io.Writer
	Stderr           io.Writer
}

// BuildPrompt combines positional args and piped stdin (hax 64ddf49).
func BuildPrompt(args []string, stdin io.Reader, stdinTTY bool) (string, error) {
	pos := strings.TrimSpace(strings.Join(args, " "))
	if stdinTTY || stdin == nil {
		return pos, nil
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	piped := strings.TrimSpace(string(raw))
	if pos == "" {
		return piped, nil
	}
	if piped == "" {
		return pos, nil
	}
	return pos + "\n\n" + piped, nil
}

// Run executes the agent loop in one-shot mode, writing only the final answer to stdout.
func Run(ctx context.Context, opts Options) int {
	if opts.Stderr != nil {
		fmt.Fprintf(opts.Stderr, "gax · %s", opts.Provider.ID())
		if opts.Model != "" {
			fmt.Fprintf(opts.Stderr, " · %s", opts.Model)
		}
		if opts.Effort != "" {
			fmt.Fprintf(opts.Stderr, " · %s", opts.Effort)
		}
		fmt.Fprintln(opts.Stderr)
	}

	var sig atomic.Int32
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopSignals := platform.WatchSignals(
		func() { sig.Store(int32(agent.SignalPause)) },
		func() {
			if sig.Load() == int32(agent.SignalAbort) {
				cancel()
				return
			}
			sig.Store(int32(agent.SignalAbort))
			cancel()
		},
	)
	defer stopSignals()

	t0 := time.Now()
	res := agent.RunLoop(runCtx, agent.LoopOptions{
		Session:          opts.Session,
		Provider:         opts.Provider,
		Tools:            opts.Tools,
		SystemPrompt:     opts.SystemPrompt,
		Model:            opts.Model,
		Effort:           opts.Effort,
		SessionID:        opts.SessionID,
		MaxTurns:         opts.MaxTurns,
		ContextLimit:     opts.ContextLimit,
		CompactAuto:      opts.CompactAuto,
		CompactThreshold: opts.CompactThreshold,
		StreamOptions:    opts.StreamOptions,
		CheckSignal: func() agent.Signal {
			return agent.Signal(sig.Load())
		},
	})
	elapsed := time.Since(t0)

	if res.FinalText != "" && opts.Stdout != nil {
		_, _ = io.WriteString(opts.Stdout, res.FinalText)
		if !strings.HasSuffix(res.FinalText, "\n") {
			_, _ = io.WriteString(opts.Stdout, "\n")
		}
	}

	if opts.Stderr != nil {
		totals := opts.Session.Totals()
		fmt.Fprintf(opts.Stderr, "%s · %d turns · $%.4f", elapsed.Round(time.Millisecond), res.Turns, totals.CostTotal)
		if opts.SessionID != "" {
			fmt.Fprintf(opts.Stderr, " · resume: gax --resume=%s -p", opts.SessionID)
		}
		fmt.Fprintln(opts.Stderr)
		if res.ErrorMessage != "" {
			fmt.Fprintf(opts.Stderr, "gax: %s\n", res.ErrorMessage)
		}
	}

	switch res.Outcome {
	case core.OutcomeComplete:
		return 0
	case core.OutcomeInterrupted:
		return 130
	default:
		return 1
	}
}

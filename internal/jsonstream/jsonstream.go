// Package jsonstream implements the --json live JSONL stdout stream frontend.
package jsonstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/tools"
)

// Options configures a --json run.
type Options struct {
	Header           core.SessionHeader
	Session          *agent.Session
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

// Run streams the session header, live appended items, and closing result record as JSONL.
func Run(ctx context.Context, opts Options) int {
	var writeMu sync.Mutex
	var writeFailed atomic.Bool
	writeRecord := func(v any) {
		if opts.Stdout == nil || writeFailed.Load() {
			return
		}
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		b = append(b, '\n')
		writeMu.Lock()
		_, werr := opts.Stdout.Write(b)
		writeMu.Unlock()
		if werr != nil {
			writeFailed.Store(true)
		}
	}

	hdr := opts.Header
	hdr.Timestamp = ""
	writeRecord(struct {
		Type string `json:"type"`
		core.SessionHeader
	}{Type: "session", SessionHeader: hdr})

	var sig atomic.Int32
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopSignals := platform.WatchSignals(
		func() { sig.Store(int32(agent.SignalPause)) },
		func() {
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
		SessionID:        opts.Header.ID,
		MaxTurns:         opts.MaxTurns,
		ContextLimit:     opts.ContextLimit,
		CompactAuto:      opts.CompactAuto,
		CompactThreshold: opts.CompactThreshold,
		StreamOptions:    opts.StreamOptions,
		CheckSignal: func() agent.Signal {
			if writeFailed.Load() {
				return agent.SignalAbort
			}
			return agent.Signal(sig.Load())
		},
	})
	elapsedMS := time.Since(t0).Milliseconds()
	totals := opts.Session.Totals()

	resultRec := struct {
		Type          string       `json:"type"`
		Outcome       core.Outcome `json:"outcome"`
		Text          string       `json:"text,omitempty"`
		Error         string       `json:"error,omitempty"`
		Turns         int          `json:"turns"`
		ElapsedMS     int64        `json:"elapsed_ms"`
		ContextTokens int64        `json:"context_tokens,omitempty"`
		Cost          float64      `json:"cost,omitempty"`
		SessionID     string       `json:"session_id,omitempty"`
	}{
		Type:          "result",
		Outcome:       res.Outcome,
		Text:          res.FinalText,
		Error:         res.ErrorMessage,
		Turns:         res.Turns,
		ElapsedMS:     elapsedMS,
		ContextTokens: max(0, res.LastContextTokens),
		Cost:          totals.CostTotal,
		SessionID:     opts.Header.ID,
	}
	writeRecord(resultRec)

	if writeFailed.Load() {
		if opts.Stderr != nil {
			fmt.Fprintln(opts.Stderr, "gax: --json output stream closed")
		}
		return 1
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

// StreamItem writes a single core.Item as JSONL (used as a Session OnAppend hook).
func StreamItem(w io.Writer, mu *sync.Mutex, failed *atomic.Bool, it core.Item) {
	if w == nil || failed.Load() {
		return
	}
	b, err := json.Marshal(it)
	if err != nil {
		return
	}
	b = append(b, '\n')
	mu.Lock()
	_, werr := w.Write(b)
	mu.Unlock()
	if werr != nil {
		failed.Store(true)
	}
}

package app

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	"github.com/luank3/gax/internal/transport"
)

func runCompactCommand(ctx context.Context, parsed slash.Parsed, provider *runtime.Provider, model *string,
	state *agent.Session, sessionID *string, log *session.Log, store *config.Store, stdin io.Reader, liveTTY bool, stdout, stderr io.Writer) {
	resyncCompactionEffort(provider, state, store, log)
	selectedModel := ""
	if model != nil {
		selectedModel = *model
	}
	interrupts, tick := startCompactionInterruptWatch(stdin, liveTTY)
	if interrupts != nil {
		defer interrupts.Stop()
	}
	result := agent.CompactConversation(agent.CompactOptions{
		Context: ctx, Session: state, Provider: provider, HTTPClient: transport.DefaultClient,
		Model: selectedModel, SessionID: sessionID, Focus: parsed.Argument, Tick: tick,
	})
	if log != nil && result.Attempts > 0 {
		if err := log.Append(state.Items); err != nil {
			writeWarningDiagnostic(stderr, "couldn't write conversation: %v", err)
		}
	}
	switch result.Outcome {
	case agent.CompactComplete:
		state.CompactionDeferred = false
		compactNote(stdout, "conversation compacted")
	case agent.CompactCancelled:
		compactNote(stdout, "compaction cancelled")
	case agent.CompactProviderError:
		message := result.ErrorMessage
		if message == "" {
			message = "stream failed"
		}
		compactNote(stdout, "compaction failed: "+message)
	case agent.CompactNoSummary:
		compactNote(stdout, "compaction produced no summary")
	case agent.CompactNoProvider:
		compactNote(stdout, "no provider selected — use /provider")
	case agent.CompactNoModel:
		compactNote(stdout, "no model selected — use /model (or /provider)")
	case agent.CompactEmpty:
		compactNote(stdout, "nothing to compact")
	}
}

func resyncCompactionEffort(provider *runtime.Provider, state *agent.Session, store *config.Store, log *session.Log) {
	if provider == nil || state == nil || len(state.Items) == 0 || state.Model == nil || *state.Model == "" {
		return
	}
	provider.WaitModelMetadata(runtime.ModelMetadataWait, nil)
	requested := state.Effort
	if store != nil {
		requested = store.String("effort")
	}
	effort := provider.ResolveEffort(*state.Model, requested)
	if state.Effort == nil && effort == nil || state.Effort != nil && effort != nil && *state.Effort == *effort {
		return
	}
	state.Effort = effort
	if log != nil {
		var preset *string
		if store != nil {
			preset = currentPreset(store)
		}
		log.SetSelection(session.Selection{
			Provider: state.ProviderID, Model: state.Model, ModelLabel: state.ModelLabel,
			Effort: state.Effort, Preset: preset,
		})
	}
}

func startCompactionInterruptWatch(stdin io.Reader, liveTTY bool) (*terminalinput.InterruptWatch, func() bool) {
	if !liveTTY {
		return nil, nil
	}
	terminal, ok := stdin.(*os.File)
	if !ok {
		return nil, nil
	}
	watch, err := terminalinput.StartInterruptWatch(terminal, nil, nil)
	if err != nil {
		return nil, nil
	}
	return watch, func() bool { return watch.PauseRequested() || watch.AbortRequested() }
}

func compactNote(writer io.Writer, message string) {
	fmt.Fprintf(writer, "\x1b[2m── %s ──\x1b[0m\n", message)
}

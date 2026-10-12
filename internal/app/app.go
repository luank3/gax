package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/cli"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/diag"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalkeepawake "github.com/luank3/gax/internal/system/keepawake"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalLive "github.com/luank3/gax/internal/terminal/live"
	terminalpaste "github.com/luank3/gax/internal/terminal/paste"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transcript"
	"github.com/luank3/gax/internal/transport"
	"github.com/luank3/gax/internal/version"
	"github.com/luank3/gax/internal/wiretrace"
)

type replOutput struct {
	result      agent.LoopResult
	answer      string
	afterAnswer func(io.Writer) error
}

func Run(args []string, stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) int {
	defer terminalpaste.Cleanup()
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if terminalocale.InitializeProcess() {
		writeWarningDiagnostic(stderr, "no UTF-8 locale found; non-ASCII text will be misread and misaligned")
	}
	store, err := loadStore(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	configureTerminalTheme(store, stderr)
	providers := registry.New(store)
	options, err := cli.ParseWithPresets(args, func(name string) bool {
		for _, candidate := range store.ObjectKeys("presets") {
			if candidate == name {
				return true
			}
		}
		return false
	}, func() []string {
		names, warnings := validPresetNamesWithWarnings(store)
		for _, warning := range warnings {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		return names
	})
	if err != nil {
		var optionError *cli.OptionError
		if errors.As(err, &optionError) {
			_, _ = io.WriteString(stderr, optionError.Diagnostic(os.Args[0]))
			return 1
		}
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	switch options.Action {
	case cli.ActionHelp:
		printHelp(stdout, store)
		return 0
	case cli.ActionVersion:
		fmt.Fprintf(stdout, "gax %s\n", version.Value)
		return 0
	}
	if err := cli.CheckSubagentDepth(os.Getenv("GAX_SUBAGENT_DEPTH")); err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	tracePath := ""
	if configured := store.String("trace"); configured != nil {
		tracePath = *configured
	}
	if err := wiretrace.Default().Configure(tracePath); err != nil {
		writeWarningDiagnostic(stderr, "GAX_TRACE: cannot open '%s' for writing", terminalSafeText(tracePath))
	}
	defer wiretrace.Default().Close()
	if path := store.StringNonempty("transcript"); path != nil {
		if err := transcript.Init(*path); err != nil {
			writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: cannot open '%s' for writing", terminalSafeText(*path))
		}
	}
	var loaded *session.Loaded
	var resumePath string
	if options.ResumeMode == cli.ResumeSelect && options.ResumeID == nil {
		cwd, _ := os.Getwd()
		selected, _, pickErr := pickSession(cwd, "", store, stdin, stdinTTY, stdout)
		if pickErr != nil {
			fmt.Fprintf(stderr, "gax: resume picker: %v\n", pickErr)
			return 1
		}
		if selected == nil {
			return 0
		}
		loaded, resumePath, err = loadResumePath(selected.Path, options, store)
	} else {
		loaded, resumePath, err = loadResume(options, store)
	}
	if err != nil {
		var warning *resumePresetWarning
		if errors.As(err, &warning) && loaded != nil {
			writeWarningDiagnostic(stderr, "%s", warning.Error())
		} else {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return 1
		}
	}
	if options.ResumeMode == cli.ResumeLatest && loaded == nil {
		writeWarningDiagnostic(stderr, "no past conversation in this directory; starting fresh")
	}
	if err := applySelection(store, options, func(message string) {
		writeWarningDiagnostic(stderr, "%s", message)
	}); err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return 1
	}
	configureTerminalTheme(store, stderr)
	var prompt *string
	if options.OneShot {
		prompt, err = cli.ReadPrompt(options, stdin, stdinTTY)
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return 1
		}
	}
	session.StartPrune(store.Int("session_retention_days"), resumePath)
	defer session.ShutdownPrune()

	resolver := runtime.NewResolver(store, providers)
	resolver.SetDiagnosticWriter(stderr)
	providerWasSelected := store.StringNonempty("provider") != nil
	diagnosticsBeforeProvider := diag.Sequence()
	provider, err := selectProvider(store, providers, resolver)
	if err != nil {
		if options.OneShot {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return 1
		}
		if !strings.Contains(err.Error(), "no provider available") {
			fmt.Fprintf(stderr, "gax: %v\n", err)
		}
		provider = nil
	}
	if provider != nil {
		for _, warning := range provider.Warnings() {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		if options.OneShot && diag.Sequence() != diagnosticsBeforeProvider {
			_, _ = io.WriteString(stderr, "\n")
		}
	}
	providerAutoselected := provider != nil && !providerWasSelected
	model := store.StringNonempty("model")
	if provider != nil && provider.Definition.Hooks&registry.HookDiscover != 0 {
		var warning string
		model, warning, err = discoverProviderModel(provider, store, transport.DefaultClient, model)
		if warning != "" {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		if err != nil {
			if options.OneShot {
				fmt.Fprintf(stderr, "gax: %v\n", err)
				return 1
			}
			fmt.Fprintf(stderr, "gax: %v\n", err)
			provider = nil
			model = nil
			store.SetOverride("model", stringPtr(config.DefaultSentinel))
		}
	}
	if model == nil && provider != nil && provider.DefaultModel != "" {
		model = stringPtr(provider.DefaultModel)
	}
	if model == nil && options.OneShot {
		providerName := "?"
		if provider != nil {
			providerName = terminalSafeText(provider.DisplayName)
		}
		fmt.Fprintf(stderr, "gax: no model selected for provider '%s' — pass --model or set GAX_MODEL\n", providerName)
		return 1
	}
	shouldAutoCompact := func(contextTokens int64) bool {
		if !store.Bool("compact.auto") || provider == nil {
			return false
		}
		selectedModel := ""
		if model != nil {
			selectedModel = *model
		}
		return agent.CompactOverThreshold(contextTokens, provider.ContextWindowForModel(selectedModel), store.Int("compact.threshold"))
	}
	tools := coretools.NewFromConfig(store)
	tools.SetDiagnosticWriter(stderr)
	toolsClosed := false
	closeTools := func() {
		if toolsClosed {
			return
		}
		toolsClosed = true
		tools.Close()
	}
	var stopSignalHandlers func()
	defer func() {
		if stopSignalHandlers != nil {
			stopSignalHandlers()
		}
	}()
	defer closeTools()
	state := agent.NewSession()
	if loaded != nil {
		state.Adopt(loaded)
		state.ResumeFromTail()
	}
	if provider != nil {
		state.ProviderID = stringPtr(provider.Definition.ID)
	} else if loaded == nil {
		state.ProviderID = nil
	}
	state.Model = cloneString(model)
	state.ModelLabel = cloneString(model)
	if provider != nil && model != nil && loaded != nil && loaded.Meta.ModelLabel != nil && loaded.Meta.Model != nil && *loaded.Meta.Model == *model &&
		loaded.Meta.Provider != nil && *loaded.Meta.Provider == provider.Definition.ID {
		state.ModelLabel = cloneString(loaded.Meta.ModelLabel)
	}
	state.Effort = store.String("effort")
	if state.Effort == nil && provider != nil && provider.DefaultEffort != "" {
		state.Effort = stringPtr(provider.DefaultEffort)
	}
	if provider != nil && model != nil {
		state.Effort = provider.ResolveEffort(*model, state.Effort)
	}
	if options.Bare {
		// --bare promises no skills; with skills served by a tool, that means not offering it.
		store.SetOverride("no_skills", stringPtr("on"))
	}
	if !options.Raw {
		state.Tools = tools.Definitions()
	}
	promptOptions := agent.PromptOptions{Bare: options.Bare}
	if !options.Raw && !store.Bool("no_env") {
		basePrompt := store.String("system_prompt")
		if basePrompt == nil || *basePrompt != "(none)" {
			promptOptions.Shell = tools.ResolveShell()
		}
	}
	selectedModel := ""
	if model != nil {
		selectedModel = *model
	}
	promptResult := agent.ConfigureSession(state, store, selectedModel, options.Raw, promptOptions)
	for _, warning := range promptResult.Warnings {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
	var transcriptLog *transcript.Log
	if path := store.StringNonempty("transcript"); path != nil {
		transcriptLog, err = transcript.Open(*path, state.Context())
		if err != nil {
			writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: cannot open '%s' for writing", terminalSafeText(*path))
			transcriptLog = nil
		} else if transcriptLog != nil {
			context := state.Context()
			if err := transcriptLog.Sync(context, context.Items); err != nil {
				writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: couldn't write '%s'", terminalSafeText(*path))
				_ = transcriptLog.Close()
				transcriptLog = nil
			}
		}
	}
	if transcriptLog != nil {
		defer func() {
			if err := transcriptLog.Close(); err != nil {
				writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: couldn't close transcript")
			}
		}()
	}
	if loaded != nil && provider != nil && model != nil && state.HasReportedUsage() && store.Bool("compact.auto") {
		provider.WaitModelMetadata(runtime.ModelMetadataWait, nil)
	}
	state.CompactionDeferred = shouldAutoCompact(state.LastContextTokens())

	promptHistoryPersist := recordingEnabled(store, provider, options.NoSession)
	var log *session.Log
	if promptHistoryPersist {
		cwd, _ := os.Getwd()
		selection := session.Selection{Provider: state.ProviderID, Model: state.Model, ModelLabel: state.ModelLabel, Effort: state.Effort, Preset: currentPreset(store)}
		if resumePath != "" {
			log, err = session.ResumeLog(resumePath, selection, len(state.Items))
		} else {
			log, err = session.NewLog(cwd, selection)
		}
		if err != nil {
			writeWarningDiagnostic(stderr, "conversation recording disabled: %v", err)
			log = nil
		}
	}
	if log != nil && options.OneShot {
		if err := log.Begin(); err != nil {
			writeWarningDiagnostic(stderr, "conversation recording disabled: %v", err)
			_ = log.Close()
			log = nil
		}
	}
	if log != nil {
		defer func() { _ = log.Close() }()
	}
	var sessionID *string
	if log != nil {
		sessionID = log.ID()
	}
	var jsonOutput *jsonRun
	if options.JSON {
		jsonOutput = &jsonRun{writer: stdout, cursor: len(state.Items), started: time.Now()}
		if err := jsonOutput.header(state, sessionID, currentPreset(store)); err != nil {
			fmt.Fprintf(stderr, "gax: write JSON header: %v\n", err)
			return 1
		}
	}
	liveTTY := !options.OneShot && !options.JSON && terminalinput.TTYPair(stdin, stdout, stdinTTY)
	ctx, cancelSignals := context.WithCancel(context.Background())
	stopSignalEscalation := watchTerminationEscalation(cancelSignals, tools.KillChildren, func() {
		// The REPL hides the cursor for the duration of a turn; a fatal signal ends the process
		// without unwinding it, so the cursor is restored here (the restore path).
		if liveTTY {
			showCursor(stdout)
		}
	})
	stopSignalHandlers = func() {
		stopSignalEscalation()
		cancelSignals()
	}
	if provider != nil {
		modelID := ""
		if model != nil {
			modelID = *model
		}
		startModelProbe(ctx, provider, modelID, transport.DefaultClient)
	}
	defer func() {
		if provider != nil {
			provider.Close()
		}
	}()
	var pauseSignals chan os.Signal
	var pauseRequested atomic.Bool
	if options.OneShot {
		pauseSignals = make(chan os.Signal, 1)
		signal.Notify(pauseSignals, syscall.SIGUSR1)
		defer signal.Stop(pauseSignals)
	}
	takePauseSignal := func() bool {
		if pauseSignals != nil {
			select {
			case <-pauseSignals:
				pauseRequested.Store(true)
			default:
			}
		}
		return pauseRequested.Load()
	}
	if !options.OneShot && !options.JSON {
		if _, err := io.WriteString(stdout, "\n"); err != nil {
			fmt.Fprintf(stderr, "gax: write banner: %v\n", err)
			return 1
		}
		if err := printIdentityBanner(stdout, provider, state, store, true); err != nil {
			fmt.Fprintf(stderr, "gax: write banner: %v\n", err)
			return 1
		}
		if _, err := io.WriteString(stdout, "\n"); err != nil {
			fmt.Fprintf(stderr, "gax: write banner: %v\n", err)
			return 1
		}
		if loaded != nil {
			_ = replayRecentTurn(stdout, state, "resumed", provider, store, tools, liveTTY)
		}
	}
	if options.OneShot && jsonOutput == nil {
		writeOneShotStartBanner(stderr, state, provider, store, log, providerAutoselected, resumePath != "")
	}
	syncTranscriptLog := func() {
		if transcriptLog == nil {
			return
		}
		context := state.Context()
		if err := transcriptLog.Sync(context, context.Items); err != nil {
			writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: couldn't update transcript")
			_ = transcriptLog.Close()
			transcriptLog = nil
		}
	}
	taskNotesFinalized := false
	finalizeTaskNotes := func() {
		if taskNotesFinalized {
			return
		}
		taskNotesFinalized = true
		note := tools.ExitTaskNotes()
		if err := appendTaskExitNote(state, log, note); err != nil {
			writeWarningDiagnostic(stderr, "couldn't write conversation: %v", err)
		}
		syncTranscriptLog()
	}
	defer finalizeTaskNotes()
	flush := func() {
		if log != nil {
			if err := log.Append(state.Items); err != nil {
				writeWarningDiagnostic(stderr, "couldn't write conversation: %v", err)
			}
		}
		if jsonOutput != nil {
			_ = jsonOutput.items(state.Items)
		}
		syncTranscriptLog()
	}
	compactCancelled := false
	compactContext := ctx
	var compactInterruptTick func() bool
	if options.OneShot {
		compactInterruptTick = func() bool { return ctx.Err() != nil || takePauseSignal() }
	}
	compact := func() agent.CompactResult {
		resyncCompactionEffort(provider, state, store, log)
		selectedModel := ""
		if model != nil {
			selectedModel = *model
		}
		tick := compactInterruptTick
		var interrupts *terminalinput.InterruptWatch
		if tick == nil {
			interrupts, tick = startCompactionInterruptWatch(stdin, liveTTY)
		}
		if interrupts != nil {
			defer interrupts.Stop()
		}
		result := agent.CompactConversation(agent.CompactOptions{
			Context: compactContext, Session: state, Provider: provider, HTTPClient: transport.DefaultClient,
			Model: selectedModel, SessionID: sessionID, Tick: tick,
		})
		if result.Attempts > 0 {
			flush()
		}
		if result.Outcome == agent.CompactComplete {
			state.CompactionDeferred = false
		}
		compactCancelled = compactCancelled || result.Outcome == agent.CompactCancelled
		return result
	}
	var oneShotStarted time.Time
	runState := func(continued bool, submittedText string) (agent.LoopResult, string) {
		compactCancelled = false
		runContext, cancelRun := context.WithCancel(ctx)
		defer cancelRun()
		turnContext, cancelTurn := context.WithCancel(runContext)
		defer cancelTurn()
		var streamContent atomic.Bool
		previousCompactContext := compactContext
		compactContext = runContext
		defer func() { compactContext = previousCompactContext }()
		var interrupts *terminalinput.InterruptWatch
		if liveTTY {
			if terminal, ok := stdin.(*os.File); ok {
				interrupts, _ = terminalinput.StartInterruptWatch(terminal, func() {
					if !streamContent.Load() {
						cancelTurn()
					}
				}, func() {
					cancelTurn()
					cancelRun()
				})
				if interrupts != nil {
					defer interrupts.Stop()
				}
			}
		}
		previousCompactInterruptTick := compactInterruptTick
		if interrupts != nil {
			compactInterruptTick = func() bool {
				return interrupts.PauseRequested() || interrupts.AbortRequested() || takePauseSignal()
			}
		}
		defer func() { compactInterruptTick = previousCompactInterruptTick }()
		var renderer *terminalLive.Renderer
		if liveTTY {
			physicalColumns, _ := terminalwidth.WriterColumns(stdout)
			columns := markdownDisplayColumns(store, physicalColumns)
			styled := store.Bool("markdown")
			if !styled {
				columns = 0
			}
			renderer = terminalLive.NewRenderer(stdout, columns, styled, store.Bool("show_reasoning"))
			renderer.SetMarkdownEnabled(styled)
			if submittedText != "" {
				renderer.SyncExternalLine()
			}
		}
		loopOptions := agent.LoopOptions{
			Session: state, Provider: provider, HTTPClient: transport.DefaultClient, Model: *model,
			SessionID: sessionID, MaxTurns: store.Int("max_turns"), Continued: continued, ToolRegistry: tools,
			CollectTaskNotes: tools.CollectTaskNotes,
			Flush:            flush,
			ShouldCompact: func(contextTokens, _ int64) bool {
				return shouldAutoCompact(contextTokens)
			},
			Compact: func() { compact() },
		}
		loopOptions.Checkpoint = func() agent.Signal {
			if runContext.Err() != nil || (interrupts != nil && interrupts.AbortRequested()) {
				return agent.SignalAbort
			}
			if compactCancelled {
				if (interrupts != nil && interrupts.PauseRequested() || takePauseSignal()) &&
					(interrupts == nil || !interrupts.AbortRequested()) {
					return agent.SignalPause
				}
				return agent.SignalAbort
			}
			if (interrupts != nil && interrupts.PauseRequested()) || takePauseSignal() {
				return agent.SignalPause
			}
			if renderer != nil && renderer.Err() != nil {
				return agent.SignalAbort
			}
			return agent.SignalNone
		}
		if renderer != nil {
			toolHeaders := newToolHeaders(renderer)
			loopOptions.TaskNote = func(note string) { _ = renderer.TaskNote(note) }
			loopOptions.Observe = func(event conversation.Event) {
				switch event.Kind {
				case conversation.EventTextDelta, conversation.EventToolCallStart,
					conversation.EventReasoningItem, conversation.EventReasoningDelta:
					streamContent.Store(true)
				}
				_ = renderer.Event(event)
			}
			loopOptions.Tick = func() bool {
				if interrupts != nil && interrupts.AbortRequested() {
					return true
				}
				now := time.Now()
				if interrupts != nil && interrupts.PauseRequested() {
					if renderer.PausePending(now) != nil || !streamContent.Load() {
						return true
					}
				}
				return renderer.Tick(now) != nil
			}
			loopOptions.TurnBegin = func() {
				_ = renderer.Begin()
				toolHeaders.reset()
			}
			loopOptions.ToolSeen = func(call conversation.Item) {
				toolHeaders.seen(call)
			}
			loopOptions.ToolCall = func(call conversation.Item, action agent.ToolAction, imageInput int) conversation.Item {
				presentation := tools.Presentation(call)
				toolHeaders.start(call, presentation)
				if action == agent.ToolRun {
					execution := tools.ExecuteWithDisplayContext(runContext, call, imageInput, func(data []byte) error {
						return renderer.ToolOutput(data)
					})
					if execution.OutputSummarizesDisplay {
						execution.Result.Origin = conversation.OriginSummarized
					}
					_ = renderer.ToolResult(execution)
					return execution.Result
				}
				output, origin := agent.RefusedResult, conversation.OriginRefused
				if action == agent.ToolSkip {
					output, origin = agent.SkippedResult, conversation.OriginSkipped
				}
				item := agent.ToolResult(call, output, origin)
				_ = renderer.ToolResult(coretools.ToolExecution{Result: item, Presentation: presentation})
				return item
			}
		}
		if jsonOutput != nil {
			loopOptions.Tick = func() bool { return jsonOutput.err != nil }
			loopOptions.TurnBegin = func() { _ = jsonOutput.items(state.Items) }
			loopOptions.ToolSeen = func(conversation.Item) { _ = jsonOutput.items(state.Items) }
			previousCheckpoint := loopOptions.Checkpoint
			loopOptions.Checkpoint = func() agent.Signal {
				signal := previousCheckpoint()
				if signal == agent.SignalAbort {
					return signal
				}
				if jsonOutput.items(state.Items) != nil {
					return agent.SignalAbort
				}
				return signal
			}
		}
		providerID := ""
		if state.ProviderID != nil {
			providerID = *state.ProviderID
		}
		if provider != nil {
			modelID := ""
			if model != nil {
				modelID = *model
			}
			startModelProbe(runContext, provider, modelID, transport.DefaultClient)
			probeReady := provider.WaitModelMetadata(runtime.ModelMetadataWait, func() bool {
				return runContext.Err() != nil || (interrupts != nil &&
					(interrupts.AbortRequested() || interrupts.PauseRequested())) || takePauseSignal()
			})
			if probeReady {
				// Keep the configured request available when startup metadata could not resolve it.
				state.Effort = provider.ResolveEffort(*model, store.String("effort"))
			}
		}
		// Match the run clock: startup metadata work is complete before timing begins.
		started := time.Now()
		if options.OneShot {
			oneShotStarted = started
			if jsonOutput != nil {
				jsonOutput.started = started
			}
		}
		tools.SetSelection(providerID, state.Model, state.Effort)
		var result agent.LoopResult
		terminalkeepawake.Run(store.Bool("keep_awake"), terminalkeepawake.Start, func() {
			result = agent.RunLoop(turnContext, loopOptions)
		})
		if shouldAutoCompact(result.LastContextTokens) {
			switch result.Outcome {
			case agent.LoopComplete:
				compaction := compact()
				if compaction.Outcome == agent.CompactCancelled {
					if (interrupts != nil && interrupts.PauseRequested() || takePauseSignal()) &&
						(interrupts == nil || !interrupts.AbortRequested()) && runContext.Err() == nil {
						state.CompactionDeferred = true
						result.Outcome = agent.LoopPaused
					} else {
						state.MarkInterrupt()
						result.Outcome = agent.LoopInterrupted
					}
					flush()
				}
			case agent.LoopPaused, agent.LoopMaxTurns:
				state.CompactionDeferred = true
			}
		}
		answer := collectAssistantText(state.Items, result.FinalItemsFrom, result.FinalItemsTo)
		if renderer != nil {
			if result.Outcome == agent.LoopInterrupted {
				if err := renderer.Interrupt(); err != nil {
					result.Outcome = agent.LoopProviderError
					result.ErrorMessage = "write output: " + err.Error()
				}
			}
			if err := renderer.Separate(); err != nil {
				result.Outcome = agent.LoopProviderError
				result.ErrorMessage = "write output: " + err.Error()
			}
			if err := renderer.Close(); err != nil {
				result.Outcome = agent.LoopProviderError
				result.ErrorMessage = "write output: " + err.Error()
			}
			if err := renderer.Err(); err != nil {
				result.Outcome = agent.LoopProviderError
				result.ErrorMessage = "write output: " + err.Error()
			}
			if renderer.HasRenderedAnswer() {
				answer = ""
			}
		}
		state.ResumeReason = resumeReasonForOutcome(result.Outcome)
		elapsedMS := time.Since(started).Milliseconds()
		state.AddWorked(elapsedMS)
		if log != nil {
			_ = log.Append(state.Items)
			_ = log.UserTurn(elapsedMS)
		}
		if liveTTY && result.Outcome != agent.LoopProviderError {
			if err := writeTurnStatsLineWithStyle(stdout, state, provider, result.LastContextTokens,
				elapsedMS, store, true); err != nil {
				result.Outcome = agent.LoopProviderError
				result.ErrorMessage = "write output: " + err.Error()
				state.ResumeReason = resumeReasonForOutcome(result.Outcome)
			} else if _, err := io.WriteString(stdout, "\n"); err != nil {
				result.Outcome = agent.LoopProviderError
				result.ErrorMessage = "write output: " + err.Error()
				state.ResumeReason = resumeReasonForOutcome(result.Outcome)
			}
		}
		return result, answer
	}
	run := func(userText string) (agent.LoopResult, string) {
		if provider == nil {
			writeNoteLine(stdout, "no provider selected — use /provider to choose one, then resend")
			return agent.LoopResult{Outcome: agent.LoopComplete}, ""
		}
		if model == nil || *model == "" {
			writeNoteLine(stdout, "no model selected — use /model (or /provider) to choose one, then resend")
			return agent.LoopResult{Outcome: agent.LoopComplete}, ""
		}
		if state.CompactionDeferred {
			compaction := compact()
			if compaction.Outcome == agent.CompactCancelled {
				if takePauseSignal() && ctx.Err() == nil {
					return agent.LoopResult{Outcome: agent.LoopPaused}, ""
				}
				return agent.LoopResult{Outcome: agent.LoopInterrupted}, ""
			}
			state.CompactionDeferred = false
		}
		continued := false
		if strings.TrimSpace(userText) == "" {
			switch state.ResumeTail() {
			case agent.ResumeTailMarked:
				state.AddContinuation()
			case agent.ResumeTailUser:
				// Retry the unanswered user item already at the tail.
			case agent.ResumeTailClean, agent.ResumeTailEmpty:
				continued = true
			}
		} else {
			state.AddUser(userText)
		}
		return runState(continued, userText)
	}
	runREPLTurn := func(userText string) replOutput {
		result, answer := run(userText)
		output := replOutput{result: result, answer: answer}
		if !liveTTY && provider != nil && model != nil && *model != "" && result.Outcome != agent.LoopProviderError {
			output.afterAnswer = func(writer io.Writer) error {
				if display, ok := writer.(*lineOutputWriter); ok {
					display.BlockSeparator()
				}
				return writeTurnStatsLineWithStyle(writer, state, provider, result.LastContextTokens,
					state.LastUserTurnMS, store, true)
			}
		}
		return output
	}

	oneShotStarted = time.Now()
	oneShotItemsFrom, oneShotRetiredFrom := len(state.Items), len(state.Retired)
	finishOneShot := func(result agent.LoopResult, answer string) int {
		finalizeTaskNotes()
		closeTools()
		var usage agent.UsageTotals
		if jsonOutput != nil {
			_, stats := collectOneShotStats(state, provider, oneShotItemsFrom, oneShotRetiredFrom)
			usage = stats.Total
			_ = jsonOutput.items(state.Items)
		}
		code := finishRun(result, answer, jsonOutput, stdout, stderr, store, usage)
		if jsonOutput == nil {
			if flusher, ok := stdout.(interface{ Flush() error }); ok {
				_ = flusher.Flush()
			}
			writeOneShotExitNotes(stderr, state, provider, log, result.LastContextTokens,
				time.Since(oneShotStarted).Milliseconds(), oneShotItemsFrom, oneShotRetiredFrom, store)
		}
		return code
	}
	if options.OneShot {
		if prompt == nil {
			if loaded == nil {
				fmt.Fprintln(stderr, "gax: a prompt is required for this run")
				return 1
			}
			if state.CompactionDeferred {
				compaction := compact()
				if compaction.Outcome == agent.CompactCancelled {
					if takePauseSignal() && ctx.Err() == nil {
						state.CompactionDeferred = true
						return finishOneShot(agent.LoopResult{Outcome: agent.LoopPaused, LastContextTokens: -1}, "")
					}
					return finishOneShot(agent.LoopResult{Outcome: agent.LoopInterrupted, LastContextTokens: -1}, "")
				}
				state.CompactionDeferred = false
			}
			state.AddContinuation()
			result, text := runState(false, "")
			return finishOneShot(result, text)
		}
		result, text := run(*prompt)
		return finishOneShot(result, text)
	}
	startNew := func() error {
		note := tools.ExitTaskNotes()
		if err := appendTaskExitNote(state, log, note); err != nil {
			writeWarningDiagnostic(stderr, "couldn't write conversation: %v", err)
		}
		tools.ResetTasks()
		if log != nil {
			if err := log.Reset(); err != nil {
				return err
			}
		}
		_ = terminalpaste.Cleanup()
		state.Reset()
		if transcriptLog != nil {
			if err := transcriptLog.Reset(state.Context(), nil); err != nil {
				writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: couldn't reset transcript")
				_ = transcriptLog.Close()
				transcriptLog = nil
			}
		}
		if _, lineOriented := stdout.(*lineOutputWriter); !lineOriented {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				return err
			}
		}
		if err := printIdentityBanner(stdout, provider, state, store, true); err != nil {
			return err
		}
		sessionID = nil
		if log != nil {
			sessionID = log.ID()
		}
		return nil
	}
	var pendingInput string
	if !liveTTY {
		stdout = newLineOutputWriter(stdout, 2)
	}
	argumentChoices := func(name string) []string {
		return slashArgumentChoices(name, providers, provider, model, store)
	}
	replCode := runREPL(ctx, stdin, stdinTTY, promptHistoryPersist, stdout, stderr, store, tools, state, runREPLTurn, &pendingInput, argumentChoices, func(parsed slash.Parsed) bool {
		defer syncTranscriptLog()
		defer configureTerminalTheme(store, stderr)
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "preset" {
			selectedProvider, selectedModel, applied := runPresetCommand(ctx, parsed, resolver, provider, model,
				state, log, store, stdin, options.Raw, promptOptions, stdout, stderr)
			if applied {
				replaceProvider(&provider, selectedProvider)
				model = selectedModel
				if err := announceSelection(stdout, stderr, provider, state, store, true); err != nil {
					fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
				}
			}
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "config" {
			runConfigCommand(parsed, store, stdin, &pendingInput, liveTTY, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "preset-save" {
			selectedProvider, selectedModel, applied := runPresetSaveCommand(ctx, parsed, resolver, provider, model,
				state, log, store, stdin, &pendingInput, liveTTY, options.Raw, promptOptions, stdout, stderr)
			if applied {
				replaceProvider(&provider, selectedProvider)
				model = selectedModel
				if err := announceSelection(stdout, stderr, provider, state, store, true); err != nil {
					fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
				}
			}
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "new" && parsed.HasArgument {
			selectedProvider, selectedModel, applied := runPresetCommand(ctx, parsed, resolver, provider, model,
				state, log, store, stdin, options.Raw, promptOptions, stdout, stderr)
			if !applied {
				return true
			}
			replaceProvider(&provider, selectedProvider)
			model = selectedModel
			if err := startNew(); err != nil {
				fmt.Fprintf(stderr, "gax: start new conversation: %v\n", err)
			}
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "provider" {
			selectedProvider, selectedModel := runProviderCommand(ctx, parsed, providers, resolver, provider, model, state, log, store,
				stdin, stdinTTY, options.Raw, promptOptions, stdout, stderr)
			replaceProvider(&provider, selectedProvider)
			model = selectedModel
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "model" {
			model = runModelCommand(ctx, parsed, provider, model, state, log, store, stdin, stdinTTY, options.Raw,
				promptOptions, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "effort" {
			runEffortCommand(ctx, parsed, provider, model, state, log, store, stdin, stdinTTY, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "usage" {
			runProviderUsage(ctx, provider, transport.DefaultClient,
				editorDisplayColumns(store, terminalwidth.TerminalColumns()), stdin, stdinTTY, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "copy" {
			runCopyCommand(ctx, state, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "tasks" {
			runTasksCommand(parsed, tools, store.Bool("no_tasks"), store, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "compact" {
			runCompactCommand(ctx, parsed, provider, model, state, sessionID, log, store, stdin, liveTTY, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "login" {
			runLoginCommand(ctx, parsed, stdin, liveTTY, store, resolver, &provider, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "logout" {
			runLogoutCommand(ctx, parsed, stdin, liveTTY, store, resolver, &provider, stdout, stderr)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "session" {
			printSessionSummary(stdout, state, provider, resolver, log, store)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "undo" {
			selected, cancelled := pickHistoryCommand(parsed, state, store, stdin, stdout, stderr, liveTTY)
			if cancelled {
				return true
			}
			handleUndo(selected, state, log, stdout, stderr, tools, liveTTY)
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "fork" {
			selected, cancelled := pickHistoryCommand(parsed, state, store, stdin, stdout, stderr, liveTTY)
			if cancelled {
				return true
			}
			parsed = selected
			selection := session.Selection{
				Provider: state.ProviderID, Model: state.Model, ModelLabel: state.ModelLabel,
				Effort: state.Effort, Preset: currentPreset(store),
			}
			if forked, err := handleFork(parsed, state, log, selection, stdout, stderr, tools, liveTTY); err == nil && forked != nil {
				log = forked
				sessionID = log.ID()
			}
			return true
		}
		if parsed.Outcome == slash.Handled && parsed.Command != nil && parsed.Command.Name == "resume" {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(stderr, "gax: resume: %v\n", err)
				return true
			}
			selected, _, err := pickSession(cwd, log.Path(), store, stdin, stdinTTY, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "gax: resume picker: %v\n", err)
				return true
			}
			if selected == nil {
				if _, ok := stdout.(*lineOutputWriter); ok {
					fmt.Fprintln(stdout)
				}
				return true
			}
			loaded, path, err := loadResumePath(selected.Path, options, store)
			if err != nil {
				var warning *resumePresetWarning
				if errors.As(err, &warning) && loaded != nil {
					writeNoteLine(stdout, warning.Error())
				} else {
					fmt.Fprintf(stderr, "gax: resume: %v\n", err)
					return true
				}
			}
			resumedProvider, err := selectProvider(store, providers, resolver)
			if err != nil {
				fmt.Fprintf(stderr, "gax: resume: %v\n", err)
				return true
			}
			resumedModel := store.StringNonempty("model")
			if resumedProvider.Definition.Hooks&registry.HookDiscover != 0 {
				var warning string
				resumedModel, warning, err = discoverProviderModel(resumedProvider, store, transport.DefaultClient, resumedModel)
				if warning != "" {
					writeWarningDiagnostic(stderr, "%s", warning)
				}
				if err != nil {
					fmt.Fprintf(stderr, "gax: resume: %v\n", err)
					return true
				}
			}
			for _, warning := range resumedProvider.Warnings() {
				writeWarningDiagnostic(stderr, "%s", warning)
			}
			resumedModelName := ""
			if resumedModel != nil {
				resumedModelName = *resumedModel
			}
			resumedLabel := cloneString(resumedModel)
			if resumedModel != nil && loaded.Meta.ModelLabel != nil && loaded.Meta.Model != nil && *loaded.Meta.Model == *resumedModel &&
				loaded.Meta.Provider != nil && *loaded.Meta.Provider == resumedProvider.Definition.ID {
				resumedLabel = cloneString(loaded.Meta.ModelLabel)
			}

			note := tools.ExitTaskNotes()
			if err := appendTaskExitNote(state, log, note); err != nil {
				writeWarningDiagnostic(stderr, "couldn't write conversation: %v", err)
			}
			tools.ResetTasks()
			if log != nil {
				if err := log.Close(); err != nil {
					writeWarningDiagnostic(stderr, "couldn't close conversation: %v", err)
				}
			}

			state.Adopt(loaded)
			state.ResumeFromTail()
			replaceProvider(&provider, resumedProvider)
			model = resumedModel
			modelID := ""
			if model != nil {
				modelID = *model
			}
			startModelProbe(ctx, provider, modelID, transport.DefaultClient)
			state.ProviderID = stringPtr(resumedProvider.Definition.ID)
			state.Model = cloneString(resumedModel)
			state.ModelLabel = resumedLabel
			state.Effort = resumedProvider.ResolveEffort(modelID, store.String("effort"))
			if options.Raw {
				state.Tools = nil
			} else {
				state.Tools = tools.Definitions()
			}
			promptResult := agent.ConfigureSession(state, store, resumedModelName, options.Raw, promptOptions)
			for _, warning := range promptResult.Warnings {
				writeWarningDiagnostic(stderr, "%s", warning)
			}
			if transcriptLog != nil {
				context := state.Context()
				if err := transcriptLog.Reset(context, context.Items); err != nil {
					writeWarningDiagnostic(stderr, "GAX_TRANSCRIPT: couldn't reset transcript")
					_ = transcriptLog.Close()
					transcriptLog = nil
				}
			}
			if provider != nil && model != nil && state.HasReportedUsage() && store.Bool("compact.auto") {
				provider.WaitModelMetadata(runtime.ModelMetadataWait, nil)
			}
			state.CompactionDeferred = shouldAutoCompact(state.LastContextTokens())
			promptHistoryPersist = recordingEnabled(store, provider, options.NoSession)
			log = nil
			if promptHistoryPersist {
				selection := session.Selection{
					Provider: state.ProviderID, Model: state.Model, ModelLabel: state.ModelLabel,
					Effort: state.Effort, Preset: currentPreset(store),
				}
				log, err = session.ResumeLog(path, selection, len(state.Items))
				if err != nil {
					writeWarningDiagnostic(stderr, "conversation recording disabled: %v", err)
					log = nil
				}
			}
			sessionID = nil
			if log != nil {
				sessionID = log.ID()
			}
			_ = terminalpaste.Cleanup()
			_ = replayRecentTurn(stdout, state, "resumed", provider, store, tools, liveTTY)
			return true
		}
		return handleSlash(parsed, stdout, stderr, store, startNew)
	})
	finalizeTaskNotes()
	if log != nil {
		if hint := log.ResumeHint(); hint != nil && *hint != "" {
			writeNoteLine(stdout, "resume with: gax --resume="+*hint)
		}
	}
	return replCode
}

type resumePresetWarning struct {
	message string
}

func (warning *resumePresetWarning) Error() string { return warning.message }

const (
	configTierRun configTier = iota
	configTierConversation
)

func (stream *jsonRun) header(state *agent.Session, sessionID *string, preset *string) error {
	stream.sessionID = cloneString(sessionID)
	cwd, _ := os.Getwd()
	var cwdValue *string
	if cwd != "" {
		cwdValue = &cwd
	}
	header := session.NewHeaderRecord(session.HeaderInput{
		ID: sessionID, CWD: cwdValue, Provider: state.ProviderID, Model: state.Model,
		ModelLabel: state.ModelLabel, Effort: state.Effort, Preset: preset,
	})
	return stream.record(header)
}

func (stream *jsonRun) items(items []conversation.Item) error {
	if stream.err != nil {
		return stream.err
	}
	for stream.cursor < len(items) {
		if err := stream.record(items[stream.cursor]); err != nil {
			return err
		}
		stream.cursor++
	}
	return nil
}

func (stream *jsonRun) record(value any) error {
	if stream.err != nil {
		return stream.err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(value)
	if err == nil {
		data := encoded.Bytes()
		var written int
		written, err = stream.writer.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		stream.err = err
		return err
	}
	if flusher, ok := stream.writer.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			stream.err = err
			return err
		}
	}
	return nil
}

func (stream *jsonRun) finish(result agent.LoopResult, answer string, usage agent.UsageTotals) error {
	if err := stream.items(nil); err != nil {
		return err
	}
	record := jsonResultRecord{
		Type: "result", Outcome: loopOutcomeName(result.Outcome), Turns: result.Turns,
		ElapsedMS: time.Since(stream.started).Milliseconds(),
	}
	if result.Outcome == agent.LoopComplete && answer != "" {
		record.Text = answer
	}
	if result.ErrorMessage != "" {
		record.Error = result.ErrorMessage
	}
	if result.LastContextTokens >= 0 {
		record.ContextTokens = &result.LastContextTokens
	}
	if usage.Spend > 0 {
		record.Cost = &usage.Spend
		record.CostEstimated = usage.SpendEstimated
	}
	if stream.sessionID != nil {
		record.SessionID = cloneString(stream.sessionID)
	}
	return stream.record(record)
}

// toolHeaderRenderer is the live renderer surface toolHeaders drives.
type toolHeaderRenderer interface {
	ToolStart(conversation.Item, coretools.ToolPresentation) error
}

// toolHeaders decides when a live tool header is written. The loop announces a whole batch of
// calls before dispatching any of them, while the renderer
// shows one tool at a time: starting a second call's header before the first call's result takes
// over its preview renderer and drops the first call's streamed output. A header is therefore
// written when that call's dispatch begins, for every call the batch announced.
type toolHeaders struct {
	renderer  toolHeaderRenderer
	announced map[string]bool
}

func newToolHeaders(renderer toolHeaderRenderer) *toolHeaders {
	return &toolHeaders{renderer: renderer, announced: make(map[string]bool)}
}

// reset starts a new turn, dropping any call the previous turn announced but never dispatched.
func (headers *toolHeaders) reset() {
	clear(headers.announced)
}

// seen records a call the loop announced.
func (headers *toolHeaders) seen(call conversation.Item) {
	headers.announced[toolCallKey(call)] = true
}

// start writes the header for a call whose dispatch is beginning, once.
func (headers *toolHeaders) start(call conversation.Item, presentation coretools.ToolPresentation) {
	key := toolCallKey(call)
	if !headers.announced[key] {
		// A call the loop never announced, such as one restored from a session, is left to the
		// result path, which writes a header if none is open.
		return
	}
	delete(headers.announced, key)
	_ = headers.renderer.ToolStart(call, presentation)
}

// toolCallKey identifies a streamed tool call for header bookkeeping.
func toolCallKey(call conversation.Item) string {
	if call.CallID == nil {
		return ""
	}
	return *call.CallID
}

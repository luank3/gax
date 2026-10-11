package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/cli"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/diag"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/deepseek"
	"github.com/luank3/gax/internal/provider/opencodego"
	"github.com/luank3/gax/internal/provider/openrouter"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	systemfs "github.com/luank3/gax/internal/system/fs"
	terminalkeepawake "github.com/luank3/gax/internal/system/keepawake"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	systempath "github.com/luank3/gax/internal/system/path"
	terminalbanner "github.com/luank3/gax/internal/terminal/banner"
	terminaledit "github.com/luank3/gax/internal/terminal/editbuffer"
	"github.com/luank3/gax/internal/terminal/filemention"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalLive "github.com/luank3/gax/internal/terminal/live"
	terminalmarkdown "github.com/luank3/gax/internal/terminal/markdown"
	terminalnotify "github.com/luank3/gax/internal/terminal/notify"
	terminalpaste "github.com/luank3/gax/internal/terminal/paste"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
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
	stopSignalEscalation := watchTerminationEscalation(cancelSignals, tools.KillChildren)
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
			loopOptions.TurnBegin = func() { _ = renderer.Begin() }
			loopOptions.ToolSeen = func(call conversation.Item) {
				_ = renderer.ToolStart(call, tools.Presentation(call))
			}
			loopOptions.ToolCall = func(call conversation.Item, action agent.ToolAction, imageInput int) conversation.Item {
				presentation := tools.Presentation(call)
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
		// Match the upstream run clock: startup metadata work is complete before timing begins.
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
	replCode := runREPL(ctx, stdin, stdinTTY, promptHistoryPersist, stdout, stderr, store, tools, state, runREPLTurn, &pendingInput, func(parsed slash.Parsed) bool {
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

func startModelProbe(ctx context.Context, provider *runtime.Provider, model string,
	client *transport.Client) bool {
	if provider == nil || model == "" || !provider.CanProbeSelectedModel() {
		return false
	}
	return provider.StartModelProbe(ctx, model, client)
}

func waitForBusyModelMetadata(ctx context.Context, provider *runtime.Provider, model string,
	stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) bool {
	busy := startBusyOperation(ctx, stdin, stdinTTY, stdout, "fetching model catalog...")
	startModelProbe(busy.Context(), provider, model, transport.DefaultClient)
	ready := provider.WaitModelMetadata(runtime.ModelMetadataWait, busy.Tick)
	cancelled := endBusyOperation(busy, stderr)
	return ready && !cancelled
}

func configureTerminalTheme(store *config.Store, stderr io.Writer) {
	name := "auto"
	if configured := store.String("theme"); configured != nil {
		name = *configured
	}
	tint := "teal"
	if configured := store.String("tint"); configured != nil && *configured != "" {
		tint = *configured
	}
	if store.Source("tint") != "run" {
		if preset := currentPreset(store); preset != nil {
			if values, err := loadPreset(store, *preset); err == nil && values["tint"] != "" {
				tint = values["tint"]
			}
		}
	}
	for _, warning := range terminaltheme.Configure(name, tint) {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
}

func printIdentityBanner(output io.Writer, provider *runtime.Provider, state *agent.Session,
	store *config.Store, includeTips bool) error {
	columns, ok := terminalwidth.WriterColumns(output)
	if !ok || columns <= 0 {
		columns = terminalwidth.TerminalColumns()
	}
	columns = editorDisplayColumns(store, columns)
	identity := terminalbanner.Identity{}
	if provider != nil {
		identity.Provider = provider.DisplayName
		if identity.Provider == "" && provider.Definition != nil {
			identity.Provider = provider.Definition.ID
		}
		identity.Provider = terminalSafeText(identity.Provider)
	}
	if state != nil {
		if state.ModelLabel != nil && *state.ModelLabel != "" {
			identity.Model = terminalSafeText(*state.ModelLabel)
		} else if state.Model != nil {
			identity.Model = terminalSafeText(*state.Model)
		}
		if state.Effort != nil {
			identity.Effort = terminalSafeText(*state.Effort)
		}
	}
	if preset := currentPreset(store); preset != nil {
		identity.Preset = terminalSafeText(*preset)
	}
	if includeTips {
		return terminalbanner.Print(output, identity, columns)
	}
	return terminalbanner.IdentityLine(output, identity, columns)
}

func writeOneShotStartBanner(writer io.Writer, state *agent.Session, provider *runtime.Provider,
	store *config.Store, log *session.Log, autoSelected, resumed bool) {
	dim := terminalnotify.IsTerminalWriter(writer)
	if dim {
		_, _ = io.WriteString(writer, "\x1b[2m")
	}
	if preset := currentPreset(store); preset != nil && *preset != "" {
		_, _ = fmt.Fprintf(writer, "gax [%s]: ", terminalSafeText(*preset))
	} else {
		_, _ = io.WriteString(writer, "gax: ")
	}
	providerName, modelLabel, effort := "?", "?", ""
	if provider != nil {
		providerName = provider.DisplayName
		if providerName == "" && provider.Definition != nil {
			providerName = provider.Definition.ID
		}
	}
	if state != nil {
		if state.ModelLabel != nil && *state.ModelLabel != "" {
			modelLabel = *state.ModelLabel
		} else if state.Model != nil && *state.Model != "" {
			modelLabel = *state.Model
		}
		if state.Effort != nil {
			effort = *state.Effort
		}
	}
	_, _ = fmt.Fprintf(writer, "%s · %s", terminalSafeText(providerName), terminalSafeText(modelLabel))
	if effort != "" {
		_, _ = fmt.Fprintf(writer, " · %s", terminalSafeText(effort))
	}
	if autoSelected {
		_, _ = io.WriteString(writer, " (auto-selected)")
	} else if resumed {
		_, _ = io.WriteString(writer, " (resumed)")
	}
	if log != nil {
		if hint := log.ResumeHint(); hint != nil && *hint != "" {
			_, _ = fmt.Fprintf(writer, " · session %s", terminalSafeText(*hint))
		}
	}
	if dim {
		_, _ = io.WriteString(writer, "\x1b[0m")
	}
	_, _ = io.WriteString(writer, "\n\n")
}

func announceSelection(output, stderr io.Writer, provider *runtime.Provider, state *agent.Session,
	store *config.Store, bannerWhenEmpty bool) error {
	configureTerminalTheme(store, stderr)
	if bannerWhenEmpty && (state == nil || len(state.Items) == 0) {
		lineOutput, lineOriented := output.(*lineOutputWriter)
		if !lineOriented || lineOutput.trailingNewlines < 2 {
			if _, err := io.WriteString(output, "\n"); err != nil {
				return err
			}
		}
		return printIdentityBanner(output, provider, state, store, true)
	}
	providerName := "?"
	if provider != nil {
		providerName = provider.DisplayName
		if providerName == "" && provider.Definition != nil {
			providerName = provider.Definition.ID
		}
	}
	modelLabel := "no model — use /model"
	if state != nil && state.Model != nil && *state.Model != "" {
		modelLabel = *state.Model
		if state.ModelLabel != nil && *state.ModelLabel != "" {
			modelLabel = *state.ModelLabel
		}
	}
	prefix := ""
	if preset := currentPreset(store); preset != nil && *preset != "" {
		prefix = "[" + terminalSafeText(*preset) + "] "
	}
	message := "switched to " + prefix + terminalSafeText(providerName) + " · " + terminalSafeText(modelLabel)
	if state != nil && state.Model != nil && *state.Model != "" && state.Effort != nil && *state.Effort != "" {
		message += " · " + terminalSafeText(*state.Effort)
	}
	_, err := fmt.Fprintf(output, "\x1b[2m%s\x1b[0m\n", message)
	return err
}

func resumeReasonForOutcome(outcome agent.LoopOutcome) agent.ResumeReason {
	switch outcome {
	case agent.LoopPaused:
		return agent.ResumeReasonPaused
	case agent.LoopMaxTurns:
		return agent.ResumeReasonMaxTurns
	case agent.LoopInterrupted:
		return agent.ResumeReasonInterrupted
	case agent.LoopProviderError:
		return agent.ResumeReasonError
	default:
		return agent.ResumeReasonNone
	}
}

func runProviderCommand(ctx context.Context, parsed slash.Parsed, providers *registry.Registry, resolver *runtime.Resolver,
	currentProvider *runtime.Provider, currentModel *string, state *agent.Session, log *session.Log,
	store *config.Store, stdin io.Reader, stdinTTY, raw bool, promptOptions agent.PromptOptions,
	stdout, stderr io.Writer) (*runtime.Provider, *string) {
	pickedWithPicker := false
	if !parsed.HasArgument {
		if terminalinput.TTYPair(stdin, stdout, stdinTTY) {
			definitions := providers.All()
			sort.SliceStable(definitions, func(i, j int) bool {
				left, right := providers.DisplayName(definitions[i]), providers.DisplayName(definitions[j])
				if left == right {
					return definitions[i].ID < definitions[j].ID
				}
				return left < right
			})
			items := make([]terminalpicker.Item, len(definitions))
			candidates := make([]*runtime.Provider, len(definitions))
			reasons := make([]string, len(definitions))
			var probes sync.WaitGroup
			for index, definition := range definitions {
				candidate, err := resolver.Resolve(definition.ID)
				if err != nil {
					reasons[index] = err.Error()
					continue
				}
				candidates[index] = candidate
				if candidate.Availability.State == runtime.AvailabilityNeedsProbe {
					probes.Add(1)
					go func(provider *runtime.Provider) {
						defer probes.Done()
						provider.ProbeAvailability(ctx, transport.DefaultClient)
					}(candidate)
				}
			}
			probes.Wait()
			initialIndex := 0
			for index, definition := range definitions {
				label := terminalSafeText(providers.DisplayName(definition))
				item := terminalpicker.Item{Label: label,
					Current: currentProvider != nil && currentProvider.Definition != nil && definition.ID == currentProvider.Definition.ID}
				if label != definition.ID {
					item.Description = "id: " + terminalSafeText(definition.ID)
				}
				if candidate := candidates[index]; candidate != nil {
					if !candidate.Availability.Ready() {
						item.Dim = true
						item.Detail = terminalSafeText(candidate.Availability.Reason)
					}
				} else {
					item.Dim = true
					item.Detail = terminalSafeText(reasons[index])
				}
				items[index] = item
				if item.Current {
					initialIndex = index
				}
			}
			stdinFile, _ := stdin.(*os.File)
			index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
				Title: "select a provider", Items: items, InitialIndex: initialIndex,
				RepeatClippedLabel: true,
				DisplayColumns:     func(columns int) int { return editorDisplayColumns(store, columns) },
			}, stdinFile, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "gax: provider picker: %v\n", err)
				return currentProvider, currentModel
			}
			if index >= 0 && index < len(definitions) {
				parsed.Argument = definitions[index].ID
				parsed.HasArgument = true
				pickedWithPicker = true
			} else {
				return currentProvider, currentModel
			}
		} else {
			return currentProvider, currentModel
		}
	}
	requested := strings.TrimSpace(parsed.Argument)
	if requested == "" {
		fmt.Fprintln(stderr, "gax: /provider needs a provider id")
		return currentProvider, currentModel
	}
	selected, err := resolver.Resolve(requested)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown provider ") {
			fmt.Fprintf(stdout, "unknown provider '%s' — /provider lists them\n", terminalSafeText(requested))
			return currentProvider, currentModel
		}
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return currentProvider, currentModel
	}
	if currentProvider != nil && currentProvider.Definition != nil && currentProvider.Definition.ID == selected.Definition.ID {
		if pickedWithPicker {
			return currentProvider, runModelCommand(ctx, slash.Parsed{}, currentProvider,
				currentModel, state, log, store, stdin, stdinTTY, raw, promptOptions, stdout, stderr)
		}
		writeNoteLine(stdout, fmt.Sprintf("already using %s — /model switches its model", terminalSafeText(selected.DisplayName)))
		return currentProvider, currentModel
	}
	if selected.Availability.State == runtime.AvailabilityNeedsProbe {
		selected.ProbeAvailability(ctx, transport.DefaultClient)
	}
	if !selected.Availability.Ready() {
		fmt.Fprintf(stdout, "%s is unavailable — %s\n", terminalSafeText(selected.DisplayName),
			terminalSafeText(selected.Availability.Reason))
		return currentProvider, currentModel
	}
	if pickedWithPicker {
		selectedModel, selectedEffort, ok := pickProviderModelAndEffort(ctx, selected, stdin, stdinTTY, store, stdout, stderr)
		if !ok {
			return currentProvider, currentModel
		}
		return commitProviderSelection(selected, selectedModel, selectedEffort, state, log, store,
			raw, promptOptions, stdout, stderr)
	}
	selectedModel := stringPtr(selected.DefaultModel)
	if selectedModel != nil && *selectedModel == "" {
		selectedModel = nil
	}
	if selected.Definition.Hooks&registry.HookDiscover != 0 {
		var warning string
		selectedModel, warning, err = discoverProviderModel(selected, store, transport.DefaultClient, nil)
		if warning != "" {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return currentProvider, currentModel
		}
	}
	modelID := ""
	if selectedModel != nil {
		modelID = *selectedModel
	}
	startModelProbe(ctx, selected, modelID, transport.DefaultClient)
	for _, warning := range selected.Warnings() {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
	providerID := selected.Definition.ID
	exitPreset(store)
	store.SetOverride("provider", stringPtr(providerID))
	store.SetOverride("model", stringPtr(config.DefaultSentinel))
	store.SetOverride("effort", stringPtr(config.DefaultSentinel))
	state.ProviderID = stringPtr(providerID)
	state.Model = cloneString(selectedModel)
	state.ModelLabel = cloneString(selectedModel)
	state.Effort = nil
	if selectedModel != nil {
		state.Effort = selected.ResolveEffort(*selectedModel, nil)
	}
	modelName := ""
	if selectedModel != nil {
		modelName = *selectedModel
	}
	configureLivePrompt(state, store, modelName, raw, promptOptions, stderr)
	setSessionSelection(state, log, store)
	persistSelection(store, providerID, nil, nil, stderr)
	if err := announceSelection(stdout, stderr, selected, state, store, pickedWithPicker); err != nil {
		fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
	}
	return selected, cloneString(selectedModel)
}

func pickProviderModelAndEffort(ctx context.Context, provider *runtime.Provider, stdin io.Reader, stdinTTY bool,
	store *config.Store, stdout, stderr io.Writer) (*string, *string, bool) {
	busy := startBusyOperation(ctx, stdin, stdinTTY, stdout, "fetching models...")
	models, err := provider.ListModels(busy.Context(), transport.DefaultClient, busy.Tick)
	cancelled := endBusyOperation(busy, stderr)
	if cancelled {
		return nil, nil, false
	}
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return nil, nil, false
	}
	models = modelsForPicker(provider, models)
	if len(models) == 0 {
		writeNoteLine(stdout, fmt.Sprintf("%s can't list models — set one with GAX_MODEL or in config", terminalSafeText(provider.DisplayName)))
		return nil, nil, false
	}
	selectedModel := 0
	if len(models) > 1 && terminalinput.TTYPair(stdin, stdout, stdinTTY) {
		items := make([]terminalpicker.Item, len(models))
		for index, model := range models {
			items[index] = modelPickerItem(provider, model, false)
		}
		stdinFile, _ := stdin.(*os.File)
		index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
			Title: "select a model", Items: items,
			RepeatClippedLabel: true,
			DisplayColumns:     func(columns int) int { return editorDisplayColumns(store, columns) },
		}, stdinFile, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "gax: model picker: %v\n", err)
			return nil, nil, false
		}
		if index < 0 || index >= len(models) {
			return nil, nil, false
		}
		selectedModel = index
	}
	model := models[selectedModel].ID
	if !waitForBusyModelMetadata(ctx, provider, model, stdin, stdinTTY, stdout, stderr) {
		return nil, nil, false
	}
	levels := provider.EffortLevels(model)
	if len(levels) == 0 {
		return stringPtr(model), nil, true
	}
	items := make([]terminalpicker.Item, 0, len(levels)+1)
	items = append(items, terminalpicker.Item{
		Label: "default", Detail: "Let the provider choose",
		Description: "Let the provider choose the reasoning effort", Current: true,
	})
	for _, level := range levels {
		items = append(items, terminalpicker.Item{Label: terminalSafeText(level)})
	}
	stdinFile, _ := stdin.(*os.File)
	index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: "select reasoning effort", Items: items,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}, stdinFile, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: effort picker: %v\n", err)
		return nil, nil, false
	}
	if index < 0 || index >= len(items) {
		return nil, nil, false
	}
	if index == 0 {
		return stringPtr(model), provider.ResolveEffort(model, nil), true
	}
	effort := levels[index-1]
	return stringPtr(model), provider.ResolveEffort(model, &effort), true
}

func commitProviderSelection(selected *runtime.Provider, selectedModel, selectedEffort *string,
	state *agent.Session, log *session.Log, store *config.Store, raw bool, promptOptions agent.PromptOptions,
	stdout, stderr io.Writer) (*runtime.Provider, *string) {
	for _, warning := range selected.Warnings() {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
	providerID := selected.Definition.ID
	selected.ModelDiscovered = false
	exitPreset(store)
	store.SetOverride("provider", stringPtr(providerID))
	modelSetting := stringPtr(config.DefaultSentinel)
	if selectedModel != nil {
		modelSetting = selectedModel
	}
	store.SetOverride("model", modelSetting)
	effortSetting := stringPtr(config.DefaultSentinel)
	if selectedEffort != nil {
		effortSetting = selectedEffort
	}
	store.SetOverride("effort", effortSetting)
	state.ProviderID = stringPtr(providerID)
	state.Model = cloneString(selectedModel)
	state.ModelLabel = cloneString(selectedModel)
	state.Effort = cloneString(selectedEffort)
	modelName := ""
	if selectedModel != nil {
		modelName = *selectedModel
	}
	configureLivePrompt(state, store, modelName, raw, promptOptions, stderr)
	setSessionSelection(state, log, store)
	persistSelection(store, providerID, modelSetting, effortSetting, stderr)
	if err := announceSelection(stdout, stderr, selected, state, store, true); err != nil {
		fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
	}
	return selected, cloneString(selectedModel)
}

func runModelCommand(ctx context.Context, parsed slash.Parsed, provider *runtime.Provider, currentModel *string,
	state *agent.Session, log *session.Log, store *config.Store, stdin io.Reader, stdinTTY, raw bool,
	promptOptions agent.PromptOptions, stdout, stderr io.Writer) *string {
	if provider == nil {
		writeNoteLine(stdout, "no provider selected — use /provider to choose one first")
		return currentModel
	}
	if !parsed.HasArgument {
		if provider.Definition.Internal {
			writeNoteLine(stdout, fmt.Sprintf("%s can't list models — set one with GAX_MODEL or in config", terminalSafeText(provider.DisplayName)))
			return currentModel
		}
		busy := startBusyOperation(ctx, stdin, stdinTTY, stdout, "fetching models...")
		models, err := provider.ListModels(busy.Context(), transport.DefaultClient, busy.Tick)
		cancelled := endBusyOperation(busy, stderr)
		if cancelled {
			return currentModel
		}
		if err != nil {
			fmt.Fprintln(stdout, err)
			return currentModel
		}
		if len(models) == 0 {
			writeNoteLine(stdout, fmt.Sprintf("%s has no models available", terminalSafeText(provider.DisplayName)))
			return currentModel
		}
		if terminalinput.TTYPair(stdin, stdout, stdinTTY) {
			models = modelsForPicker(provider, models)
			if len(models) == 1 {
				effort, selected := pickEffortForModel(ctx, provider, models[0].ID, state.Effort,
					stdin, stdinTTY, store, stdout, stderr)
				if !selected {
					return currentModel
				}
				return applyModelSelection(ctx, models[0].ID, provider, state, log, store, currentModel,
					effort, true, true, raw, promptOptions, stdout, stderr)
			}
			items := make([]terminalpicker.Item, len(models))
			initialIndex := 0
			for index, model := range models {
				items[index] = modelPickerItem(provider, model, currentModel != nil && model.ID == *currentModel)
				if currentModel != nil && model.ID == *currentModel {
					initialIndex = index
				}
			}
			stdinFile, _ := stdin.(*os.File)
			index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
				Title: "select a model", Items: items, InitialIndex: initialIndex,
				RepeatClippedLabel: true,
				DisplayColumns:     func(columns int) int { return editorDisplayColumns(store, columns) },
			}, stdinFile, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "gax: model picker: %v\n", err)
				return currentModel
			}
			if index >= 0 && index < len(models) {
				effort, selected := pickEffortForModel(ctx, provider, models[index].ID, state.Effort,
					stdin, stdinTTY, store, stdout, stderr)
				if !selected {
					return currentModel
				}
				return applyModelSelection(ctx, models[index].ID, provider, state, log, store, currentModel,
					effort, true, true, raw, promptOptions, stdout, stderr)
			}
			return currentModel
		}
		if len(models) == 1 {
			return applyModelSelection(ctx, models[0].ID, provider, state, log, store, currentModel,
				nil, false, true, raw, promptOptions, stdout, stderr)
		}
		return currentModel
	}
	requested := strings.TrimSpace(parsed.Argument)
	if requested == "" {
		fmt.Fprintln(stderr, "gax: /model needs a model id")
		return currentModel
	}
	return applyModelSelection(ctx, requested, provider, state, log, store, currentModel,
		nil, false, false, raw, promptOptions, stdout, stderr)
}

func applyModelSelection(ctx context.Context, requested string, provider *runtime.Provider, state *agent.Session, log *session.Log,
	store *config.Store, currentModel, selectedEffort *string, effortChosen, bannerWhenEmpty, raw bool, promptOptions agent.PromptOptions,
	stdout, stderr io.Writer) *string {
	selected := stringPtr(requested)
	startModelProbe(ctx, provider, requested, transport.DefaultClient)
	provider.ModelDiscovered = false
	exitPreset(store)
	store.SetOverride("provider", stringPtr(provider.Definition.ID))
	store.SetOverride("model", selected)
	effort := provider.ResolveEffort(requested, state.Effort)
	if effortChosen {
		effort = provider.ResolveEffort(requested, selectedEffort)
	}
	persistedEffort := stringPtr(config.DefaultSentinel)
	if effort != nil {
		persistedEffort = effort
	}
	store.SetOverride("effort", persistedEffort)
	state.ProviderID = stringPtr(provider.Definition.ID)
	state.Model = selected
	state.ModelLabel = cloneString(selected)
	state.Effort = cloneString(effort)
	configureLivePrompt(state, store, requested, raw, promptOptions, stderr)
	setSessionSelection(state, log, store)
	persistSelection(store, provider.Definition.ID, selected, persistedEffort, stderr)
	if err := announceSelection(stdout, stderr, provider, state, store, bannerWhenEmpty); err != nil {
		fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
	}
	return selected
}

func pickEffortForModel(ctx context.Context, provider *runtime.Provider, model string, currentEffort *string,
	stdin io.Reader, stdinTTY bool, store *config.Store, stdout, stderr io.Writer) (*string, bool) {
	if !waitForBusyModelMetadata(ctx, provider, model, stdin, stdinTTY, stdout, stderr) {
		return nil, false
	}
	levels := provider.EffortLevels(model)
	if len(levels) == 0 {
		return nil, true
	}
	items := make([]terminalpicker.Item, 0, len(levels)+1)
	items = append(items, terminalpicker.Item{
		Label: "default", Detail: "Let the provider choose",
		Description: "Let the provider choose the reasoning effort",
		Current:     currentEffort == nil || *currentEffort == "",
	})
	initialIndex := 0
	for _, level := range levels {
		current := currentEffort != nil && *currentEffort == level
		if current {
			initialIndex = len(items)
		}
		items = append(items, terminalpicker.Item{Label: terminalSafeText(level), Current: current})
	}
	stdinFile, _ := stdin.(*os.File)
	index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: "select reasoning effort", Items: items, InitialIndex: initialIndex,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}, stdinFile, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: effort picker: %v\n", err)
		return nil, false
	}
	if index < 0 || index >= len(items) {
		return nil, false
	}
	if index == 0 {
		return provider.ResolveEffort(model, nil), true
	}
	effort := levels[index-1]
	return provider.ResolveEffort(model, &effort), true
}

func runEffortCommand(ctx context.Context, parsed slash.Parsed, provider *runtime.Provider, model *string, state *agent.Session,
	log *session.Log, store *config.Store, stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) {
	if provider == nil || provider.Definition == nil {
		writeNoteLine(stdout, "no provider selected — use /provider to choose one first")
		return
	}
	modelID := ""
	if model != nil {
		modelID = *model
	}
	needsMetadata := !parsed.HasArgument || strings.TrimSpace(parsed.Argument) != "default"
	if needsMetadata && modelID != "" && !waitForBusyModelMetadata(ctx, provider, modelID, stdin, stdinTTY, stdout, stderr) {
		return
	}
	levels := provider.EffortLevels(modelID)
	selectedLevel := ""
	if parsed.HasArgument {
		selectedLevel = strings.TrimSpace(parsed.Argument)
		if selectedLevel == "" {
			fmt.Fprintln(stderr, "gax: /effort needs a level or no argument")
			return
		}
		if selectedLevel != "default" && !containsEffort(levels, selectedLevel) {
			if len(levels) == 0 {
				writeNoEffortLevels(stdout, provider, modelID)
				return
			}
			subject := "this model"
			if modelID != "" {
				subject = terminalSafeText(modelID)
			}
			fmt.Fprintf(stdout, "%s doesn't take effort '%s' — expected %s, or default\n",
				subject, terminalSafeText(selectedLevel), strings.Join(levels, ", "))
			return
		}
	} else {
		if len(levels) == 0 {
			writeNoEffortLevels(stdout, provider, modelID)
			return
		}
		items := make([]terminalpicker.Item, 0, len(levels)+1)
		items = append(items, terminalpicker.Item{
			Label: "default", Detail: "Let the provider choose", Description: "Let the provider choose the reasoning effort",
			Current: state.Effort == nil || *state.Effort == "",
		})
		initialIndex := 0
		for _, level := range levels {
			current := state.Effort != nil && *state.Effort == level
			if current {
				initialIndex = len(items)
			}
			items = append(items, terminalpicker.Item{Label: level, Current: current})
		}
		stdinFile, _ := stdin.(*os.File)
		index, _, err := terminalpicker.RunTTY(terminalpicker.Options{
			Title: "select reasoning effort", Items: items, InitialIndex: initialIndex,
			DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
		}, stdinFile, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "gax: effort picker: %v\n", err)
			return
		}
		if index < 0 || index >= len(items) {
			return
		}
		if index > 0 {
			selectedLevel = items[index].Label
		}
	}

	var persistedModel *string
	if currentPreset(store) != nil && state.Model != nil {
		persistedModel = cloneString(state.Model)
	}
	exitPreset(store)
	store.SetOverride("provider", stringPtr(provider.Definition.ID))
	if persistedModel != nil {
		store.SetOverride("model", persistedModel)
	}
	persistedEffort := stringPtr(config.DefaultSentinel)
	if selectedLevel == "" || selectedLevel == "default" {
		store.SetOverride("effort", persistedEffort)
	} else {
		persistedEffort = stringPtr(selectedLevel)
		store.SetOverride("effort", persistedEffort)
	}
	if selectedLevel == "" || selectedLevel == "default" {
		state.Effort = provider.ResolveEffort(modelID, nil)
	} else {
		state.Effort = provider.ResolveEffort(modelID, &selectedLevel)
	}
	setSessionSelection(state, log, store)
	persistSelection(store, provider.Definition.ID, persistedModel, persistedEffort, stderr)
	if err := announceSelection(stdout, stderr, provider, state, store, !parsed.HasArgument); err != nil {
		fmt.Fprintf(stderr, "gax: write identity banner: %v\n", err)
	}
}

func exitPreset(store *config.Store) {
	store.SetOverride("preset", stringPtr(""))
	store.SetOverride("system_prompt", nil)
	store.SetOverride("system_prompt_append", nil)
	store.SetConversation("preset", nil)
	store.SetConversation("system_prompt", nil)
	store.SetConversation("system_prompt_append", nil)
}

func configureLivePrompt(state *agent.Session, store *config.Store, model string, raw bool,
	options agent.PromptOptions, stderr io.Writer) {
	result := agent.ConfigureSession(state, store, model, raw, options)
	for _, warning := range result.Warnings {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
}

func persistSelection(store *config.Store, provider string, model, effort *string, stderr io.Writer) {
	if err := store.PersistSelection(provider, model, effort); err != nil {
		writeWarningDiagnostic(stderr, "couldn't save to state.json — this choice applies to this run only: %v", err)
	}
}

func containsEffort(levels []string, level string) bool {
	for _, candidate := range levels {
		if candidate == level {
			return true
		}
	}
	return false
}

func writeNoEffortLevels(stdout io.Writer, provider *runtime.Provider, model string) {
	if !provider.SupportsEffortLevels() {
		writeNoteLine(stdout, fmt.Sprintf("the %s provider doesn't expose reasoning-effort levels", terminalSafeText(provider.DisplayName)))
		return
	}
	if model == "" {
		model = "this model"
	}
	writeNoteLine(stdout, fmt.Sprintf("%s doesn't take reasoning-effort levels", terminalSafeText(model)))
}

func setSessionSelection(state *agent.Session, log *session.Log, store *config.Store) {
	if log == nil || state == nil {
		return
	}
	log.SetSelection(session.Selection{
		Provider: state.ProviderID, Model: state.Model, ModelLabel: state.ModelLabel,
		Effort: state.Effort, Preset: currentPreset(store),
	})
}

// appendTaskExitNote records task bookkeeping without committing a selection staged for the next
// conversation into the session being left.
func appendTaskExitNote(state *agent.Session, log *session.Log, note string) error {
	if log != nil {
		log.DiscardSelection()
	}
	if note == "" || state == nil {
		return nil
	}
	state.Items = append(state.Items, conversation.Item{
		Kind: conversation.KindUser, Text: conversation.String(note), Origin: conversation.OriginTaskNote,
	})
	if log != nil {
		return log.Append(state.Items)
	}
	return nil
}

func terminalSafeText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
}

func runProviderUsage(ctx context.Context, provider *runtime.Provider, httpClient *transport.Client, displayWidth int,
	stdin io.Reader, stdinTTY bool, stdout, stderr io.Writer) {
	if provider == nil || provider.Definition == nil {
		writeNoteLine(stdout, "no provider selected — use /provider to choose one first")
		return
	}
	if provider.Definition.Hooks&registry.HookQueryUsage == 0 {
		writeNoteLine(stdout, fmt.Sprintf("/usage is not supported by the %s provider", provider.DisplayName))
		return
	}
	busy := startBusyOperation(ctx, stdin, stdinTTY, stdout, "fetching usage...")
	switch provider.Definition.ID {
	case "codex":
		if !provider.WaitModelProbe(runtime.ModelMetadataWait, busy.Tick) {
			endBusyOperation(busy, stderr)
			return
		}
		auth := provider.CodexAuth()
		report, err := codex.QueryUsage(busy.Context(), auth, provider.ConfiguredHeaders(""), httpClient, busy.Tick)
		cancelled := endBusyOperation(busy, stderr)
		if cancelled {
			return
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return
		}
		for _, warning := range report.Warnings {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		codex.WriteUsageReport(stdout, report, displayWidth)
	case "deepseek":
		report, err := deepseek.QueryUsage(busy.Context(), provider.BaseURL, provider.MetadataHeaders(""),
			provider.HasAPIKey(), httpClient, busy.Tick)
		cancelled := endBusyOperation(busy, stderr)
		if cancelled {
			return
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return
		}
		deepseek.WriteUsageReport(stdout, provider.DisplayName, report)
	case "openrouter":
		report, err := openrouter.QueryUsage(busy.Context(), provider.BaseURL, provider.MetadataHeaders(""),
			provider.HasAPIKey(), httpClient, busy.Tick)
		cancelled := endBusyOperation(busy, stderr)
		if cancelled {
			return
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return
		}
		openrouter.WriteUsageReport(stdout, report)
	case "opencode-go":
		// OpenCode Go's usage endpoint requires Bearer auth even when /models is configured to
		// use the Anthropic Messages dialect.
		report, err := opencodego.QueryUsage(busy.Context(), provider.BaseURL,
			provider.Headers(runtime.WireChat, "", false), provider.HasAPIKey(), httpClient, busy.Tick)
		cancelled := endBusyOperation(busy, stderr)
		if cancelled {
			return
		}
		if err != nil {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			return
		}
		opencodego.WriteUsageReport(stdout, provider.DisplayName, report, displayWidth)
	default:
		endBusyOperation(busy, stderr)
		writeNoteLine(stdout, fmt.Sprintf("/usage is not supported by the %s provider", provider.DisplayName))
	}
}

func loadResume(options cli.Options, store *config.Store) (*session.Loaded, string, error) {
	if options.ResumeMode == cli.ResumeNone {
		return nil, "", nil
	}
	if options.ResumeMode == cli.ResumeSelect && options.ResumeID == nil {
		return nil, "", errors.New("--resume needs an id in this build (use --resume=ID)")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	entries, err := session.List(cwd, store.Int("session_retention_days"))
	if err != nil {
		return nil, "", fmt.Errorf("list conversations: %w", err)
	}
	var path string
	if options.ResumeMode == cli.ResumeLatest {
		if len(entries) > 0 {
			path = entries[0].Path
		}
		if path == "" && options.OneShot {
			return nil, "", errors.New("no past conversation in this directory to continue")
		}
	} else if options.ResumeID != nil {
		for _, entry := range entries {
			if entry.ID != nil && *entry.ID == *options.ResumeID {
				path = entry.Path
				break
			}
		}
		if path == "" {
			matches := 0
			for _, entry := range entries {
				if entry.ID != nil && strings.HasPrefix(*entry.ID, *options.ResumeID) {
					matches++
					path = entry.Path
				}
			}
			if matches != 1 {
				path = ""
			}
		}
	}
	if path == "" {
		if options.ResumeMode == cli.ResumeLatest && !options.OneShot {
			return nil, "", nil
		}
		if options.ResumeID != nil && strings.Contains(*options.ResumeID, "/") {
			return nil, "", errors.New("--resume takes a session id, not a path (sessions are per-directory)")
		}
		if options.ResumeID != nil {
			return nil, "", fmt.Errorf("no session matching '%s'", *options.ResumeID)
		}
		return nil, "", errors.New("conversation not found in this directory")
	}
	return loadResumePath(path, options, store)
}

func loadResumePath(path string, options cli.Options, store *config.Store) (*session.Loaded, string, error) {
	_ = session.Touch(path)
	loaded, err := session.LoadAll(path)
	if err != nil {
		return nil, "", fmt.Errorf("load conversation: %w", err)
	}
	store.SetConversation("provider", loaded.Meta.Provider)
	store.SetConversation("model", loaded.Meta.Model)
	store.SetConversation("effort", loaded.Meta.Effort)
	store.SetConversation("preset", stringPtr(""))
	store.SetConversation("system_prompt", nil)
	store.SetConversation("system_prompt_append", nil)
	if !runReplacesResumedPreset(options, store) && loaded.Meta.Preset != nil && *loaded.Meta.Preset != "" {
		if err := applyPreset(store, *loaded.Meta.Preset, configTierConversation); err != nil {
			message := err.Error()
			if options.OneShot {
				return nil, "", fmt.Errorf("%s (recorded by this session; pass --preset or --model to run it differently)", message)
			}
			return &loaded, path, &resumePresetWarning{message: message + " — resuming without it"}
		}
	}
	return &loaded, path, nil
}

type resumePresetWarning struct {
	message string
}

func (warning *resumePresetWarning) Error() string { return warning.message }

func loadStore(stderr io.Writer) (*config.Store, error) {
	store := config.New(config.DefaultSettings())
	if path, ok := systempath.XDGHaxPath(os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME"), ".config", "config.json"); ok {
		data, truncated, err := systemfs.ReadFileCapped(path, config.MaxTierFileBytes)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				store.RejectConfigFile()
				writeWarningDiagnostic(stderr, "ignoring unreadable config at %s: %v", path, err)
			}
		} else if truncated {
			store.RejectConfigFile()
			writeWarningDiagnostic(stderr, "ignoring config at %s: larger than the 1 MiB limit", path)
		} else if err := store.LoadConfig(data); err != nil {
			writeWarningDiagnostic(stderr, "ignoring malformed config at %s (expected a JSON object)", path)
		}
	}
	if path, ok := systempath.XDGHaxPath(os.Getenv("XDG_STATE_HOME"), os.Getenv("HOME"), ".local/state", "state.json"); ok {
		data, truncated, err := systemfs.ReadFileCapped(path, config.MaxTierFileBytes)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				writeWarningDiagnostic(stderr, "ignoring unreadable state at %s: %v", path, err)
			}
		} else if truncated {
			writeWarningDiagnostic(stderr, "ignoring state at %s: larger than the 1 MiB limit", path)
		} else if err := store.LoadState(data); err != nil {
			writeWarningDiagnostic(stderr, "ignoring malformed state at %s (expected a JSON object)", path)
		}
	}
	return store, nil
}

func applySelection(store *config.Store, options cli.Options, warn func(string)) error {
	preset := options.Selection.Preset
	namedForRun := preset != nil
	if preset == nil {
		if envPreset, exists := store.LookupEnv("GAX_PRESET"); exists {
			if envPreset != "" {
				preset = stringPtr(envPreset)
				namedForRun = true
			}
		} else if store.Source("preset") != "conversation" {
			preset = store.StringNonempty("preset")
			if preset != nil && *preset != "" && hasPerSettingSelection(store, options) {
				store.SetOverride("preset", stringPtr(""))
				preset = nil
			}
		}
	}
	if preset != nil && *preset != "" {
		if err := applyPreset(store, *preset, configTierRun); err != nil {
			if namedForRun {
				return err
			}
			if warn != nil {
				warn(fmt.Sprintf("%v — starting without it", err))
			}
			store.SetOverride("preset", stringPtr(""))
		}
	}
	for key, value := range map[string]*string{
		"provider": options.Selection.Provider,
		"model":    options.Selection.Model,
		"effort":   options.Selection.Effort,
	} {
		if value != nil {
			store.SetOverride(key, value)
		}
	}
	return nil
}

type configTier uint8

const (
	configTierRun configTier = iota
	configTierConversation
)

func presetScalar(raw json.RawMessage) (string, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return strconv.FormatInt(integer, 10), true
		}
		value, err := typed.Float64()
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(value, 'g', 6, 64), true
	case bool:
		if typed {
			return "1", true
		}
		return "0", true
	default:
		return "", false
	}
}

func hasPerSettingSelection(store *config.Store, options cli.Options) bool {
	if options.Selection.Provider != nil || options.Selection.Model != nil || options.Selection.Effort != nil {
		return true
	}
	for _, name := range []string{"GAX_PROVIDER", "GAX_MODEL", "GAX_EFFORT", "GAX_SYSTEM_PROMPT"} {
		if _, exists := store.LookupEnv(name); exists {
			return true
		}
	}
	return false
}

func runReplacesResumedPreset(options cli.Options, store *config.Store) bool {
	if options.Selection.Provider != nil || options.Selection.Model != nil || options.Selection.Effort != nil || options.Selection.Preset != nil {
		return true
	}
	value, exists := store.LookupEnv("GAX_PRESET")
	return exists && value != ""
}

func selectProvider(store *config.Store, providers *registry.Registry, resolver *runtime.Resolver) (*runtime.Provider, error) {
	if requested := store.StringNonempty("provider"); requested != nil {
		definition, found := providers.Find(*requested)
		if !found {
			definitions := providers.All()
			names := make([]string, 0, len(definitions))
			for _, candidate := range definitions {
				names = append(names, candidate.ID)
			}
			return nil, fmt.Errorf("unknown provider '%s' (supported: %s)", *requested, strings.Join(names, " "))
		}
		provider, err := resolver.Resolve(*requested)
		if err != nil {
			return nil, err
		}
		switch provider.Availability.State {
		case runtime.AvailabilityUnconfigured:
			return nil, unconfiguredProviderDiagnostic(definition)
		case runtime.AvailabilityMissingCredential:
			if definition.Hooks&registry.HookAuthSource != 0 {
				return nil, errors.New(provider.Availability.Reason)
			}
		}
		return provider, nil
	}
	type candidate struct {
		id       string
		provider *runtime.Provider
	}
	definitions := providers.All()
	candidates := make([]candidate, 0, len(definitions))
	for index, definition := range definitions {
		provider, err := resolver.Resolve(definition.ID)
		if err != nil {
			continue
		}
		if index == 0 && provider.Availability.Ready() {
			store.SetOverride("provider", stringPtr(definition.ID))
			return provider, nil
		}
		candidates = append(candidates, candidate{id: definition.ID, provider: provider})
	}
	var probes sync.WaitGroup
	for _, candidate := range candidates {
		if candidate.provider.Availability.State != runtime.AvailabilityNeedsProbe {
			continue
		}
		probes.Add(1)
		go func(provider *runtime.Provider) {
			defer probes.Done()
			provider.ProbeAvailability(context.Background(), transport.DefaultClient)
		}(candidate.provider)
	}
	probes.Wait()
	for _, candidate := range candidates {
		if candidate.provider.Availability.Ready() {
			store.SetOverride("provider", stringPtr(candidate.id))
			return candidate.provider, nil
		}
	}
	return nil, errors.New("no provider available (set GAX_PROVIDER or configure one)")
}

func unconfiguredProviderDiagnostic(definition *registry.Definition) error {
	key := "providers." + definition.ID + ".base_url"
	for _, setting := range config.DefaultSettings() {
		if setting.Key == key && setting.EnvVar != "" {
			return fmt.Errorf("provider '%s': no base_url (set %s, or %s in config.json)", definition.ID, setting.EnvVar, key)
		}
	}
	return fmt.Errorf("provider '%s': no base_url (set %s in config.json)", definition.ID, key)
}

func discoverProviderModel(provider *runtime.Provider, store *config.Store, client *transport.Client,
	configured *string) (*string, string, error) {
	if provider == nil || provider.Definition == nil || provider.Definition.Hooks&registry.HookDiscover == 0 {
		return configured, "", nil
	}
	configuredModel := ""
	if configured != nil {
		configuredModel = *configured
	}
	model, discovered, warning, err := provider.DiscoverModel(context.Background(), client, configuredModel)
	if err != nil {
		return configured, "", modelDiscoveryError(provider, err)
	}
	provider.ModelDiscovered = discovered
	store.SetOverride("model", stringPtr(config.DefaultSentinel))
	if model == "" {
		return nil, warning, nil
	}
	store.SetOverride("model", stringPtr(model))
	return stringPtr(model), warning, nil
}

func modelDiscoveryError(provider *runtime.Provider, err error) error {
	if provider != nil && provider.Definition != nil && provider.Definition.ID == "llamacpp" {
		return fmt.Errorf("llama.cpp: failed to auto-discover model from %s/models\ngax: is llama-server running? (set GAX_MODEL to skip probing, or adjust GAX_LLAMACPP_PORT / GAX_LLAMACPP_BASE_URL)", provider.BaseURL)
	}
	return err
}

func recordingEnabled(store *config.Store, provider *runtime.Provider, disabled bool) bool {
	if disabled {
		return false
	}
	defaultDisabled := provider != nil && provider.Definition != nil && provider.Definition.Internal
	return !store.BoolOr("no_session", defaultDisabled)
}

func currentPreset(store *config.Store) *string {
	preset := store.String("preset")
	if preset == nil || *preset == "" {
		return nil
	}
	return preset
}

func runREPL(ctx context.Context, stdin io.Reader, inputTTY, persistPromptHistory bool, stdout, stderr io.Writer,
	store *config.Store, tools *coretools.Registry, state *agent.Session, run func(string) replOutput, pendingInput *string,
	handleCommand func(slash.Parsed) bool) int {
	if terminalinput.TTYPair(stdin, stdout, inputTTY) {
		return runTTYREPL(ctx, stdin.(*os.File), persistPromptHistory, stdout, stderr, store, tools, state, run, pendingInput, handleCommand)
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	type scanResult struct {
		text string
		err  error
		ok   bool
	}
	lines := make(chan scanResult)
	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanResult{text: scanner.Text(), ok: true}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- scanResult{err: scanner.Err()}:
		case <-ctx.Done():
		}
	}()
	for {
		if display, ok := stdout.(*lineOutputWriter); ok {
			display.BlockSeparator()
		}
		if inputTTY {
			fmt.Fprint(stdout, "> ")
		}
		select {
		case <-ctx.Done():
			return 130
		case line := <-lines:
			if !line.ok {
				if line.err != nil {
					fmt.Fprintf(stderr, "gax: read prompt: %v\n", line.err)
					return 1
				}
				fmt.Fprintln(stdout)
				return 0
			}
			text := line.text
			if strings.TrimSpace(text) == "" && (state == nil || state.ResumeReason == agent.ResumeReasonNone) {
				continue
			}
			if code, done := processREPLLine(text, stdout, stderr, store, run, handleCommand); done {
				return code
			}
		}
	}
}

func ttyPrompt(haveUTF8 bool) string {
	if !haveUTF8 {
		return "\x1b[1m>\x1b[22m "
	}
	return terminaltheme.Open(terminaltheme.Accent) + "\x1b[1m❯\x1b[22m" + terminaltheme.Close(terminaltheme.Accent) + " "
}

func runTTYREPL(ctx context.Context, stdin *os.File, persistPromptHistory bool, stdout, stderr io.Writer,
	store *config.Store, tools *coretools.Registry, state *agent.Session, run func(string) replOutput, pendingInput *string,
	handleCommand func(slash.Parsed) bool) int {
	core := terminalinput.NewCore()
	cwd, _ := os.Getwd()
	historyPath, _ := session.PromptHistoryPath(cwd)
	history := terminalinput.OpenHistory(core, historyPath, persistPromptHistory)
	editorOptions := terminalinput.EditorOptions{
		ModalKeys: map[byte]func(){
			0x0f: historyModalKey(state, func() bool { return store.Bool("show_reasoning") }, tools, store, stdout, stderr), // Ctrl-O
			0x14: transcriptModalKey(state, stdout, stderr),                                                                 // Ctrl-T
		},
		Completers: []terminalinput.Completer{{
			Match:    slash.MatchName,
			Complete: slash.CompleteName,
			Candidates: func(token string) (string, bool) {
				if token == "" {
					return "  see /help", true
				}
				candidates, ok := slash.NameCandidates(token)
				if !ok {
					return "", false
				}
				return "  " + candidates, true
			},
		}, {
			Match: filemention.Match,
			Complete: func(token string) (string, bool) {
				if !filemention.Available() {
					writeWarningDiagnostic(stderr, "@file completion needs fzf installed")
					return "", false
				}
				query := strings.TrimPrefix(token, "@")
				path, ok, err := filemention.Pick(query)
				if err != nil {
					writeWarningDiagnostic(stderr, "%v", err)
					return "", false
				}
				return path, ok
			},
			Modal: true,
		}},
		EditBuffer: func(buffer string) (string, bool) {
			text, ok, err := terminaledit.EditBuffer(buffer, stdin, stdout, stderr)
			if err != nil {
				writeWarningDiagnostic(stderr, "%v", err)
				return "", false
			}
			return text, ok
		},
		Hint: func(buffer string) (string, bool) {
			if buffer == "" && state != nil {
				if hint := state.ResumeReason.Placeholder(); hint != "" {
					return hint, true
				}
			}
			return slash.Hint(buffer)
		},
		DisplayColumns: func(terminalColumns int) int { return editorDisplayColumns(store, terminalColumns) },
		PasteHook:      func() (string, bool) { return terminalpaste.CaptureClipboard(ctx) },
		PasteFilter:    terminalpaste.FileURIsToPaths,
	}
	for {
		if ctx.Err() != nil {
			return 130
		}
		editorOptions.InitialText = ""
		editorOptions.EmptySubmit = state != nil && state.ResumeReason != agent.ResumeReasonNone
		if pendingInput != nil {
			editorOptions.InitialText = *pendingInput
			*pendingInput = ""
		}
		prompt := ttyPrompt(terminalocale.HaveUTF8())
		text, submitted, err := terminalinput.ReadTTYLine(ctx, stdin, stdout, core, history, prompt, editorOptions)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return 130
			}
			fmt.Fprintf(stderr, "gax: read prompt: %v\n", err)
			return 1
		}
		if !submitted {
			if ctx.Err() != nil {
				return 130
			}
			return 0
		}
		if strings.TrimSpace(text) == "" && (state == nil || state.ResumeReason == agent.ResumeReasonNone) {
			continue
		}
		if text != "" && slash.Parse(text).Outcome == slash.NotCommand {
			history.Add(core, text)
		}
		isCommand := slash.Parse(text).Outcome != slash.NotCommand
		if isCommand {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return 1
			}
		}
		code, done := processREPLLine(text, stdout, stderr, store, run, handleCommand)
		if isCommand && !done {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return 1
			}
		}
		if done {
			return code
		}
	}
}

func editorDisplayColumns(store *config.Store, terminalColumns int) int {
	mode := "auto"
	configured := 0
	if store != nil {
		if value := store.String("display_width"); value != nil {
			mode = *value
		}
		configured = store.Int("display_width")
	}
	return terminalwidth.ContentColumns(terminalColumns, mode, configured)
}

func markdownDisplayColumns(store *config.Store, terminalColumns int) int {
	columns := editorDisplayColumns(store, terminalColumns)
	if edge := terminalColumns - 1; edge > 0 && columns > edge {
		columns = edge
	}
	return columns
}

// processREPLLine returns done=true when the REPL should exit. Commands and empty lines leave the
// loop running; provider failures and writer failures terminate it with their corresponding status.
func processREPLLine(text string, stdout, stderr io.Writer, store *config.Store,
	run func(string) replOutput, handleCommand func(slash.Parsed) bool) (int, bool) {
	parsed := slash.Parse(text)
	if text != "" {
		if display, ok := stdout.(*lineOutputWriter); ok {
			display.SyncExternalLine()
			display.BlockSeparator()
		}
	}
	if parsed.Outcome != slash.NotCommand {
		if handleCommand != nil {
			handleCommand(parsed)
		}
		return 0, false
	}
	output := run(text)
	result, answer := output.result, output.answer
	notify := func() {
		if result.Turns > 0 {
			emitAttentionNotification(stdout, store)
		}
	}
	writeOutput := func() bool {
		if err := writeAssistantAnswer(stdout, answer, store); err != nil {
			fmt.Fprintf(stderr, "gax: write output: %v\n", err)
			return false
		}
		if output.afterAnswer != nil {
			if err := output.afterAnswer(stdout); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return false
			}
		}
		return true
	}
	switch result.Outcome {
	case agent.LoopComplete:
	case agent.LoopPaused:
		if !writeOutput() {
			return 1, true
		}
		return 0, false
	case agent.LoopMaxTurns:
		if !writeOutput() {
			return 1, true
		}
		notify()
		return 0, false
	case agent.LoopProviderError:
		if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "gax: %s\n", result.ErrorMessage)
		}
		notify()
		return 0, false
	case agent.LoopInterrupted:
		if !writeOutput() {
			return 1, true
		}
		return 0, false
	default:
		if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "gax: %s\n", result.ErrorMessage)
		}
		notify()
		return 1, true
	}
	if !writeOutput() {
		return 1, true
	}
	notify()
	return 0, false
}

func emitAttentionNotification(output io.Writer, store *config.Store) {
	configured := "auto"
	if store != nil {
		if value := store.String("notify"); value != nil {
			configured = *value
		}
	}
	_ = terminalnotify.Attention(output, terminalnotify.IsTerminalWriter(output), configured,
		terminalnotify.CurrentEnvironment())
}

func handleSlash(parsed slash.Parsed, stdout, stderr io.Writer, store *config.Store, startNew func() error) bool {
	switch parsed.Outcome {
	case slash.NotCommand:
		return false
	case slash.Unknown:
		fmt.Fprintf(stdout, "unknown command: /%s. type /help for the list.\n", parsed.Name)
	case slash.BadUsage:
		fmt.Fprintf(stdout, "/%s takes no arguments.\n", parsed.Name)
	case slash.Handled:
		if parsed.Command == nil {
			return true
		}
		switch parsed.Command.Name {
		case "help":
			printSlashHelp(stdout, appDisplayWidth(store))
		case "new":
			if parsed.HasArgument {
				fmt.Fprintf(stderr, "gax: /%s with a preset is not available in the line-oriented interface yet.\n", parsed.Name)
			} else if startNew != nil {
				if err := startNew(); err != nil {
					fmt.Fprintf(stderr, "gax: start new conversation: %v\n", err)
				}
			}
		default:
			fmt.Fprintf(stderr, "gax: /%s is not available in the line-oriented interface yet.\n", parsed.Name)
		}
	}
	return true
}

func handleUndo(parsed slash.Parsed, state *agent.Session, log *session.Log, stdout, stderr io.Writer,
	tools *coretools.Registry, inputTTY bool) {
	turns := typedUserTurnCount(state.Items)
	if !parsed.HasArgument {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to undo yet")
		} else if inputTTY {
			writeHistoryNoteLine(stdout, "/undo needs a turn count in the line-oriented interface")
		} else {
			writeHistoryNoteLine(stdout, "/undo needs a number of turns when not interactive")
		}
		return
	}
	turnsBack, err := strconv.ParseInt(strings.TrimSpace(parsed.Argument), 10, 64)
	if err != nil || turnsBack < 1 || turnsBack > int64(turns) {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to undo yet")
		} else {
			fmt.Fprintf(stdout, "/undo takes a number of turns between 1 and %d\n", turns)
		}
		return
	}
	keep := turns - int(turnsBack)
	cut := conversation.UserTurnCut(state.Items, keep)
	if err := log.Undo(keep, cut); err != nil {
		fmt.Fprintln(stderr, "gax: could not record the undo in the session file; conversation left unchanged")
		return
	}
	state.Retire(cut)
	unit := "user turns"
	if turnsBack == 1 {
		unit = "user turn"
	}
	if inputTTY {
		_ = replayRecentTurn(stdout, state, fmt.Sprintf("undid %d %s", turnsBack, unit), nil, nil, tools, true)
	} else {
		fmt.Fprintf(stdout, "undid %d %s\n", turnsBack, unit)
	}
}

func typedUserTurnCount(items []conversation.Item) int {
	count := 0
	for _, item := range items {
		if conversation.IsTypedPrompt(item) {
			count++
		}
	}
	return count
}

func handleFork(parsed slash.Parsed, state *agent.Session, log *session.Log, selection session.Selection,
	stdout, stderr io.Writer, tools *coretools.Registry, inputTTY bool) (*session.Log, error) {
	turns := typedUserTurnCount(state.Items)
	if !parsed.HasArgument {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to fork yet")
		} else if inputTTY {
			writeHistoryNoteLine(stdout, "/fork needs a turn count in the line-oriented interface")
		} else {
			writeHistoryNoteLine(stdout, "/fork needs a number of turns when not interactive")
		}
		return nil, nil
	}
	turnsBack, err := strconv.ParseInt(strings.TrimSpace(parsed.Argument), 10, 64)
	if err != nil || turnsBack < 0 || turnsBack > int64(turns) || turnsBack == 0 && len(state.Items) == 0 {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to fork yet")
		} else {
			fmt.Fprintf(stdout, "/fork takes a number of turns between 0 and %d\n", turns)
		}
		return nil, nil
	}
	if log == nil || !log.Materialized() {
		fmt.Fprintln(stderr, "gax: /fork needs session recording (it is disabled or unavailable)")
		return nil, nil
	}
	cut := len(state.Items)
	if turnsBack > 0 {
		keep := turns - int(turnsBack)
		cut = conversation.UserTurnCut(state.Items, keep)
	}
	forkPath, err := session.ForkFile(log.Path(), state.Items[:cut], selection)
	if err != nil {
		fmt.Fprintf(stderr, "gax: could not create fork: %v\n", err)
		return nil, nil
	}
	forked, err := session.ResumeLog(forkPath, selection, cut)
	if err != nil {
		_ = os.Remove(forkPath)
		fmt.Fprintf(stderr, "gax: could not open the fork session file: %v\n", err)
		return nil, nil
	}
	if err := log.Close(); err != nil {
		writeWarningDiagnostic(stderr, "could not close the source session file: %v", err)
	}
	for index := 0; index < cut; index++ {
		state.Items[index].Inherited = true
	}
	clear(state.Items[cut:])
	state.Items = state.Items[:cut]
	clear(state.Retired)
	state.Retired = state.Retired[:0]
	state.WorkedMS = 0
	state.LastUserTurnMS = -1
	state.CompactionDeferred = false
	state.ResumeReason = agent.ResumeReasonNone
	if inputTTY {
		_ = replayRecentTurn(stdout, state, "forked", nil, nil, tools, true)
	} else {
		fmt.Fprintln(stdout, "forked conversation")
	}
	return forked, nil
}

func writeHistoryNoteLine(writer io.Writer, text string) {
	writeNoteLine(writer, text)
}

func printSlashHelp(writer io.Writer, columns int) {
	printSlashHelpWithStyle(writer, columns, true)
}

func printSlashHelpWithStyle(writer io.Writer, columns int, styled bool) {
	headingOpen, headingClose := "", ""
	if styled {
		headingOpen, headingClose = "\x1b[1m", "\x1b[0m"
	}
	fmt.Fprintf(writer, "%scommands%s\n", headingOpen, headingClose)
	commands := slash.Commands()
	shortcuts := []struct {
		key, description string
		available        bool
		note             string
	}{
		{"enter", "submit prompt", true, ""},
		{"shift-enter", "insert newline (terminal must be configured to send LF)", true, ""},
		{"esc", "pause after the current step to steer the model", true, ""},
		{"esc esc", "interrupt model or running tool immediately", true, ""},
		{"ctrl-c", "cancel current prompt line", true, ""},
		{"ctrl-d", "quit (on empty prompt)", true, ""},
		{"ctrl-l", "clear screen and redraw prompt", true, ""},
		{"ctrl-g", "edit prompt in $EDITOR", true, ""},
		{"ctrl-o", "view the conversation in $PAGER", true, ""},
		{"ctrl-t", "view model-facing transcript in $PAGER", true, ""},
		{"ctrl-v", "paste image (or text) from clipboard", true, ""},
		{"@ + tab", "pick a project file to mention", filemention.Available(), "(fzf not installed)"},
	}
	labelWidth := 0
	for _, command := range commands {
		if cells := 1 + terminalwidth.Cells(command.Name); cells > labelWidth {
			labelWidth = cells
		}
		if cells := 1 + terminalwidth.Cells(command.Alias); cells > labelWidth {
			labelWidth = cells
		}
	}
	for _, shortcut := range shortcuts {
		if cells := terminalwidth.Cells(shortcut.key); cells > labelWidth {
			labelWidth = cells
		}
	}
	textColumn := labelWidth + 4
	for _, command := range commands {
		summary := command.Summary
		if command.Usage != "" {
			summary += " " + command.Usage
		}
		labelOpen := ""
		if styled {
			labelOpen = terminaltheme.Open(terminaltheme.Chrome)
		}
		writeSlashHelpRow(writer, "/"+command.Name, summary, labelWidth, textColumn, columns, labelOpen, false, styled)
		if command.Alias != "" {
			labelOpen := ""
			if styled {
				labelOpen = terminaltheme.Open(terminaltheme.ChromeDim)
			}
			writeSlashHelpRow(writer, "/"+command.Alias, "alias for /"+command.Name, labelWidth, textColumn, columns, labelOpen, true, styled)
		}
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%sshortcuts%s\n", headingOpen, headingClose)
	for _, shortcut := range shortcuts {
		description := shortcut.description
		if !shortcut.available && shortcut.note != "" {
			description += " " + shortcut.note
		}
		labelOpen := ""
		if styled {
			role := terminaltheme.Chrome
			if !shortcut.available {
				role = terminaltheme.ChromeDim
			}
			labelOpen = terminaltheme.Open(role)
		}
		writeSlashHelpRow(writer, shortcut.key, description, labelWidth, textColumn, columns, labelOpen, !shortcut.available, styled)
	}
}

func writeSlashHelpRow(writer io.Writer, label, summary string, labelWidth, textColumn, columns int, labelOpen string, dimmed, styled bool) {
	fmt.Fprint(writer, "  ")
	if labelOpen != "" {
		fmt.Fprint(writer, labelOpen)
	}
	fmt.Fprint(writer, label)
	if labelOpen != "" {
		fmt.Fprint(writer, "\x1b[0m")
	}
	if columns-textColumn >= 20 {
		fmt.Fprint(writer, strings.Repeat(" ", textColumn-2-terminalwidth.Cells(label)))
		styleOpen := ""
		if dimmed && styled {
			styleOpen = "\x1b[2m"
		}
		writeStyledWrappedRows(writer, summary, textColumn, columns, styleOpen)
		return
	}
	fmt.Fprintln(writer)
	fmt.Fprint(writer, "    ")
	styleOpen := ""
	if dimmed && styled {
		styleOpen = "\x1b[2m"
	}
	writeStyledWrappedRows(writer, summary, 4, columns, styleOpen)
}

func writeStyledWrappedRows(writer io.Writer, text string, indent, columns int, styleOpen string) {
	if styleOpen == "" {
		writeWrappedRows(writer, text, indent, columns)
		return
	}
	for _, row := range terminalwidth.WrapRows(text, indent, columns) {
		fmt.Fprintf(writer, "%s%s\x1b[0m\n", styleOpen, row)
	}
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

func writeAnswer(writer io.Writer, answer string) error {
	if answer == "" {
		return nil
	}
	if _, err := io.WriteString(writer, answer); err != nil {
		return err
	}
	if !strings.HasSuffix(answer, "\n") {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func writeAssistantAnswer(writer io.Writer, answer string, store *config.Store) error {
	columns, terminal := terminalwidth.WriterColumns(writer)
	return writeAssistantAnswerWithTerminal(writer, answer, store, columns, terminal)
}

func writeAssistantAnswerWithTerminal(writer io.Writer, answer string, store *config.Store, physicalColumns int, terminal bool) error {
	if answer == "" {
		return nil
	}
	if !terminal || store == nil || !store.Bool("markdown") {
		return writeAnswer(writer, answer)
	}
	columns := markdownDisplayColumns(store, physicalColumns)
	if columns < 1 {
		columns = 1
	}
	rendered := terminalmarkdown.Render(answer, columns, true)
	if _, err := io.WriteString(writer, rendered); err != nil {
		return err
	}
	if !terminalmarkdown.HasTrailingNewline(rendered) {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func printHelp(writer io.Writer, store *config.Store) {
	printHelpWithStyle(writer, store, terminalnotify.IsTerminalWriter(writer))
}

func printHelpWithStyle(writer io.Writer, store *config.Store, styled bool) {
	columns := appDisplayWidth(store)
	tagline := "a minimalist coding assistant in your terminal"
	name := "gax " + version.Value
	boldOpen, boldClose := "", ""
	chromeOpen, chromeClose := "", ""
	if styled {
		boldOpen, boldClose = "\x1b[1m", "\x1b[0m"
		chromeOpen = terminaltheme.Open(terminaltheme.Chrome)
		if chromeOpen != "" {
			chromeClose = "\x1b[0m"
		}
	}
	if terminalwidth.Cells(name)+3+terminalwidth.Cells(tagline) <= columns {
		fmt.Fprintf(writer, "%s%s%s — %s\n", boldOpen, name, boldClose, tagline)
	} else {
		fmt.Fprintf(writer, "%s%s%s\n", boldOpen, name, boldClose)
		writeWrappedRows(writer, tagline, 0, columns)
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%susage:%s ", boldOpen, boldClose)
	writeWrappedRows(writer, "gax [PRESET] [OPTIONS] [PROMPT...]", len("usage: "), columns)
	fmt.Fprintln(writer)
	writeWrappedRows(writer, "With no arguments, runs an interactive REPL.", 0, columns)
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%soptions%s\n", boldOpen, boldClose)
	options := []struct{ flags, description string }{
		{"-p, --print", "Non-interactive mode. Runs the prompt to completion and prints the final assistant message to stdout. The prompt comes from PROMPT positional arguments (joined with spaces) when given, otherwise from stdin if stdin is not a terminal."},
		{"--json", "Non-interactive JSON output (implies -p): stream the run as JSON records on stdout, one per line, ending with a result record. Format reference: docs/sessions.md."},
		{"-c, --continue", "Resume the most recent conversation in this directory."},
		{"--resume[=ID]", "Resume a past conversation in this directory, restoring its recorded settings: pick from a list, or give a session ID. Works with -p; without a prompt, the conversation continues from where it stopped."},
		{"--no-session", "Don't record this conversation: nothing to resume afterwards, and the prompts you type aren't added to Ctrl-R recall. Earlier sessions and prompts stay readable."},
		{"--raw", "Send only the prompt text — no system prompt, no Environment section, no AGENTS.md, no skills, and no tools. Useful as a barebones chat interface."},
		{"--bare", "Run without project and delegation context — no AGENTS.md, skills, or subagents section. The Environment section, tools, and base system prompt remain (unlike --raw)."},
		{"--provider=NAME", "Select the backend for this run."},
		{"--model=ID", "Select the model for this run."},
		{"--effort=LEVEL", "Select the reasoning effort for this run."},
		{"--preset=NAME", "Apply the named preset — a presets.NAME selection from the config file. Explicit selection flags win over the preset's values. The name may also be given right after 'gax' in place of this flag."},
		{"-h, --help", "Show this help and exit."},
		{"-v, --version", "Show version and exit."},
	}
	flagWidth := 0
	for _, option := range options {
		if cells := terminalwidth.Cells(option.flags); cells > flagWidth {
			flagWidth = cells
		}
	}
	textColumn := flagWidth + 4
	for _, option := range options {
		fmt.Fprint(writer, "  ")
		if chromeOpen != "" {
			fmt.Fprint(writer, chromeOpen)
		}
		fmt.Fprint(writer, option.flags)
		if chromeOpen != "" {
			fmt.Fprint(writer, chromeClose)
		}
		if columns-textColumn >= 20 {
			fmt.Fprint(writer, strings.Repeat(" ", textColumn-2-terminalwidth.Cells(option.flags)))
			writeWrappedRows(writer, option.description, textColumn, columns)
		} else {
			fmt.Fprintln(writer)
			fmt.Fprint(writer, "    ")
			writeWrappedRows(writer, option.description, 4, columns)
		}
	}
	fmt.Fprintln(writer)
	writeWrappedRows(writer, "The selection flags (--provider, --model, --effort, --preset) apply to this run only and take priority over every other source. Configuration and precedence are covered in README.md.", 0, columns)
}

func appDisplayWidth(store *config.Store) int {
	mode, configured := "auto", 0
	if store != nil {
		if value := store.String("display_width"); value != nil {
			mode = *value
		}
		configured = store.Int("display_width")
	}
	return terminalwidth.DisplayWidth(terminalwidth.TerminalColumns(), mode, configured)
}

func writeWrappedRows(writer io.Writer, text string, indent, columns int) {
	for _, row := range terminalwidth.WrapRows(text, indent, columns) {
		fmt.Fprintln(writer, row)
	}
}

func writeNoteLine(writer io.Writer, text string) {
	writeNoteLineWithStyle(writer, text, terminalnotify.IsTerminalWriter(writer))
}

func writeNoteLineWithStyle(writer io.Writer, text string, styled bool) {
	if styled {
		fmt.Fprintf(writer, "\x1b[2m%s\x1b[0m\n", text)
		return
	}
	fmt.Fprintln(writer, text)
}

func stringPtr(value string) *string { return &value }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

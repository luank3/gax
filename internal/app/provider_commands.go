package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/provider/deepseek"
	"github.com/luank3/gax/internal/provider/opencodego"
	"github.com/luank3/gax/internal/provider/openrouter"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	"github.com/luank3/gax/internal/transport"
)

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

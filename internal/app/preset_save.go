package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

func runPresetSaveCommand(ctx context.Context, parsed slash.Parsed, resolver *runtime.Resolver,
	provider *runtime.Provider, model *string, state *agent.Session, log *session.Log,
	store *config.Store, stdin io.Reader, pendingInput *string, tty, raw bool,
	promptOptions agent.PromptOptions, stdout, stderr io.Writer) (*runtime.Provider, *string, bool) {
	if provider == nil || provider.Definition == nil {
		writeNoteLine(stdout, "no provider selected — use /provider to choose one first")
		return provider, model, false
	}
	if model == nil || *model == "" {
		writeNoteLine(stdout, "no model resolved yet — use /model to pick one first")
		return provider, model, false
	}
	if !parsed.HasArgument || strings.TrimSpace(parsed.Argument) == "" {
		writeNoteLine(stdout, "name it: /preset-save <name> [tint]")
		if tty && pendingInput != nil {
			*pendingInput = "/preset-save "
		}
		return provider, model, false
	}

	name, tintArgument := splitPresetSaveArgument(parsed.Argument)
	if !config.ValidPresetName(name) {
		fmt.Fprintf(stdout, "'%s' can't be a preset name — use letters, digits, '.', '-' or '_', starting with a letter or digit\n", terminalSafeText(name))
		return provider, model, false
	}
	tintSetting := presetTintSetting()
	if tintArgument != "" && !tintSetting.Valid(tintArgument) {
		fmt.Fprintf(stdout, "unknown tint '%s' (expected %s)\n", terminalSafeText(tintArgument), tintSetting.Choices)
		return provider, model, false
	}

	exists := store.PresetExists(name)
	if exists {
		confirmed, opened := confirmPresetOverwrite(name, store, stdin, stdout, stderr)
		if !opened || !confirmed {
			writeNoteLine(stdout, fmt.Sprintf("left preset '%s' unchanged", terminalSafeText(name)))
			return provider, model, false
		}
	}

	var tint *string
	if tintArgument != "" {
		tint = stringPtr(canonicalConfigValue(tintSetting, tintArgument))
	} else {
		selected, ok := pickPresetTint(initialPresetSaveTint(name, exists, store), tintSetting, store, stdin, stdout, stderr)
		if !ok {
			return provider, model, false
		}
		tint = selected
	}

	savedModel := cloneString(model)
	if provider.ModelDiscovered {
		savedModel = nil
	}
	preset := config.Preset{
		Provider: provider.Definition.ID, Model: savedModel, Effort: cloneString(state.Effort),
		SystemPrompt:       capturePresetPrompt(store, "system_prompt"),
		SystemPromptAppend: capturePresetPrompt(store, "system_prompt_append"),
		Tint:               tint, Description: savedPresetDescription(name, exists, store),
	}
	for _, value := range []*string{preset.SystemPrompt, preset.SystemPromptAppend} {
		if value != nil {
			if err := agent.ValidatePromptFile(*value); err != nil {
				fmt.Fprintf(stderr, "gax: preset %q: %v\n", name, err)
				return provider, model, false
			}
		}
	}
	if err := store.PersistPreset(name, preset); err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return provider, model, false
	}
	verb := "saved"
	if exists {
		verb = "updated"
	}
	writeNoteLine(stdout, fmt.Sprintf("%s preset '%s' in config.json", verb, terminalSafeText(name)))
	parsedPreset := slash.Parsed{Argument: name, HasArgument: true}
	return runPresetCommand(ctx, parsedPreset, resolver, provider, model, state, log, store, stdin,
		raw, promptOptions, stdout, stderr)
}

func splitPresetSaveArgument(argument string) (name, tint string) {
	separator := strings.IndexFunc(argument, func(character rune) bool { return character == ' ' || character == '\t' })
	if separator < 0 {
		return argument, ""
	}
	return argument[:separator], strings.TrimSpace(argument[separator:])
}

func presetTintSetting() config.Setting {
	for _, setting := range config.DefaultSettings() {
		if setting.Key == "tint" {
			return setting
		}
	}
	return config.Setting{Key: "tint", Choices: "teal|violet|rose|sage"}
}

func confirmPresetOverwrite(name string, store *config.Store, stdin io.Reader, stdout, stderr io.Writer) (confirmed, opened bool) {
	var previous presetDefinition
	var members map[string]json.RawMessage
	if json.Unmarshal(store.PresetJSON(name), &members) == nil && members != nil {
		previous = make(presetDefinition, len(members))
		for key, raw := range members {
			if value, ok := presetScalar(raw); ok {
				previous[key] = value
			}
		}
	}
	var detail []string
	for _, key := range []string{"provider", "model", "effort"} {
		if value := previous[key]; value != "" {
			detail = append(detail, terminalSafeText(value))
		}
	}
	items := []terminalpicker.Item{
		{Label: "keep it", Description: "Leave the existing definition alone", Current: true},
		{Label: "overwrite", Detail: strings.Join(detail, " · "), Description: "Replace it with the current selection"},
	}
	index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: "preset '" + terminalSafeText(name) + "' already exists", Items: items,
	}, stdinFile(stdin), stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: preset overwrite picker: %v\n", err)
		return false, true
	}
	return opened && index == 1, opened
}

func pickPresetTint(initial *string, setting config.Setting, store *config.Store, stdin io.Reader,
	stdout, stderr io.Writer) (*string, bool) {
	items := make([]terminalpicker.Item, 1, 1+len(strings.Split(setting.Choices, "|")))
	items[0] = terminalpicker.Item{
		Label: "none", Description: "Carry no tint of its own: your tint setting applies",
		Current: initial == nil || *initial == "",
	}
	initialIndex := 0
	for _, choice := range strings.Split(setting.Choices, "|") {
		current := initial != nil && strings.EqualFold(choice, *initial)
		items = append(items, terminalpicker.Item{Label: choice, Current: current, LabelColor: terminaltheme.TintOpen(choice)})
		if current {
			initialIndex = len(items) - 1
		}
	}
	index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: "tint for this preset", Items: items, InitialIndex: initialIndex,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}, stdinFile(stdin), stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: preset tint picker: %v\n", err)
		return nil, false
	}
	if !opened || index < 0 || index >= len(items) {
		return nil, false
	}
	if index == 0 {
		return nil, true
	}
	return stringPtr(items[index].Label), true
}

func initialPresetSaveTint(name string, exists bool, store *config.Store) *string {
	if store.Source("tint") == "run" {
		return store.String("tint")
	}
	if active := currentPreset(store); active != nil {
		if values, err := loadPreset(store, *active); err == nil {
			if value, ok := values["tint"]; ok {
				return stringPtr(value)
			}
		}
	}
	if exists {
		return savedPresetTint(name, store)
	}
	return nil
}

func savedPresetDescription(name string, exists bool, store *config.Store) *string {
	if !exists {
		return nil
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(store.PresetJSON(name), &members) != nil || members == nil {
		return nil
	}
	if raw := members["description"]; len(raw) > 0 {
		if value, ok := presetScalar(raw); ok {
			return stringPtr(value)
		}
	}
	return nil
}

func savedPresetTint(name string, store *config.Store) *string {
	var members map[string]json.RawMessage
	if json.Unmarshal(store.PresetJSON(name), &members) != nil || members == nil {
		return nil
	}
	if raw := members["tint"]; len(raw) > 0 {
		if value, ok := presetScalar(raw); ok {
			return stringPtr(value)
		}
	}
	return nil
}

func capturePresetPrompt(store *config.Store, key string) *string {
	if source := store.Source(key); source == "config" || source == "default" {
		return nil
	}
	return cloneString(store.String(key))
}

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	"github.com/luank3/gax/internal/transport"
)

type presetDefinition map[string]string

func runPresetCommand(ctx context.Context, parsed slash.Parsed, resolver *runtime.Resolver,
	currentProvider *runtime.Provider, currentModel *string, state *agent.Session, log *session.Log,
	store *config.Store, stdin io.Reader, raw bool, promptOptions agent.PromptOptions, stdout, stderr io.Writer) (*runtime.Provider, *string, bool) {
	name := parsed.Argument
	if !parsed.HasArgument {
		names, warnings := validPresetNamesWithWarnings(store)
		for _, warning := range warnings {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		if len(names) == 0 {
			writeNoteLine(stdout, "no presets defined in config.json — use /preset-save to save the current selection")
			return currentProvider, currentModel, false
		}
		items := make([]terminalpicker.Item, 0, len(names))
		active := currentPreset(store)
		for _, candidate := range names {
			values, err := loadPreset(store, candidate)
			if err != nil {
				continue
			}
			var detail []string
			for _, key := range []string{"provider", "model", "effort"} {
				if value := values[key]; value != "" {
					detail = append(detail, terminalSafeText(value))
				}
			}
			items = append(items, terminalpicker.Item{
				Label: candidate, Detail: strings.Join(detail, " · "), Description: values["description"],
				Current:    active != nil && *active == candidate,
				LabelColor: presetTintPreview(values["tint"], store),
			})
		}
		stdinFile, _ := stdin.(*os.File)
		index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
			Title: "select a preset", Items: items,
			DisplayColumns: func(columns int) int { return terminalwidth.ContentColumns(columns, "auto", 0) },
		}, stdinFile, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "gax: preset picker: %v\n", err)
			return currentProvider, currentModel, false
		}
		if !opened {
			for _, item := range items {
				fmt.Fprintf(stdout, "  %s\n", terminalSafeText(item.Label))
			}
			return currentProvider, currentModel, false
		}
		if index < 0 || index >= len(items) {
			return currentProvider, currentModel, false
		}
		name = items[index].Label
	}

	values, err := loadPreset(store, name)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return currentProvider, currentModel, false
	}
	providerID := values["provider"]
	selected, err := resolver.Resolve(providerID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown provider ") {
			fmt.Fprintf(stdout, "preset '%s': unknown provider '%s'\n", terminalSafeText(name), terminalSafeText(providerID))
		} else if strings.Contains(err.Error(), "unsupported api") {
			var resolvedProvider, api string
			if _, scanErr := fmt.Sscanf(err.Error(), `provider %q: unsupported api %q`, &resolvedProvider, &api); scanErr == nil {
				fmt.Fprintf(stderr, "gax: provider '%s': unsupported api '%s' (supported: openai-completions, openai-responses, anthropic-messages, catalog)\n",
					terminalSafeText(resolvedProvider), terminalSafeText(api))
			} else {
				fmt.Fprintf(stderr, "gax: %v\n", err)
			}
			syncLineOutput(stdout)
		} else {
			fmt.Fprintf(stderr, "gax: %v\n", err)
			syncLineOutput(stdout)
		}
		return currentProvider, currentModel, false
	}
	if selected.Availability.State == runtime.AvailabilityUnconfigured {
		fmt.Fprintf(stderr, "gax: %v\n", unconfiguredProviderDiagnostic(selected.Definition))
		syncLineOutput(stdout)
		return currentProvider, currentModel, false
	}
	if selected.Availability.State == runtime.AvailabilityMissingCredential &&
		selected.Definition.Hooks&registry.HookAuthSource != 0 {
		fmt.Fprintf(stderr, "gax: %s\n", selected.Availability.Reason)
		syncLineOutput(stdout)
		return currentProvider, currentModel, false
	}
	model := values["model"]
	if selected.Definition.Hooks&registry.HookDiscover != 0 {
		discoveredModel, modelDiscovered, warning, discoverErr := selected.DiscoverModel(context.Background(), transport.DefaultClient, model)
		if warning != "" {
			writeWarningDiagnostic(stderr, "%s", warning)
		}
		if discoverErr != nil {
			fmt.Fprintf(stderr, "gax: %v\n", modelDiscoveryError(selected, discoverErr))
			syncLineOutput(stdout)
			return currentProvider, currentModel, false
		}
		model = discoveredModel
		selected.ModelDiscovered = modelDiscovered
	}
	if model == "" {
		model = selected.DefaultModel
	}
	if model == "" {
		fmt.Fprintf(stdout, "preset '%s': no model resolves for provider '%s' — name one in the preset\n",
			terminalSafeText(name), terminalSafeText(providerID))
		return currentProvider, currentModel, false
	}
	if err := applyPreset(store, name, configTierRun); err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		return currentProvider, currentModel, false
	}
	startModelProbe(ctx, selected, model, transport.DefaultClient)
	modelValue := stringPtr(model)
	if model == "" {
		modelValue = nil
	}
	store.SetOverride("model", stringPtr(config.DefaultSentinel))
	if modelValue != nil {
		store.SetOverride("model", modelValue)
	}
	warnings := selected.Warnings()
	for _, warning := range warnings {
		writeWarningDiagnostic(stderr, "%s", warning)
	}
	if len(warnings) != 0 {
		syncLineOutput(stdout)
	}
	state.ProviderID = stringPtr(providerID)
	state.Model = modelValue
	state.ModelLabel = cloneString(modelValue)
	state.Effort = selected.ResolveEffort(model, store.String("effort"))
	configureLivePrompt(state, store, model, raw, promptOptions, stderr)
	setSessionSelection(state, log, store)
	if err := store.PersistState("preset", stringPtr(name)); err != nil {
		writeWarningDiagnostic(stderr, "couldn't save to state.json — this preset applies to this run only: %v", err)
	}
	return selected, modelValue, true
}

func validPresetNames(store *config.Store) []string {
	return presetNamesInConfigOrder(store)
}

func validPresetNamesWithWarnings(store *config.Store) ([]string, []string) {
	names := store.ObjectKeys("presets")
	valid := make([]string, 0, len(names))
	var warnings []string
	for _, name := range names {
		if _, err := loadPreset(store, name); err == nil {
			valid = append(valid, name)
			continue
		} else if len(store.PresetJSON(name)) == 0 {
			if store.MarkPresetWarning(name) {
				warnings = append(warnings, fmt.Sprintf(
					"preset '%s' is not an object (define a presets.%s block) — ignoring it", name, name))
			}
		} else if store.MarkPresetWarning(name) {
			warnings = append(warnings, fmt.Sprintf("%s — ignoring it", err))
		}
	}
	return valid, warnings
}

func presetNamesInConfigOrder(store *config.Store) []string {
	names := store.ObjectKeys("presets")
	valid := names[:0]
	for _, name := range names {
		if _, err := loadPreset(store, name); err == nil {
			valid = append(valid, name)
		}
	}
	return valid
}

func presetTintPreview(tint string, store *config.Store) string {
	if tint == "" && store != nil {
		if configured := store.StringBelowRun("tint"); configured != nil {
			tint = *configured
		}
	}
	return terminaltheme.TintOpen(tint)
}

func loadPreset(store *config.Store, name string) (presetDefinition, error) {
	raw := store.PresetJSON(name)
	var members map[string]json.RawMessage
	if len(raw) == 0 {
		return nil, fmt.Errorf("unknown preset '%s' (define a presets.%s block in config.json)", name, name)
	}
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return nil, fmt.Errorf("preset '%s' must name a provider", name)
	}
	keys, err := orderedPresetKeys(raw)
	if err != nil {
		return nil, fmt.Errorf("preset '%s' must name a provider", name)
	}
	allowed := map[string]bool{
		"description": true, "provider": true, "model": true, "effort": true,
		"system_prompt": true, "system_prompt_append": true, "tint": true,
	}
	values := make(presetDefinition, len(members))
	for _, key := range keys {
		member := members[key]
		if !allowed[key] {
			return nil, fmt.Errorf("preset '%s': '%s' is not presettable (allowed: provider, model, effort, system_prompt, system_prompt_append, tint); endpoint settings belong in a providers.<name> block, context/recording in the --bare/--no-session flags", name, key)
		}
		value, ok := presetScalar(member)
		if !ok {
			return nil, fmt.Errorf("preset '%s': '%s' must be a scalar", name, key)
		}
		values[key] = value
	}
	for _, key := range []string{"system_prompt", "system_prompt_append"} {
		if value, exists := values[key]; exists {
			if err := agent.ValidatePromptFile(value); err != nil {
				return nil, fmt.Errorf("preset '%s': %w", name, err)
			}
		}
	}
	if values["provider"] == "" {
		return nil, fmt.Errorf("preset '%s' must name a provider", name)
	}
	for _, setting := range config.DefaultSettings() {
		if setting.Key == "tint" {
			if value, exists := values["tint"]; exists && !setting.Valid(value) {
				return nil, fmt.Errorf("preset '%s': unknown tint '%s' (expected %s)", name, value, setting.Choices)
			}
			break
		}
	}
	return values, nil
}

func orderedPresetKeys(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("expected preset object")
	}
	keys := make([]string, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected preset member name")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return keys, nil
}

func applyPreset(store *config.Store, name string, tier configTier) error {
	values, err := loadPreset(store, name)
	if err != nil {
		return err
	}
	if tier == configTierConversation {
		store.SetConversation("preset", stringPtr(""))
		store.SetConversation("system_prompt", nil)
		store.SetConversation("system_prompt_append", nil)
		store.SetConversation("tint", nil)
	} else {
		exitPreset(store)
		store.SetOverride("tint", nil)
	}
	for _, key := range []string{"provider", "model", "effort", "system_prompt", "system_prompt_append"} {
		value, exists := values[key]
		if (key == "model" || key == "effort") && !exists {
			value, exists = config.DefaultSentinel, true
		}
		if !exists {
			continue
		}
		if tier == configTierConversation {
			store.SetConversation(key, stringPtr(value))
		} else {
			store.SetOverride(key, stringPtr(value))
		}
	}
	if tier == configTierConversation {
		store.SetConversation("preset", stringPtr(name))
	} else {
		store.SetOverride("preset", stringPtr(name))
	}
	return nil
}

func runConfigCommand(parsed slash.Parsed, store *config.Store, stdin io.Reader, pendingInput *string, tty bool, stdout, stderr io.Writer) {
	settings := userConfigSettings()
	if !parsed.HasArgument {
		items := make([]terminalpicker.Item, len(settings))
		for index, setting := range settings {
			items[index] = terminalpicker.Item{
				Label: setting.Key,
				Detail: fmt.Sprintf("%s (%s%s)", settingDisplayValue(store, setting), store.Source(setting.Key),
					settingInvalid(store, setting)),
				Description: settingPickerDescription(setting),
				Dim:         !setting.Editable,
			}
		}
		stdinFile, _ := stdin.(*os.File)
		index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
			Title: "configuration", Items: items,
			DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
		}, stdinFile, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "gax: config picker: %v\n", err)
			return
		}
		if !opened {
			return
		}
		if index < 0 || index >= len(settings) {
			return
		}
		setting := settings[index]
		if !setting.Editable {
			noteReadonlySetting(stdout, store, setting)
			return
		}
		if setting.Choices == "" {
			seedConfigPrompt(setting, store, pendingInput, tty, stdout)
			return
		}
		pickConfigChoice(setting, store, stdin, pendingInput, stdout, stderr)
		return
	}

	key, value, hasValue := splitConfigArgument(parsed.Argument)
	var setting *config.Setting
	allSettings := config.DefaultSettings()
	for index := range allSettings {
		if allSettings[index].Key == key {
			setting = &allSettings[index]
			break
		}
	}
	if setting == nil {
		fmt.Fprintf(stdout, "unknown setting '%s' — /config lists them\n", terminalSafeText(key))
		return
	}
	if !hasValue {
		if setting.Editable {
			printCurrentSetting(stdout, store, *setting)
		} else {
			noteReadonlySetting(stdout, store, *setting)
		}
		return
	}
	setConfigValue(*setting, value, store, stdout, stderr)
}

func userConfigSettings() []config.Setting {
	all := config.DefaultSettings()
	settings := make([]config.Setting, 0, len(all))
	for _, setting := range all {
		if !strings.HasPrefix(setting.Key, "providers.") {
			settings = append(settings, setting)
		}
	}
	return settings
}

func settingPickerDescription(setting config.Setting) string {
	showHint := setting.Kind == config.KindSize || setting.Kind == config.KindTokens || setting.Kind == config.KindDuration ||
		setting.Kind == config.KindInt && (setting.Min != 0 || setting.Max != 0)
	if !showHint {
		return setting.Description
	}
	return setting.Description + " (" + settingValueHint(setting) + ")"
}

func seedConfigPrompt(setting config.Setting, store *config.Store, pendingInput *string, tty bool, stdout io.Writer) {
	if pendingInput != nil && tty {
		value := store.String(setting.Key)
		if value != nil && *value != "" && !setting.Valid(*value) {
			value = store.Default(setting.Key)
		}
		seed := ""
		if value != nil {
			seed = *value
		}
		*pendingInput = "/config " + setting.Key + " " + seed
		return
	}
	printCurrentSetting(stdout, store, setting)
	writeNoteLine(stdout, fmt.Sprintf("  change it with /config %s <value>", setting.Key))
}

func splitConfigArgument(argument string) (key, value string, hasValue bool) {
	end := strings.IndexFunc(argument, func(character rune) bool { return character == ' ' || character == '\t' })
	if end < 0 {
		return argument, "", false
	}
	key = argument[:end]
	value = strings.TrimLeft(argument[end:], " \t")
	return key, value, value != ""
}

func setConfigValue(setting config.Setting, value string, store *config.Store, stdout, stderr io.Writer) {
	if !setting.Editable {
		if command := settingRuntimeCommand(setting.Key); command != "" {
			fmt.Fprintf(stdout, "'%s' can't be changed from /config — use %s\n", setting.Key, command)
		} else {
			fmt.Fprintf(stdout, "'%s' can't be changed at runtime — set %s or config.json and restart\n", setting.Key, setting.EnvVar)
		}
		return
	}
	if value == "default" {
		store.SetOverride(setting.Key, nil)
		printCurrentSetting(stdout, store, setting)
		return
	}
	if !setting.Valid(value) {
		fmt.Fprintf(stdout, "invalid value '%s' for %s (expected: %s, or default)\n", terminalSafeText(value), setting.Key, settingValueHint(setting))
		return
	}
	store.SetOverride(setting.Key, stringPtr(canonicalConfigValue(setting, value)))
	printCurrentSetting(stdout, store, setting)
}

func pickConfigChoice(setting config.Setting, store *config.Store, stdin io.Reader, pendingInput *string, stdout, stderr io.Writer) {
	choices := strings.Split(setting.Choices, "|")
	acceptsExact := setting.Kind != config.KindString
	items := make([]terminalpicker.Item, 1, 1+len(choices)+boolToInt(acceptsExact))
	items[0] = terminalpicker.Item{
		Label: "default", Description: "Clear the runtime override and use the environment, saved configuration, or built-in default",
	}
	current := settingDisplayValue(store, setting)
	initial := 0
	currentChoice := false
	for _, choice := range choices {
		item := terminalpicker.Item{Label: choice, Current: strings.EqualFold(choice, current)}
		if setting.Key == "tint" {
			item.LabelColor = terminaltheme.TintOpen(choice)
		}
		items = append(items, item)
		if strings.EqualFold(choice, current) {
			initial = len(items) - 1
			currentChoice = true
		}
	}
	if acceptsExact {
		exact := terminalpicker.Item{Label: "exact value..."}
		if setting.Example != "" {
			exact.Description = "Enter an exact value such as " + setting.Example
		}
		exact.Current = !currentChoice && setting.Valid(current)
		items = append(items, exact)
		if exact.Current {
			initial = len(items) - 1
		}
	}
	index, opened, err := terminalpicker.RunTTY(terminalpicker.Options{
		Title: setting.Key + " — " + setting.Description, Items: items, InitialIndex: initial,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}, stdinFile(stdin), stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: config value picker: %v\n", err)
		return
	}
	if !opened || index < 0 || index >= len(items) {
		return
	}
	if index == 0 {
		setConfigValue(setting, "default", store, stdout, stderr)
	} else if index <= len(choices) {
		setConfigValue(setting, choices[index-1], store, stdout, stderr)
	} else {
		seed := setting.Example
		if items[index].Current {
			if current := store.String(setting.Key); current != nil {
				seed = *current
			}
		}
		if pendingInput != nil {
			*pendingInput = "/config " + setting.Key + " " + seed
		} else {
			writeNoteLine(stdout, fmt.Sprintf("  enter an exact value with /config %s <value>", setting.Key))
		}
	}
}

func stdinFile(stdin io.Reader) *os.File {
	file, _ := stdin.(*os.File)
	return file
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func settingRuntimeCommand(key string) string {
	switch key {
	case "provider", "model", "effort", "preset":
		return "/" + key
	default:
		return ""
	}
}

func noteReadonlySetting(writer io.Writer, store *config.Store, setting config.Setting) {
	printCurrentSetting(writer, store, setting)
	if command := settingRuntimeCommand(setting.Key); command != "" {
		writeNoteLine(writer, fmt.Sprintf("  change it with %s", command))
	} else {
		writeNoteLine(writer, fmt.Sprintf("  read-only at runtime — set %s or config.json and restart to change", setting.EnvVar))
	}
}

func printCurrentSetting(writer io.Writer, store *config.Store, setting config.Setting) {
	writeNoteLine(writer, fmt.Sprintf("%s = %s (%s%s)", setting.Key, settingDisplayValue(store, setting), store.Source(setting.Key), settingInvalid(store, setting)))
}

func settingDisplayValue(store *config.Store, setting config.Setting) string {
	value := store.String(setting.Key)
	if setting.Secret {
		if value != nil && *value != "" {
			return "set"
		}
		return "unset"
	}
	if setting.Kind == config.KindString && setting.Choices == "on|off" {
		if store.Bool(setting.Key) {
			return "on"
		}
		return "off"
	}
	if setting.Kind == config.KindString && setting.Choices == "auto|on|off" {
		if value == nil || strings.EqualFold(*value, "auto") {
			return "auto"
		}
		if !setting.Valid(*value) {
			return *value
		}
		if store.Bool(setting.Key) {
			return "on"
		}
		return "off"
	}
	if value == nil {
		return "unset"
	}
	if *value == "" {
		return "(empty)"
	}
	return terminalSafeText(*value)
}

func settingInvalid(store *config.Store, setting config.Setting) string {
	value := store.String(setting.Key)
	if setting.Secret || value == nil || *value == "" || setting.Valid(*value) {
		return ""
	}
	return ", invalid"
}

func canonicalConfigValue(setting config.Setting, value string) string {
	if setting.Choices == "" || setting.Kind == config.KindString && setting.Choices == "on|off" {
		return value
	}
	for _, choice := range strings.Split(setting.Choices, "|") {
		if strings.EqualFold(choice, value) {
			return choice
		}
	}
	return value
}

func settingValueHint(setting config.Setting) string {
	if setting.Kind == config.KindString {
		return setting.Choices
	}
	kind := ""
	switch setting.Kind {
	case config.KindInt:
		kind = "a whole number"
		if setting.Min > 0 && setting.Max > 0 {
			kind = fmt.Sprintf("a whole number from %d to %d", setting.Min, setting.Max)
		} else if setting.Max > 0 {
			kind = fmt.Sprintf("a whole number up to %d", setting.Max)
		} else if setting.Min > 0 {
			kind = fmt.Sprintf("a whole number of at least %d", setting.Min)
		}
	case config.KindSize:
		kind = "a byte size like 64k or 1M (k = 1024)"
	case config.KindTokens:
		kind = "a token count like 200k or 1M (k = 1000)"
	case config.KindDuration:
		kind = "a duration like 2s or 500ms"
	}
	if setting.Choices != "" && setting.Example != "" {
		return fmt.Sprintf("%s, or %s; e.g. %s", setting.Choices, kind, setting.Example)
	}
	if setting.Choices != "" {
		return setting.Choices + ", or " + kind
	}
	if setting.Example != "" {
		return fmt.Sprintf("%s; e.g. %s", kind, setting.Example)
	}
	return kind
}

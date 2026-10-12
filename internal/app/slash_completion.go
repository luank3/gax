package app

import (
	"sort"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/modelsort"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/slash"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
)

// slashCompleter adapts the slash package's completer, which needs the values the current selection
// enumerates, to the editor's single-token callbacks.
func slashCompleter(choices slash.ArgumentChoices) terminalinput.Completer {
	return terminalinput.Completer{
		Match:      slash.Match,
		Complete:   func(text string) (string, bool) { return slash.Complete(text, choices) },
		Candidates: func(text string) (string, bool) { return slash.ListCandidates(text, choices) },
	}
}

// slashArgumentChoices lists the values Tab offers for an enumerating command's first argument,
// following the per-command choice hooks. Every value is already in memory: a Tab cannot wait
// on the network or a model listing.
func slashArgumentChoices(name string, providers *registry.Registry, provider *runtime.Provider,
	model *string, store *config.Store) []string {
	var choices []string
	switch name {
	case "provider":
		choices = providerIDChoices(providers)
	case "model":
		choices = modelIDChoices(provider)
	case "effort":
		choices = effortLevelChoices(provider, model)
	case "new", "preset":
		choices = presetNameChoices(store)
	default:
		return nil
	}
	// The editor echoes candidates into the prompt, so the ids and levels that reach them from
	// configuration and remote listings are filtered like every other terminal-bound value.
	for index, choice := range choices {
		choices[index] = terminalSafeText(choice)
	}
	return choices
}

func providerIDChoices(providers *registry.Registry) []string {
	if providers == nil {
		return nil
	}
	definitions := providers.All()
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	sort.Strings(ids)
	return ids
}

func modelIDChoices(provider *runtime.Provider) []string {
	if provider == nil {
		return nil
	}
	ids := provider.ListedModelIDs()
	if !provider.SortModels() {
		return ids
	}
	sort.Slice(ids, func(first, second int) bool {
		return modelsort.CompareIDs(ids[first], ids[second]) < 0
	})
	return ids
}

// effortLevelChoices always offers default, which lets the provider choose.
func effortLevelChoices(provider *runtime.Provider, model *string) []string {
	if provider == nil {
		return nil
	}
	modelID := ""
	if model != nil {
		modelID = *model
	}
	return append(provider.EffortLevels(modelID), "default")
}

// presetNameChoices offers only names in the preset-name grammar: a hand-written name outside it,
// such as one with a space, cannot complete as one word.
func presetNameChoices(store *config.Store) []string {
	if store == nil {
		return nil
	}
	names := validPresetNames(store)
	completable := make([]string, 0, len(names))
	for _, name := range names {
		if config.ValidPresetName(name) {
			completable = append(completable, name)
		}
	}
	sort.Strings(completable)
	return completable
}

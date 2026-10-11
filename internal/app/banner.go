package app

import (
	"fmt"
	"io"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	terminalbanner "github.com/luank3/gax/internal/terminal/banner"
	terminalnotify "github.com/luank3/gax/internal/terminal/notify"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

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

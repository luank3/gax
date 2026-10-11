package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/cli"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/transport"
)

func exitPreset(store *config.Store) {
	store.SetOverride("preset", stringPtr(""))
	store.SetOverride("system_prompt", nil)
	store.SetOverride("system_prompt_append", nil)
	store.SetConversation("preset", nil)
	store.SetConversation("system_prompt", nil)
	store.SetConversation("system_prompt_append", nil)
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

func loadStore(stderr io.Writer) (*config.Store, error) {
	store := config.New(config.DefaultSettings())
	if path, ok := systempath.XDGGaxPath(os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME"), ".config", "config.json"); ok {
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
	if path, ok := systempath.XDGGaxPath(os.Getenv("XDG_STATE_HOME"), os.Getenv("HOME"), ".local/state", "state.json"); ok {
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

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
)

const maxPersistSymlinks = 32
const maxPresetPromptBytes = 64 << 10

// Preset is the persisted user-facing selection captured by /preset-save. Nil fields are omitted.
type Preset struct {
	Description        *string
	Tint               *string
	Provider           string
	Model              *string
	Effort             *string
	SystemPrompt       *string
	SystemPromptAppend *string
}

// PersistState atomically updates one nested value in state.json and commits the in-memory state
// tier only after the file write succeeds.
func (store *Store) PersistState(key string, value *string) error {
	if store == nil {
		return errors.New("config store is unavailable")
	}
	path, err := xdgGaxPath("XDG_STATE_HOME", ".local/state", "state.json")
	if err != nil {
		return err
	}
	updated, err := persistObject(store.state, key, value)
	if err != nil {
		return err
	}
	if err := writeOrderedAtomic(path, updated); err != nil {
		return err
	}
	store.state = updated
	return nil
}

// PersistConfig updates config.json. A config file that failed to load is left untouched so a
// command cannot replace user data it could not parse.
func (store *Store) PersistConfig(key string, value *string) error {
	if store == nil {
		return errors.New("config store is unavailable")
	}
	if store.fileUnusable {
		return errors.New("config file could not be loaded; refusing to overwrite it")
	}
	path, err := xdgGaxPath("XDG_CONFIG_HOME", ".config", "config.json")
	if err != nil {
		return err
	}
	updated, err := persistObject(store.file, key, value)
	if err != nil {
		return err
	}
	if err := writeOrderedAtomic(path, updated); err != nil {
		return err
	}
	store.file = updated
	return nil
}

// PersistPreset atomically adds or replaces one nested preset in config.json. It keeps the
// current in-memory config tier unchanged unless the write succeeds.
func (store *Store) PersistPreset(name string, preset Preset) error {
	if store == nil {
		return errors.New("config store is unavailable")
	}
	if !ValidPresetName(name) {
		return fmt.Errorf("'%s' can't be a preset name — use letters, digits, '.', '-' or '_', starting with a letter or digit", name)
	}
	if store.fileUnusable {
		return errors.New("config file could not be loaded; refusing to overwrite it")
	}
	if preset.Provider == "" {
		return errors.New("preset must name a provider")
	}
	if preset.Tint != nil && !validPresetTint(*preset.Tint) {
		return fmt.Errorf("preset %q: unknown tint %q (expected teal|violet|rose|sage)", name, *preset.Tint)
	}
	if stateDefinesPreset(store.state, name) {
		return fmt.Errorf("preset %q is defined in state.json, which outranks the config file — remove it there first", name)
	}
	for _, member := range []struct {
		key   string
		value *string
	}{
		{"system_prompt", preset.SystemPrompt},
		{"system_prompt_append", preset.SystemPromptAppend},
	} {
		if member.value != nil {
			if err := validatePresetPromptFile(*member.value); err != nil {
				return fmt.Errorf("preset %q: %s: %w", name, member.key, err)
			}
		}
	}
	path, err := xdgGaxPath("XDG_CONFIG_HOME", ".config", "config.json")
	if err != nil {
		return err
	}
	updated := cloneOrderedObject(store.file)
	if updated.values == nil {
		updated.values = make(map[string]json.RawMessage)
	}
	deleteOrderedValue(&updated, "presets."+name)

	var presets orderedObject
	if raw := updated.values["presets"]; len(raw) > 0 {
		presets, err = parseObject(raw)
		if err != nil {
			return fmt.Errorf("\"presets\" in %s is not a block of presets — fix it first", path)
		}
	}
	if presets.values == nil {
		presets.values = make(map[string]json.RawMessage)
	}

	definition := orderedObject{values: make(map[string]json.RawMessage)}
	for _, member := range []struct {
		key   string
		value *string
	}{
		{"description", preset.Description}, {"tint", preset.Tint}, {"provider", &preset.Provider},
		{"model", preset.Model}, {"effort", preset.Effort},
		{"system_prompt", preset.SystemPrompt}, {"system_prompt_append", preset.SystemPromptAppend},
	} {
		if member.value == nil {
			continue
		}
		setOrderedValue(&definition, member.key, marshalString(*member.value))
	}
	encodedDefinition, err := marshalOrderedCompact(definition)
	if err != nil {
		return err
	}
	setOrderedValue(&presets, name, encodedDefinition)
	encodedPresets, err := marshalOrderedCompact(presets)
	if err != nil {
		return err
	}
	setOrderedValue(&updated, "presets", encodedPresets)
	if err := writeOrderedAtomic(path, updated); err != nil {
		return fmt.Errorf("couldn't write %s: %w", path, err)
	}
	store.file = updated
	return nil
}

func validatePresetPromptFile(value string) error {
	if !strings.HasPrefix(value, "@") {
		return nil
	}
	spec := value[1:]
	path := spec
	switch {
	case strings.HasPrefix(spec, "~"):
		path = systempath.ExpandHome(spec, os.Getenv("HOME"))
	case strings.HasPrefix(spec, "/"):
		path = spec
	default:
		configPath, err := xdgGaxPath("XDG_CONFIG_HOME", ".config", spec)
		if err != nil {
			return fmt.Errorf("couldn't resolve prompt file %q: %w", spec, err)
		}
		path = configPath
	}

	_, truncated, err := systemfs.ReadFileCapped(path, maxPresetPromptBytes)
	if err != nil {
		return fmt.Errorf("couldn't read prompt file %s: %w", path, err)
	}
	if truncated {
		return fmt.Errorf("prompt file %s exceeds %d bytes", path, maxPresetPromptBytes)
	}
	return nil
}

// ValidPresetName matches the names accepted by the preset registry and persistence path.
func ValidPresetName(name string) bool {
	if len(name) == 0 || len(name) >= 64 || !presetNameAlphaNumeric(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if !presetNameAlphaNumeric(value) && value != '.' && value != '-' && value != '_' {
			return false
		}
	}
	return true
}

func presetNameAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validPresetTint(value string) bool {
	for _, setting := range DefaultSettings() {
		if setting.Key == "tint" {
			return setting.Valid(value)
		}
	}
	return false
}

func stateDefinesPreset(state orderedObject, name string) bool {
	presets, err := parseObject(state.values["presets"])
	if err != nil {
		return false
	}
	_, err = parseObject(presets.values[name])
	return err == nil && len(presets.values[name]) > 0
}

// PersistSelection saves provider, model, and effort together. Missing values preserve the
// current provider's choice, or select that setting's default when the provider changes.
func (store *Store) PersistSelection(provider string, model, effort *string) error {
	if store == nil {
		return errors.New("config store is unavailable")
	}
	if provider == "" {
		return errors.New("provider selection is required")
	}
	previous := scalarString(objectGet(store.state, "provider"))
	providerChanged := previous == nil || canonicalProviderID(*previous) != canonicalProviderID(provider)
	updated := cloneOrderedObject(store.state)
	if updated.values == nil {
		updated.values = make(map[string]json.RawMessage)
	}
	deleteOrderedValue(&updated, "preset")
	setOrderedValue(&updated, "provider", marshalString(provider))
	if model != nil || providerChanged {
		value := model
		if value == nil {
			value = stringPointer(DefaultSentinel)
		}
		setOrderedValue(&updated, "model", marshalString(*value))
	}
	if effort != nil || providerChanged {
		value := effort
		if value == nil {
			value = stringPointer(DefaultSentinel)
		}
		setOrderedValue(&updated, "effort", marshalString(*value))
	}
	path, err := xdgGaxPath("XDG_STATE_HOME", ".local/state", "state.json")
	if err != nil {
		return err
	}
	if err := writeOrderedAtomic(path, updated); err != nil {
		return err
	}
	store.state = updated
	return nil
}

func persistObject(original orderedObject, key string, value *string) (orderedObject, error) {
	segments, err := keySegments(key)
	if err != nil {
		return orderedObject{}, err
	}
	updated := cloneOrderedObject(original)
	if updated.values == nil {
		updated.values = make(map[string]json.RawMessage)
	}
	// A flat dotted spelling takes precedence over nested access, so remove it before writing the
	// nested representation just as config resolution does.
	deleteOrderedValue(&updated, key)
	var raw json.RawMessage
	if value != nil {
		encoded, err := json.Marshal(*value)
		if err != nil {
			return orderedObject{}, err
		}
		raw = encoded
	}
	if err := setNestedOrdered(&updated, segments, raw); err != nil {
		return orderedObject{}, err
	}
	return updated, nil
}

func keySegments(key string) ([]string, error) {
	if key == "" {
		return nil, errors.New("config key is empty")
	}
	segments := strings.Split(key, ".")
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("config key %q has an empty segment", key)
		}
	}
	return segments, nil
}

func cloneOrderedObject(object orderedObject) orderedObject {
	clone := orderedObject{keys: append([]string(nil), object.keys...), values: make(map[string]json.RawMessage, len(object.values))}
	for key, value := range object.values {
		clone.values[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}

func marshalString(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func setNestedOrdered(object *orderedObject, segments []string, raw json.RawMessage) error {
	if len(segments) == 1 {
		if raw == nil {
			deleteOrderedValue(object, segments[0])
		} else {
			setOrderedValue(object, segments[0], raw)
		}
		return nil
	}
	child := orderedObject{}
	if current := object.values[segments[0]]; len(current) > 0 {
		parsed, err := parseObject(current)
		if err == nil {
			child = parsed
		}
	}
	if child.values == nil {
		child.values = make(map[string]json.RawMessage)
	}
	if err := setNestedOrdered(&child, segments[1:], raw); err != nil {
		return err
	}
	encoded, err := marshalOrderedCompact(child)
	if err != nil {
		return err
	}
	setOrderedValue(object, segments[0], encoded)
	return nil
}

func setOrderedValue(object *orderedObject, key string, raw json.RawMessage) {
	if object.values == nil {
		object.values = make(map[string]json.RawMessage)
	}
	if _, exists := object.values[key]; !exists {
		object.keys = append(object.keys, key)
	}
	object.values[key] = append(json.RawMessage(nil), raw...)
}

func deleteOrderedValue(object *orderedObject, key string) {
	if object.values == nil {
		return
	}
	if _, exists := object.values[key]; !exists {
		return
	}
	delete(object.values, key)
	for index, candidate := range object.keys {
		if candidate == key {
			object.keys = append(object.keys[:index], object.keys[index+1:]...)
			return
		}
	}
}

func marshalOrderedCompact(object orderedObject) (json.RawMessage, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	first := true
	for _, key := range object.keys {
		raw, exists := object.values[key]
		if !exists {
			continue
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		var compactValue bytes.Buffer
		if err := json.Compact(&compactValue, raw); err != nil {
			return nil, err
		}
		if !first {
			output.WriteByte(',')
		}
		first = false
		output.Write(encodedKey)
		output.WriteByte(':')
		output.Write(compactValue.Bytes())
	}
	output.WriteByte('}')
	return append(json.RawMessage(nil), output.Bytes()...), nil
}

func marshalOrdered(object orderedObject) ([]byte, error) {
	compact, err := marshalOrderedCompact(object)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := json.Indent(&output, compact, "", "  "); err != nil {
		return nil, err
	}
	output.WriteByte('\n')
	return output.Bytes(), nil
}

func writeOrderedAtomic(path string, object orderedObject) error {
	destination, err := resolveLinkTarget(path)
	if err != nil {
		return err
	}
	data, err := marshalOrdered(object)
	if err != nil {
		return err
	}
	directory := systempath.Dir(destination)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func resolveLinkTarget(path string) (string, error) {
	current := path
	for range maxPersistSymlinks {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return current, nil
		}
		target, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = systempath.Join(systempath.Dir(current), target)
		}
		current = target
	}
	return "", errors.New("too many config file symlinks")
}

func xdgGaxPath(environment, homeRelative, relative string) (string, error) {
	path, ok := systempath.XDGGaxPath(os.Getenv(environment), os.Getenv("HOME"), homeRelative, relative)
	if !ok {
		return "", fmt.Errorf("cannot resolve config path without %s or HOME", environment)
	}
	return path, nil
}

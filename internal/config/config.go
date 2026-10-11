// Package config implements the layered JSON and environment configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultSentinel = "(default)"
const MaxTierFileBytes int64 = 1 << 20

type Kind uint8

const (
	KindString Kind = iota
	KindInt
	KindSize
	KindTokens
	KindDuration
	KindBool
)

type Setting struct {
	Key         string
	EnvVar      string
	Default     string
	HasDefault  bool
	Description string
	Choices     string
	Example     string
	Kind        Kind
	Min         int64
	Max         int64
	Editable    bool
	Secret      bool
	KeepEmpty   bool
}

type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// Store is foreground-owned, like upstream's process-wide config object. Its load operations and
// setters replace tiers; getters resolve them in the documented precedence order.
type Store struct {
	settings               map[string]Setting
	run                    map[string]json.RawMessage
	conversation           map[string]json.RawMessage
	state                  orderedObject
	file                   orderedObject
	fileUnusable           bool
	environment            func(string) (string, bool)
	reportedPresetWarnings map[string]bool
}

func New(settings []Setting) *Store {
	store := &Store{
		settings:    make(map[string]Setting, len(settings)),
		environment: os.LookupEnv,
	}
	for _, setting := range settings {
		store.settings[setting.Key] = setting
	}
	return store
}

func (store *Store) SetEnvironment(lookup func(string) (string, bool)) {
	if lookup == nil {
		store.environment = func(string) (string, bool) { return "", false }
		return
	}
	store.environment = lookup
}

// LookupEnv reads through the store's environment source so config consumers and tests share the
// same environment view as scalar setting resolution.
func (store *Store) LookupEnv(name string) (string, bool) {
	if store == nil || store.environment == nil {
		return "", false
	}
	return store.environment(name)
}

// LoadConfig replaces the config-file tier. Empty input clears it. Invalid JSON or a non-object
// root also clears it and returns an error, matching config_load's destructive-load semantics.
func (store *Store) LoadConfig(data []byte) error {
	store.file = orderedObject{}
	store.fileUnusable = false
	store.reportedPresetWarnings = nil
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	object, err := parseObject(data)
	if err != nil {
		store.fileUnusable = true
		return fmt.Errorf("load config: %w", err)
	}
	store.file = object
	return nil
}

// RejectConfigFile clears the config tier and prevents later writes from replacing a file that
// could not be read safely.
func (store *Store) RejectConfigFile() {
	if store == nil {
		return
	}
	store.file = orderedObject{}
	store.fileUnusable = true
	store.reportedPresetWarnings = nil
}

// LoadState replaces the state-file tier. Empty input clears it.
func (store *Store) LoadState(data []byte) error {
	store.state = orderedObject{}
	store.reportedPresetWarnings = nil
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	object, err := parseObject(data)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	store.state = object
	return nil
}

// MarkPresetWarning reports whether this preset's configuration warning has not been emitted
// since the most recent config or state load.
func (store *Store) MarkPresetWarning(name string) bool {
	if store == nil {
		return false
	}
	if store.reportedPresetWarnings == nil {
		store.reportedPresetWarnings = make(map[string]bool)
	}
	if store.reportedPresetWarnings[name] {
		return false
	}
	store.reportedPresetWarnings[name] = true
	return true
}

func parseObject(data []byte) (orderedObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return orderedObject{}, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return orderedObject{}, errors.New("JSON root must be an object")
	}
	object := orderedObject{values: make(map[string]json.RawMessage)}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return orderedObject{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return orderedObject{}, errors.New("object key is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return orderedObject{}, err
		}
		if _, exists := object.values[key]; !exists {
			object.keys = append(object.keys, key)
		}
		object.values[key] = append(json.RawMessage(nil), value...)
	}
	if _, err := decoder.Token(); err != nil {
		return orderedObject{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return orderedObject{}, errors.New("unexpected data after JSON object")
		}
		return orderedObject{}, err
	}
	return object, nil
}

func (store *Store) SetOverride(key string, value *string) { store.set(&store.run, key, value) }

func (store *Store) SetConversation(key string, value *string) {
	store.set(&store.conversation, key, value)
}

func (store *Store) ClearConversation() { store.conversation = nil }

func (store *Store) set(tier *map[string]json.RawMessage, key string, value *string) {
	if *tier == nil {
		*tier = make(map[string]json.RawMessage)
	}
	if value == nil {
		delete(*tier, key)
		return
	}
	encoded, _ := json.Marshal(*value)
	(*tier)[key] = encoded
}

func (store *Store) String(key string) *string {
	return store.resolve(key, store.settingSkipsEmpty(key), false).value
}

func (store *Store) StringNonempty(key string) *string { return store.resolve(key, true, false).value }

func (store *Store) StringBelowRun(key string) *string {
	return store.resolve(key, store.settingSkipsEmpty(key), true).value
}

func (store *Store) Default(key string) *string {
	setting, ok := store.settings[key]
	if !ok || !settingHasDefault(setting) {
		return nil
	}
	return stringPointer(setting.Default)
}

type resolvedValue struct {
	value  *string
	source string
}

func (store *Store) Source(key string) string {
	return store.resolve(key, store.settingSkipsEmpty(key), false).source
}

func (store *Store) resolve(key string, skipEmpty, skipRun bool) resolvedValue {
	setting, hasSetting := store.settings[key]
	if !skipRun {
		if value := scalarString(store.run[key]); present(value, skipEmpty) {
			return resolvedValue{value: applyDefault(value, setting, hasSetting), source: "run"}
		}
	}
	if value := scalarString(store.conversationValue(key)); present(value, skipEmpty) && store.bindingAllows(store.conversation, key) {
		return resolvedValue{value: applyDefault(value, setting, hasSetting), source: "conversation"}
	}
	if hasSetting && setting.EnvVar != "" && store.environment != nil {
		if envValue, ok := store.environment(setting.EnvVar); ok {
			value := stringPointer(envValue)
			if present(value, skipEmpty) {
				return resolvedValue{value: applyDefault(value, setting, true), source: "env"}
			}
		}
	}
	if value := scalarString(objectGet(store.state, key)); present(value, skipEmpty) && store.bindingAllows(store.state.values, key) {
		return resolvedValue{value: applyDefault(value, setting, hasSetting), source: "state"}
	}
	if value := scalarString(objectGet(store.file, key)); present(value, skipEmpty) && store.bindingAllows(store.file.values, key) {
		return resolvedValue{value: applyDefault(value, setting, hasSetting), source: "config"}
	}
	return resolvedValue{value: store.Default(key), source: "default"}
}

func (store *Store) settingSkipsEmpty(key string) bool {
	setting, ok := store.settings[key]
	return ok && !setting.KeepEmpty
}

func (store *Store) conversationValue(key string) json.RawMessage {
	return store.conversation[key]
}

func (store *Store) bindingAllows(tier map[string]json.RawMessage, key string) bool {
	if key != "model" && key != "effort" {
		return true
	}
	bound := scalarString(objectGet(orderedObject{values: tier}, "provider"))
	if bound == nil || *bound == "" {
		return true
	}
	active := store.resolve("provider", false, false).value
	return active != nil && *active != "" && canonicalProviderID(*active) == canonicalProviderID(*bound)
}

func canonicalProviderID(value string) string {
	if value == "llama.cpp" {
		return "llamacpp"
	}
	return value
}

func applyDefault(value *string, setting Setting, hasSetting bool) *string {
	if value == nil || *value != DefaultSentinel {
		return value
	}
	if !hasSetting || !settingHasDefault(setting) {
		return nil
	}
	return stringPointer(setting.Default)
}

func present(value *string, skipEmpty bool) bool { return value != nil && (!skipEmpty || *value != "") }

func (store *Store) JSONNode(key string) json.RawMessage {
	if value := objectGet(store.state, key); len(value) > 0 {
		return append(json.RawMessage(nil), value...)
	}
	return append(json.RawMessage(nil), objectGet(store.file, key)...)
}

// PresetJSON resolves a named preset as a literal member of presets, preserving dots in names.
// Exact nested definitions are checked across both file tiers before the legacy flat dotted form.
func (store *Store) PresetJSON(name string) json.RawMessage {
	if store == nil || name == "" {
		return nil
	}
	for _, tier := range []orderedObject{store.state, store.file} {
		presets, err := parseObject(objectGet(tier, "presets"))
		if err != nil {
			continue
		}
		if raw := presets.values[name]; isJSONObject(raw) {
			return append(json.RawMessage(nil), raw...)
		}
	}
	key := "presets." + name
	for _, tier := range []orderedObject{store.state, store.file} {
		if raw := objectGet(tier, key); isJSONObject(raw) {
			return append(json.RawMessage(nil), raw...)
		}
	}
	return nil
}

// PresetExists reports whether any definition occupies the name in state or config, including an
// invalid non-object value that should not be silently overwritten by /preset-save.
func (store *Store) PresetExists(name string) bool {
	if store == nil || name == "" {
		return false
	}
	for _, tier := range []orderedObject{store.state, store.file} {
		presets, err := parseObject(objectGet(tier, "presets"))
		if err == nil {
			if _, exists := presets.values[name]; exists {
				return true
			}
		}
	}
	key := "presets." + name
	return len(objectGet(store.state, key)) > 0 || len(objectGet(store.file, key)) > 0
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func (store *Store) ObjectKeys(key string) []string {
	seen := make(map[string]bool)
	keys := make([]string, 0)
	for _, tier := range []orderedObject{store.file, store.state} {
		for _, child := range objectChildKeys(tier, key) {
			if !seen[child] {
				seen[child] = true
				keys = append(keys, child)
			}
		}
	}
	return keys
}

func objectChildKeys(tier orderedObject, key string) []string {
	keys := make([]string, 0)
	seen := make(map[string]bool)
	if object, err := parseObject(objectGet(tier, key)); err == nil {
		for _, child := range object.keys {
			if !seen[child] {
				seen[child] = true
				keys = append(keys, child)
			}
		}
	}
	prefix := key + "."
	for _, name := range tier.keys {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		child := strings.TrimPrefix(name, prefix)
		if dot := strings.IndexByte(child, '.'); dot >= 0 {
			child = child[:dot]
		}
		if child != "" && !seen[child] {
			seen[child] = true
			keys = append(keys, child)
		}
	}
	return keys
}

func objectGet(tier orderedObject, key string) json.RawMessage {
	if len(tier.values) == 0 {
		return nil
	}
	if raw := tier.values[key]; len(raw) > 0 {
		return raw // A flat dotted spelling takes precedence over a nested spelling.
	}
	segments := strings.Split(key, ".")
	var object = tier
	var raw json.RawMessage
	for index, segment := range segments {
		raw = object.values[segment]
		if len(raw) == 0 || index == len(segments)-1 {
			return raw
		}
		nested, err := parseObject(raw)
		if err != nil {
			return nil
		}
		object = nested
	}
	return raw
}

func scalarString(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '"' {
		var value string
		if json.Unmarshal(trimmed, &value) == nil {
			return &value
		}
		return nil
	}
	switch string(trimmed) {
	case "true":
		return stringPointer("1")
	case "false":
		return stringPointer("0")
	case "null":
		return nil
	}
	if trimmed[0] == '-' || trimmed[0] >= '0' && trimmed[0] <= '9' {
		if !bytes.ContainsAny(trimmed, ".eE") {
			var value int64
			if json.Unmarshal(trimmed, &value) == nil {
				return stringPointer(strconv.FormatInt(value, 10))
			}
		}
		var value float64
		if json.Unmarshal(trimmed, &value) == nil {
			return stringPointer(strconv.FormatFloat(value, 'g', 6, 64))
		}
	}
	return nil
}

func stringPointer(value string) *string { return &value }

func settingHasDefault(setting Setting) bool { return setting.HasDefault || setting.Default != "" }

func (store *Store) Int(key string) int {
	setting, hasSetting := store.settings[key]
	value := parseNonnegativeInt(store.resolve(key, true, false).value)
	if value != nil && boundsAllow(setting, hasSetting, *value) {
		return int(*value)
	}
	if hasSetting {
		if fallback := parseNonnegativeInt(stringPointer(setting.Default)); fallback != nil {
			return int(*fallback)
		}
	}
	return 0
}

func parseNonnegativeInt(value *string) *int64 {
	if value == nil {
		return nil
	}
	// Match strtol followed by the upstream C int bounds check: leading C whitespace is
	// accepted, but trailing whitespace or any other suffix is rejected.
	text := trimStrtolLeadingWhitespace(*value)
	parsed, err := strconv.ParseInt(text, 10, 32)
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func trimStrtolLeadingWhitespace(value string) string {
	return strings.TrimLeft(value, " \t\n\v\f\r")
}

func (store *Store) Bool(key string) bool {
	setting, hasSetting := store.settings[key]
	if parsed := parseBool(store.resolve(key, true, false).value); parsed != nil {
		return *parsed
	}
	if hasSetting {
		if parsed := parseBool(stringPointer(setting.Default)); parsed != nil {
			return *parsed
		}
	}
	return false
}

func (store *Store) BoolOr(key string, fallback bool) bool {
	if parsed := parseBool(store.resolve(key, true, false).value); parsed != nil {
		return *parsed
	}
	return fallback
}

func parseBool(value *string) *bool {
	if value == nil {
		return nil
	}
	switch {
	case *value == "1" || asciiEqualFold(*value, "true") || asciiEqualFold(*value, "yes") || asciiEqualFold(*value, "on"):
		result := true
		return &result
	case *value == "0" || asciiEqualFold(*value, "false") || asciiEqualFold(*value, "no") || asciiEqualFold(*value, "off"):
		result := false
		return &result
	default:
		return nil
	}
}

func asciiEqualFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := 0; index < len(left); index++ {
		leftByte, rightByte := left[index], right[index]
		if leftByte >= 'A' && leftByte <= 'Z' {
			leftByte += 'a' - 'A'
		}
		if rightByte >= 'A' && rightByte <= 'Z' {
			rightByte += 'a' - 'A'
		}
		if leftByte != rightByte {
			return false
		}
	}
	return true
}

func boundsAllow(setting Setting, hasSetting bool, value int64) bool {
	if !hasSetting {
		return true
	}
	return (setting.Min == 0 || value >= setting.Min) && (setting.Max == 0 || value <= setting.Max)
}

func (store *Store) Size(key string) int64 {
	return store.scaled(key, 1024)
}

func (store *Store) Tokens(key string) int64 {
	return store.scaled(key, 1000)
}

func (store *Store) scaled(key string, kilo int64) int64 {
	setting, hasSetting := store.settings[key]
	value := parseScaled(store.resolve(key, true, false).value, kilo)
	if value > 0 && boundsAllow(setting, hasSetting, value) {
		return value
	}
	if hasSetting {
		return parseScaled(stringPointer(setting.Default), kilo)
	}
	return 0
}

func parseScaled(text *string, kilo int64) int64 {
	if text == nil {
		return 0
	}
	value := trimStrtolLeadingWhitespace(*text)
	numberEnd := 0
	if numberEnd < len(value) && value[numberEnd] == '+' {
		numberEnd++
	}
	digitStart := numberEnd
	for numberEnd < len(value) && value[numberEnd] >= '0' && value[numberEnd] <= '9' {
		numberEnd++
	}
	if numberEnd == digitStart {
		return 0
	}
	parsed, err := strconv.ParseInt(value[:numberEnd], 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	rest := strings.TrimLeft(value[numberEnd:], " \t")
	multiplier := int64(1)
	if len(rest) > 0 {
		switch rest[0] {
		case 'k', 'K':
			multiplier = kilo
			rest = rest[1:]
		case 'm', 'M':
			if kilo > math.MaxInt64/kilo {
				return 0
			}
			multiplier = kilo * kilo
			rest = rest[1:]
		}
	}
	if strings.Trim(rest, " \t") != "" || parsed > math.MaxInt64/multiplier {
		return 0
	}
	return parsed * multiplier
}

func (store *Store) Duration(key string) time.Duration {
	milliseconds := store.DurationMS(key)
	maxMilliseconds := int64(math.MaxInt64) / int64(time.Millisecond)
	if milliseconds > maxMilliseconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// DurationMS returns the resolved nonnegative duration in upstream's native millisecond unit.
// Duration exposes the same setting as a Go duration and saturates only when nanoseconds cannot
// represent its millisecond value.
func (store *Store) DurationMS(key string) int64 {
	setting, hasSetting := store.settings[key]
	value := parseDuration(store.resolve(key, true, false).value)
	if value >= 0 && boundsAllow(setting, hasSetting, value) {
		return value
	}
	if hasSetting {
		fallback := parseDuration(stringPointer(setting.Default))
		if fallback >= 0 {
			return fallback
		}
	}
	return 0
}

func parseDuration(text *string) int64 {
	if text == nil {
		return -1
	}
	value := trimStrtolLeadingWhitespace(*text)
	numberEnd := 0
	if numberEnd < len(value) && (value[numberEnd] == '+' || value[numberEnd] == '-') {
		numberEnd++
	}
	digitStart := numberEnd
	for numberEnd < len(value) && value[numberEnd] >= '0' && value[numberEnd] <= '9' {
		numberEnd++
	}
	if numberEnd == digitStart {
		return -1
	}
	parsed, err := strconv.ParseInt(value[:numberEnd], 10, 64)
	if err != nil || parsed < 0 {
		return -1
	}
	rest := strings.Trim(value[numberEnd:], " \t")
	multiplier := int64(1000)
	switch {
	case asciiEqualFold(rest, "ms"):
		multiplier = 1
	case rest == "" || asciiEqualFold(rest, "s"):
		multiplier = 1000
	case asciiEqualFold(rest, "m"):
		multiplier = 60000
	case asciiEqualFold(rest, "h"):
		multiplier = 3600000
	default:
		return -1
	}
	if multiplier > 1 && parsed > math.MaxInt64/multiplier {
		return -1
	}
	return parsed * multiplier
}

func (setting Setting) Valid(value string) bool {
	if setting.Choices != "" {
		if setting.Kind == KindString {
			switch setting.Choices {
			case "on|off":
				return parseBool(stringPointer(value)) != nil
			case "auto|on|off":
				return asciiEqualFold(value, "auto") || parseBool(stringPointer(value)) != nil
			}
		}
		for _, choice := range strings.Split(setting.Choices, "|") {
			if asciiEqualFold(choice, value) {
				return true
			}
		}
		if setting.Kind == KindString {
			return false
		}
	}
	var parsed int64
	switch setting.Kind {
	case KindString:
		return true
	case KindInt:
		valuePointer := stringPointer(value)
		if result := parseNonnegativeInt(valuePointer); result != nil {
			parsed = *result
		} else {
			return false
		}
	case KindSize:
		parsed = parseScaled(stringPointer(value), 1024)
		if parsed <= 0 {
			return false
		}
	case KindTokens:
		parsed = parseScaled(stringPointer(value), 1000)
		if parsed <= 0 {
			return false
		}
	case KindDuration:
		parsed = parseDuration(stringPointer(value))
		if parsed < 0 {
			return false
		}
	case KindBool:
		return parseBool(stringPointer(value)) != nil
	}
	return boundsAllow(setting, true, parsed)
}

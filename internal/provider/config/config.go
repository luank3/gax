// Package config resolves the shared providers.<name> configuration vocabulary.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	baseconfig "github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/wiretrace"
)

type FieldClass uint32

const (
	FieldOpenAIChat FieldClass = 1 << iota
	FieldOpenAIResponses
	FieldAnthropic
	FieldUnpinned
	FieldKeyed
	FieldPortTemplated
)

const FieldOpenAI = FieldOpenAIChat | FieldOpenAIResponses

type FieldMetadata struct {
	Name    string
	Classes FieldClass
	Secret  bool
}

var fieldInventory = [...]FieldMetadata{
	{Name: "api", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "base_url", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "port", Classes: FieldPortTemplated},
	{Name: "api_key", Classes: FieldKeyed, Secret: true},
	{Name: "api_key_env", Classes: FieldUnpinned},
	{Name: "display_name", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "catalog_id", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "sort_models", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "metadata_api", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "model_apis", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "cache", Classes: FieldOpenAIChat | FieldAnthropic},
	{Name: "cache_ttl", Classes: FieldOpenAIChat | FieldAnthropic},
	{Name: "send_cache_key", Classes: FieldOpenAI},
	{Name: "request_cost", Classes: FieldOpenAIChat},
	{Name: "reasoning_format", Classes: FieldOpenAIChat},
	{Name: "reasoning_roundtrip", Classes: FieldOpenAIChat},
	{Name: "reasoning_required", Classes: FieldOpenAIChat},
	{Name: "max_tokens", Classes: FieldAnthropic},
	{Name: "thinking_mode", Classes: FieldAnthropic},
	{Name: "thinking_budget", Classes: FieldAnthropic},
	{Name: "version", Classes: FieldAnthropic},
	{Name: "extra_body", Classes: FieldOpenAI | FieldAnthropic},
	{Name: "extra_headers", Classes: FieldOpenAI | FieldAnthropic, Secret: true},
}

var reservedBodyFields = [...]string{
	"model", "stream", "messages", "input", "include", "n", "system", "tools", "stream_options", "instructions",
}

type WarningHandler func(message string)

// Config reads provider values from a shared config store. It is foreground-owned, matching the
// configuration store and upstream provider constructor.
type Config struct {
	store   *baseconfig.Store
	warning WarningHandler
}

func New(store *baseconfig.Store) *Config { return &Config{store: store} }

func (config *Config) SetWarningHandler(handler WarningHandler) {
	if config != nil {
		config.warning = handler
	}
}

func Fields() []FieldMetadata { return append([]FieldMetadata(nil), fieldInventory[:]...) }

func Field(name string) (FieldMetadata, bool) {
	for _, field := range fieldInventory {
		if field.Name == name {
			return field, true
		}
	}
	return FieldMetadata{}, false
}

func (field FieldMetadata) AppliesTo(classes FieldClass) bool { return field.Classes&classes != 0 }

// WarnUnusedFields reports provider block members the selected wire, provider traits, or a
// provider-specific setting do not consume. A mixed-protocol provider treats all wire fields as
// live, and extra contains fields consumed by a metadata hook rather than the request wire.
func (config *Config) WarnUnusedFields(provider, apiLabel string, classes FieldClass, extra []string) {
	if config == nil || config.store == nil || provider == "" {
		return
	}
	prefix := "providers." + provider
	for _, leaf := range config.store.ObjectKeys(prefix) {
		if containsField(extra, leaf) {
			continue
		}
		if field, ok := Field(leaf); ok {
			if field.AppliesTo(classes) {
				continue
			}
			if field.Classes&(FieldOpenAI|FieldAnthropic) != 0 {
				config.warn("provider '%s': field '%s' is not used by %s providers", provider, leaf, apiLabel)
			} else {
				config.warn("provider '%s': field '%s' is not configurable for this provider", provider, leaf)
			}
			continue
		}
		if !registeredSetting(prefix + "." + leaf) {
			config.warn("provider '%s': unknown field '%s' (see docs/providers.md)", provider, leaf)
		}
	}
}

func (config *Config) String(prefix, leaf string) *string {
	if config == nil || config.store == nil || prefix == "" || leaf == "" {
		return nil
	}
	return config.store.String(prefix + "." + leaf)
}

// APIKey resolves an inline key, optional $ENV indirection, and the provider definition's
// environment fallback in upstream order. It never includes a credential in a diagnostic.
func (config *Config) APIKey(prefix, fallbackEnv string) string {
	if value := config.String(prefix, "api_key"); value != nil && *value != "" {
		if resolved, ok := config.resolveEnvEscape(*value); ok && resolved != "" {
			wiretrace.Default().RegisterSecret(resolved)
			return resolved
		}
	}
	if fallbackEnv == "" || config == nil || config.store == nil {
		return ""
	}
	value, ok := config.store.LookupEnv(fallbackEnv)
	if !ok || value == "" {
		return ""
	}
	wiretrace.Default().RegisterSecret(value)
	return value
}

func (config *Config) CacheTTL(prefix string) string {
	value := config.String(prefix, "cache_ttl")
	if value == nil || *value == "" {
		return "1h"
	}
	switch strings.ToLower(*value) {
	case "5m":
		return "5m"
	case "1h":
		return "1h"
	default:
		config.warn("unknown cache_ttl '%s' (5m or 1h) — using 1h", *value)
		return "1h"
	}
}

// ExtraBody returns the configured object with protocol-owned top-level fields removed. Raw
// members preserve their JSON types and content for the request encoder.
func (config *Config) ExtraBody(prefix string) map[string]json.RawMessage {
	if config == nil || config.store == nil || prefix == "" {
		return nil
	}
	key := prefix + ".extra_body"
	data := config.store.JSONNode(key)
	if len(data) == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' || json.Unmarshal(data, &body) != nil || body == nil {
		config.warn("%s must be a JSON object — ignoring it", key)
		return nil
	}
	for _, reserved := range reservedBodyFields {
		if _, exists := body[reserved]; exists {
			config.warn("%s: '%s' is protocol-owned — ignoring it", key, reserved)
			delete(body, reserved)
		}
	}
	return body
}

// HeadersFromObject validates a JSON object of header names and string values. Empty literal
// values are removal markers; unresolved or empty environment indirections are dropped.
func (config *Config) HeadersFromObject(data json.RawMessage) []string {
	return config.headersFromObject(data, "extra_headers")
}

func (config *Config) headersFromObject(data json.RawMessage, label string) []string {
	if len(data) == 0 {
		return nil
	}
	members, err := decodeOrderedObject(data)
	if err != nil {
		config.warn("%s must be a JSON object of name/value members — ignoring it", label)
		return nil
	}
	headers := make([]string, 0, len(members))
	for _, member := range members {
		if !validHeaderName(member.name) {
			config.warn("%s: invalid header name '%s' — ignoring it", label, member.name)
			continue
		}
		if trimmed := bytes.TrimSpace(member.value); len(trimmed) == 0 || trimmed[0] != '"' {
			config.warn("%s: header '%s' needs a string value — ignoring it", label, member.name)
			continue
		}
		var text string
		if json.Unmarshal(member.value, &text) != nil {
			config.warn("%s: header '%s' needs a string value — ignoring it", label, member.name)
			continue
		}
		resolved, ok := config.resolveEnvEscape(text)
		if !ok {
			config.warn("%s: header '%s' dropped — %s is not set", label, member.name, text[1:])
			continue
		}
		if resolved == "" {
			headers = append(headers, member.name+":")
			continue
		}
		if !validHeaderValue(resolved) {
			config.warn("%s: header '%s' needs a control-character-free value — ignoring it", label, member.name)
			continue
		}
		headers = append(headers, member.name+": "+resolved)
	}
	return headers
}

func (config *Config) ExtraHeaderTemplates(prefix string) []string {
	if config == nil || config.store == nil || prefix == "" {
		return nil
	}
	return config.headersFromObject(config.store.JSONNode(prefix+".extra_headers"), prefix+".extra_headers")
}

func (config *Config) ExtraHeaders(prefix, sessionID string) []string {
	return ExpandHeaders(MergeHeaders(nil, config.ExtraHeaderTemplates(prefix)), sessionID)
}

func (config *Config) resolveEnvEscape(value string) (string, bool) {
	if !strings.HasPrefix(value, "$") {
		return value, true
	}
	if strings.HasPrefix(value, "$$") {
		return value[1:], true
	}
	if config == nil || config.store == nil {
		return "", false
	}
	resolved, ok := config.store.LookupEnv(value[1:])
	if ok && resolved != "" {
		wiretrace.Default().RegisterSecret(resolved)
	}
	return resolved, ok && resolved != ""
}

func (config *Config) warn(format string, args ...any) {
	if config != nil && config.warning != nil {
		config.warning(fmt.Sprintf(format, args...))
	}
}

func containsField(fields []string, name string) bool {
	for _, field := range fields {
		if field == name {
			return true
		}
	}
	return false
}

func registeredSetting(key string) bool {
	for _, setting := range baseconfig.DefaultSettings() {
		if setting.Key == key {
			return true
		}
	}
	return false
}

type objectMember struct {
	name  string
	value json.RawMessage
}

func decodeOrderedObject(data []byte) ([]objectMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("root is not an object")
	}
	members := make([]objectMember, 0)
	indices := make(map[string]int)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("object member is not a string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if index, exists := indices[name]; exists {
			members[index].value = append(json.RawMessage(nil), value...)
		} else {
			indices[name] = len(members)
			members = append(members, objectMember{name: name, value: append(json.RawMessage(nil), value...)})
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing data")
		}
		return nil, err
	}
	return members, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		char := name[index]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if !alphaNumeric && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char < ' ' && char != '\t' || char == 0x7f {
			return false
		}
	}
	return true
}

func MergeHeaders(defaults, overrides []string) []string {
	merged := make([]string, 0, len(defaults)+len(overrides))
	for _, header := range defaults {
		if containsHeaderName(overrides, header) || isRemovalHeader(header) {
			continue
		}
		merged = append(merged, header)
	}
	for _, header := range overrides {
		if !isRemovalHeader(header) {
			merged = append(merged, header)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func containsHeaderName(headers []string, target string) bool {
	for _, header := range headers {
		if strings.EqualFold(headerName(header), headerName(target)) {
			return true
		}
	}
	return false
}

func headerName(header string) string {
	if colon := strings.IndexByte(header, ':'); colon >= 0 {
		return header[:colon]
	}
	return header
}

func isRemovalHeader(header string) bool {
	colon := strings.IndexByte(header, ':')
	return colon >= 0 && colon == len(header)-1
}

func ExpandHeaders(headers []string, sessionID string) []string {
	if len(headers) == 0 {
		return nil
	}
	expanded := make([]string, len(headers))
	for index, header := range headers {
		expanded[index] = strings.ReplaceAll(header, "{session_id}", sessionID)
	}
	return expanded
}

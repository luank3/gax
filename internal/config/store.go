package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Source identifies which tier supplied a resolved setting.
type Source string

const (
	SourceOverride Source = "override"
	SourceSession  Source = "session"
	SourceEnv      Source = "env"
	SourceState    Source = "state"
	SourceConfig   Source = "config"
	SourceDefault  Source = "default"
	SourceUnset    Source = "unset"
)

// Preset defines a named provider/model/effort/prompt bundle.
type Preset struct {
	Description        string `json:"description,omitempty"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Effort             string `json:"effort,omitempty"`
	SystemPrompt       string `json:"system_prompt,omitempty"`
	SystemPromptAppend string `json:"system_prompt_append,omitempty"`
	Tint               string `json:"tint,omitempty"`
}

// MCPServerConfig configures one lazy-loaded MCP server.
type MCPServerConfig struct {
	Transport string            `json:"transport"` // "stdio" or "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Timeout   string            `json:"timeout,omitempty"`
}

// Store holds layered configuration, presets, provider blocks, and MCP servers.
type Store struct {
	mu        sync.RWMutex
	overrides map[string]string
	session   map[string]string
	state     map[string]string
	config    map[string]string
	rawConfig map[string]any
	presets   map[string]Preset
	mcp       map[string]MCPServerConfig
	statePath string
	cfgPath   string
}

// NewStore creates an empty Store (useful for tests or defaults-only runs).
func NewStore() *Store {
	return &Store{
		overrides: make(map[string]string),
		session:   make(map[string]string),
		state:     make(map[string]string),
		config:    make(map[string]string),
		rawConfig: make(map[string]any),
		presets:   make(map[string]Preset),
		mcp:       make(map[string]MCPServerConfig),
	}
}

// Load reads config.json and state.json from configDir and stateDir.
// Missing files are ignored; malformed JSON returns an error.
func Load(configDir, stateDir string) (*Store, error) {
	s := NewStore()
	if configDir != "" {
		s.cfgPath = filepath.Join(configDir, "config.json")
		if err := s.loadConfigFile(s.cfgPath); err != nil {
			return nil, err
		}
	}
	if stateDir != "" {
		s.statePath = filepath.Join(stateDir, "state.json")
		if err := s.loadStateFile(s.statePath); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) loadConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %q: %w", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	s.rawConfig = root
	flattenScalars("", root, s.config)

	if rawPresets, ok := root["presets"]; ok {
		if b, err := json.Marshal(rawPresets); err == nil {
			_ = json.Unmarshal(b, &s.presets)
		}
	}
	if rawMCP, ok := root["mcp"].(map[string]any); ok {
		if rawServers, ok := rawMCP["servers"]; ok {
			if b, err := json.Marshal(rawServers); err == nil {
				_ = json.Unmarshal(b, &s.mcp)
			}
		}
	}
	return nil
}

func (s *Store) loadStateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read state %q: %w", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse state %q: %w", path, err)
	}
	flattenScalars("", root, s.state)
	return nil
}

func flattenScalars(prefix string, obj map[string]any, dst map[string]string) {
	for k, v := range obj {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenScalars(full, val, dst)
		case string:
			dst[full] = val
		case bool:
			if val {
				dst[full] = "true"
			} else {
				dst[full] = "false"
			}
		case float64:
			dst[full] = strconv.FormatFloat(val, 'f', -1, 64)
		}
	}
}

// SetOverride sets a process-local override (e.g. CLI flag or /config).
func (s *Store) SetOverride(key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if val == DefaultSentinel {
		delete(s.overrides, key)
		return
	}
	s.overrides[key] = val
}

// SetSession sets a resumed-conversation selection tier value.
func (s *Store) SetSession(key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if val == "" {
		delete(s.session, key)
		return
	}
	s.session[key] = val
}

// Resolve returns the effective value and winning source for key.
func (s *Store) Resolve(key string) (string, Source) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if v, ok := s.overrides[key]; ok {
		return v, SourceOverride
	}
	if v, ok := s.session[key]; ok {
		return v, SourceSession
	}
	def, hasDef := LookupSetting(key)
	if hasDef && def.EnvVar != "" {
		if v, ok := os.LookupEnv(def.EnvVar); ok {
			if v == DefaultSentinel {
				if def.Default != "" {
					return def.Default, SourceDefault
				}
				return "", SourceUnset
			}
			return v, SourceEnv
		}
	}
	if v, ok := s.state[key]; ok {
		return v, SourceState
	}
	if v, ok := s.config[key]; ok {
		return v, SourceConfig
	}
	if hasDef && def.Default != "" {
		return def.Default, SourceDefault
	}
	return "", SourceUnset
}

// String returns the resolved string value (empty if unset).
func (s *Store) String(key string) string {
	v, _ := s.Resolve(key)
	return v
}

// Bool returns the resolved boolean value, falling back to the setting default.
func (s *Store) Bool(key string) bool {
	v, _ := s.Resolve(key)
	if b, ok := ParseBool(v); ok {
		return b
	}
	if def, ok := LookupSetting(key); ok {
		if b, ok := ParseBool(def.Default); ok {
			return b
		}
	}
	return false
}

// Int returns the resolved integer value, falling back to the setting default.
func (s *Store) Int(key string) int64 {
	v, _ := s.Resolve(key)
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
		return n
	}
	if def, ok := LookupSetting(key); ok {
		if n, err := strconv.ParseInt(def.Default, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// Duration returns the resolved duration, falling back to the setting default.
func (s *Store) Duration(key string) time.Duration {
	v, _ := s.Resolve(key)
	if d, err := ParseDuration(v); err == nil {
		return d
	}
	if def, ok := LookupSetting(key); ok {
		if d, err := ParseDuration(def.Default); err == nil {
			return d
		}
	}
	return 0
}

// Bytes returns the resolved byte count, falling back to the setting default.
func (s *Store) Bytes(key string) int64 {
	v, _ := s.Resolve(key)
	if n, err := ParseBytes(v); err == nil {
		return n
	}
	if def, ok := LookupSetting(key); ok {
		if n, err := ParseBytes(def.Default); err == nil {
			return n
		}
	}
	return 0
}

// Presets returns a copy of configured presets.
func (s *Store) Presets() map[string]Preset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Preset, len(s.presets))
	for k, v := range s.presets {
		out[k] = v
	}
	return out
}

// MCPServers returns a copy of configured MCP servers.
func (s *Store) MCPServers() map[string]MCPServerConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]MCPServerConfig, len(s.mcp))
	for k, v := range s.mcp {
		out[k] = v
	}
	return out
}

// RawSection returns a top-level object from config.json (e.g., "providers", "catalog").
func (s *Store) RawSection(name string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.rawConfig[name].(map[string]any); ok {
		return m
	}
	return nil
}

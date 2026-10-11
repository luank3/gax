# Configuration and Provider Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port hax's configuration resolution and provider registry substrate into Go so later agent and CLI layers can use the same provider selection behavior.

**Architecture:** Keep JSON configuration tiers and typed setting metadata in `internal/config`. Put environment aliases and provider-field resolution in `internal/provider/config`; keep immutable shipped definitions and dynamic user-defined entries in `internal/provider/registry`. Provider construction remains a later task that consumes these values and the already implemented wire/transport packages.

**Tech Stack:** Go 1.25 standard library, `encoding/json`, `os`, and `testing`; no third-party dependencies.

**Spec:** `/tmp/luank3-hax-upstream/docs/configuration.md`, `/tmp/luank3-hax-upstream/docs/providers.md`, and upstream `src/config.c`, `src/providers/provider_config.c`, and `src/providers/registry.c` at commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

## Global Constraints

- Resolve scalar settings in this order: run override, conversation, environment, state file, config file, registry default.
- `(default)` stops lookup at that tier and resolves to the setting's registry default.
- JSON accepts both nested objects and flat dotted keys; numeric and boolean scalar values coerce to strings for scalar getters.
- `model` and `effort` values stored with a provider selection apply only while that provider is active, with `llama.cpp` canonicalized to `llamacpp`.
- Shipped providers retain upstream autoselect order; internal providers stay addressable by name but are excluded from enumeration.
- Provider credentials and header values must not be written to diagnostics or trace output.
- Preserve the full project objective; this plan implements only the configuration and registry subsystem.

## Review Focus

- Empty environment values either fall through or remain meaningful according to setting metadata; test both.
- Invalid JSON and a non-object root return an error and leave that tier empty; test both.
- State/config values for `model` and `effort` must not leak across provider switches; test provider-bound selection resolution.
- Provider aliases and custom config-only names must resolve without changing shipped priority; test canonical aliases and custom registration.
- Empty, missing, or invalid credentials must produce a deterministic availability reason without exposing secret values; test key resolution and status.

---

### Task 1: Configuration store and resolution

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/settings.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces `type Store`, `type Setting`, `func New(settings []Setting) *Store`, `func (s *Store) LoadConfig([]byte) error`, `func (s *Store) LoadState([]byte) error`, `func (s *Store) SetOverride(key string, value *string)`, `func (s *Store) SetConversation(key string, value *string)`, `func (s *Store) String(key string) *string`, `func (s *Store) Source(key string) string`, `func (s *Store) Int(key string) int`, `func (s *Store) Bool(key string) bool`, `func (s *Store) Size(key string) int64`, `func (s *Store) Tokens(key string) int64`, and `func (s *Store) Duration(key string) time.Duration`.
- Config and state loads preserve raw JSON nodes while scalar and object access support both nested objects and flat dotted keys.

- [x] **Step 1: Write tests** for precedence, dotted and nested JSON, scalar coercion, default sentinel, provider-bound model/effort, meaningful empty values, invalid-load clearing, and typed parsing/bounds.
- [x] **Step 2: Run** `go test ./internal/config` and confirm the absent API fails to compile.
- [x] **Step 3: Implement** the store and all 64 ordered upstream setting entries.
- [x] **Step 4: Run** `go test ./internal/config` and `go test ./...`.

### Task 2: Provider configuration and credentials

**Files:**
- Create: `internal/provider/config/config.go`
- Test: `internal/provider/config/config_test.go`

**Interfaces:**
- Consumes `config.Store` from Task 1.
- Produces provider-scoped field reads, shipped environment aliases, API-key resolution, cache TTL normalization, extra-body filtering, and validated extra-header expansion with `{session_id}` replacement.

- [x] **Step 1: Write tests** for provider env aliases, `$ENV` indirection, `$$` escaping, header-name/value validation, default-header overrides, and removal markers.
- [x] **Step 2: Run** `go test ./internal/provider/config` and confirm the missing package/API fails.
- [x] **Step 3: Implement** provider field metadata and helpers without importing a concrete wire implementation.
- [x] **Step 4: Run** `go test ./internal/provider/config` and `go test ./...`.

### Task 3: Shipped and user-defined provider registry

**Files:**
- Create: `internal/provider/registry/registry.go`
- Test: `internal/provider/registry/registry_test.go`

**Interfaces:**
- Consumes provider-scoped config from Task 2.
- Produces `type Definition`, `type Registry`, `func New(config *config.Store) *Registry`, `func (r *Registry) Find(name string) (*Definition, bool)`, `func (r *Registry) All() []*Definition`, `func (r *Registry) Default() *Definition`, and `func (r *Registry) DisplayName(def *Definition) string`.
- Register the upstream shipped IDs, defaults, canonical names, internal visibility, and autoselect priority; config-only provider names append after shipped user-facing entries.

- [x] **Step 1: Write tests** for exact shipped order/default, `llama.cpp` alias lookup, internal provider exclusion, and custom config-only registration.
- [x] **Step 2: Run** `go test ./internal/provider/registry` and confirm the missing API fails.
- [x] **Step 3: Implement** shipped definitions plus config-derived definitions.
- [x] **Step 4: Run** `go test ./internal/provider/registry` and `go test ./...`.


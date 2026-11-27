// Package config implements gax's layered settings registry, config.json /
// state.json loading, preset definitions, and provider/MCP config blocks.
package config

// ValueKind identifies how a setting's string value is parsed and validated.
type ValueKind uint8

const (
	KindString ValueKind = iota
	KindBool
	KindInt
	KindDuration
	KindBytes
	KindTokens
)

// DefaultSentinel clears a process override or stops layered lookup at that tier.
const DefaultSentinel = "(default)"

// SettingDef describes one registered configuration key.
type SettingDef struct {
	Key         string
	EnvVar      string
	Default     string
	Description string
	Kind        ValueKind
	Min         int64
	Max         int64
	Runtime     bool // editable via /config at runtime
}

// Registry lists every user-facing setting in display order.
var Registry = []SettingDef{
	// Selection and context
	{Key: "preset", EnvVar: "GAX_PRESET", Description: "Startup preset name", Kind: KindString},
	{Key: "provider", EnvVar: "GAX_PROVIDER", Default: "auto", Description: "Provider id", Kind: KindString},
	{Key: "model", EnvVar: "GAX_MODEL", Description: "Provider-specific model id", Kind: KindString},
	{Key: "effort", EnvVar: "GAX_EFFORT", Description: "Reasoning effort level", Kind: KindString},
	{Key: "system_prompt", EnvVar: "GAX_SYSTEM_PROMPT", Description: "Replace built-in base prompt (@path or (none))", Kind: KindString},
	{Key: "system_prompt_append", EnvVar: "GAX_SYSTEM_PROMPT_APPEND", Description: "Append text or @path to base prompt", Kind: KindString},
	{Key: "no_env", EnvVar: "GAX_NO_ENV", Default: "off", Description: "Omit Environment section", Kind: KindBool},
	{Key: "no_agents_md", EnvVar: "GAX_NO_AGENTS_MD", Default: "off", Description: "Omit discovered AGENTS.md files", Kind: KindBool},
	{Key: "no_skills", EnvVar: "GAX_NO_SKILLS", Default: "off", Description: "Omit skill descriptions and skill tool", Kind: KindBool},
	{Key: "no_subagents", EnvVar: "GAX_NO_SUBAGENTS", Default: "off", Description: "Omit subagent delegation guidance", Kind: KindBool},
	{Key: "no_tasks", EnvVar: "GAX_NO_TASKS", Default: "off", Description: "Disable managed background tasks", Kind: KindBool},
	{Key: "no_web", EnvVar: "GAX_NO_WEB", Default: "off", Description: "Disable web_fetch and web_search tools", Kind: KindBool},

	// Display and behavior
	{Key: "markdown", EnvVar: "GAX_MARKDOWN", Default: "on", Description: "Render Markdown on terminal output", Kind: KindBool, Runtime: true},
	{Key: "show_reasoning", EnvVar: "GAX_SHOW_REASONING", Default: "off", Description: "Display reasoning emitted by provider", Kind: KindBool, Runtime: true},
	{Key: "sort_models", EnvVar: "GAX_SORT_MODELS", Default: "auto", Description: "Sort model picker newest-first (auto/on/off)", Kind: KindString, Runtime: true},
	{Key: "context_limit", EnvVar: "GAX_CONTEXT_LIMIT", Default: "auto", Description: "Override model context-window token limit", Kind: KindTokens, Runtime: true},
	{Key: "display_width", EnvVar: "GAX_DISPLAY_WIDTH", Default: "auto", Description: "Render width (auto, terminal, or >= 20)", Kind: KindString, Runtime: true},
	{Key: "notify", EnvVar: "GAX_NOTIFY", Default: "auto", Description: "Terminal attention notification (auto/bel/osc9/off)", Kind: KindString, Runtime: true},
	{Key: "program_status", EnvVar: "GAX_PROGRAM_STATUS", Default: "auto", Description: "Emit OSC 7501 program status reports", Kind: KindString, Runtime: true},
	{Key: "theme", EnvVar: "GAX_THEME", Default: "auto", Description: "Color theme (auto/dark/light/ansi/off)", Kind: KindString, Runtime: true},
	{Key: "tint", EnvVar: "GAX_TINT", Default: "teal", Description: "Model-output tint (teal/violet/rose/sage)", Kind: KindString, Runtime: true},
	{Key: "keep_awake", EnvVar: "GAX_KEEP_AWAKE", Default: "on", Description: "Inhibit idle sleep while a turn runs", Kind: KindBool, Runtime: true},
	{Key: "compact.auto", EnvVar: "GAX_COMPACT_AUTO", Default: "on", Description: "Automatically compact near context limit", Kind: KindBool, Runtime: true},
	{Key: "compact.threshold", EnvVar: "GAX_COMPACT_THRESHOLD", Default: "85", Description: "Context percentage triggering compaction", Kind: KindInt, Min: 10, Max: 99, Runtime: true},
	{Key: "max_turns", EnvVar: "GAX_MAX_TURNS", Default: "0", Description: "Max model round-trips per user turn (0 = unlimited)", Kind: KindInt, Min: 0, Max: 10000, Runtime: true},

	// Recording
	{Key: "no_session", EnvVar: "GAX_NO_SESSION", Default: "auto", Description: "Skip session and history writes", Kind: KindString},
	{Key: "session_retention_days", EnvVar: "GAX_SESSION_RETENTION_DAYS", Default: "30", Description: "Prune inactive sessions after N days (0 = keep)", Kind: KindInt, Min: 0, Max: 36500},
	{Key: "transcript", EnvVar: "GAX_TRANSCRIPT", Description: "Mirror model-facing transcript to file", Kind: KindString},
	{Key: "trace", EnvVar: "GAX_TRACE", Description: "Write redacted HTTP/SSE trace to file", Kind: KindString},

	// Model metadata
	{Key: "catalog.url", EnvVar: "GAX_CATALOG_URL", Default: "https://models.dev/api.json", Description: "Model metadata catalog URL", Kind: KindString},
	{Key: "catalog.refresh", EnvVar: "GAX_CATALOG_REFRESH", Default: "24h", Description: "Catalog cache refresh interval", Kind: KindDuration},

	// Tools and transport
	{Key: "image_input", EnvVar: "GAX_IMAGE_INPUT", Default: "auto", Description: "Image input support (auto/on/off)", Kind: KindString, Runtime: true},
	{Key: "tool_output_cap", EnvVar: "GAX_TOOL_OUTPUT_CAP", Default: "50k", Description: "Max tool-output bytes retained for model", Kind: KindBytes, Min: 1024, Runtime: true},
	{Key: "bash.timeout", EnvVar: "GAX_BASH_TIMEOUT", Default: "2m", Description: "Default bash timeout before background detach", Kind: KindDuration, Runtime: true},
	{Key: "bash.timeout_max", EnvVar: "GAX_BASH_TIMEOUT_MAX", Default: "30m", Description: "Max timeout model may request", Kind: KindDuration, Runtime: true},
	{Key: "bash.timeout_grace", EnvVar: "GAX_BASH_TIMEOUT_GRACE", Default: "2s", Description: "Grace period between SIGTERM and SIGKILL", Kind: KindDuration, Runtime: true},
	{Key: "bash.background_yield", EnvVar: "GAX_BASH_BACKGROUND_YIELD", Default: "5s", Description: "Initial output window for explicit backgrounding", Kind: KindDuration, Runtime: true},
	{Key: "bash.shell", EnvVar: "GAX_BASH_SHELL", Description: "Shell executable for bash tool", Kind: KindString},
	{Key: "bash.compress", EnvVar: "GAX_BASH_COMPRESS", Default: "on", Description: "Compress recognized shell command output", Kind: KindBool, Runtime: true},
	{Key: "task.wait_timeout", EnvVar: "GAX_TASK_WAIT_TIMEOUT", Default: "10m", Description: "Default wait timeout for task_wait", Kind: KindDuration, Runtime: true},
	{Key: "task.max_running", EnvVar: "GAX_TASK_MAX_RUNNING", Default: "32", Description: "Max concurrent background tasks", Kind: KindInt, Min: 1, Max: 64, Runtime: true},
	{Key: "http.max_retries", EnvVar: "GAX_HTTP_MAX_RETRIES", Default: "4", Description: "Additional retries for transient HTTP errors", Kind: KindInt, Min: 0, Max: 20},
	{Key: "http.retry_base", EnvVar: "GAX_HTTP_RETRY_BASE", Default: "1s", Description: "Initial retry backoff", Kind: KindDuration},
	{Key: "http.idle_timeout", EnvVar: "GAX_HTTP_IDLE_TIMEOUT", Default: "10m", Description: "Streaming silence timeout (0 = disabled)", Kind: KindDuration},

	// Web tools
	{Key: "web.search_provider", EnvVar: "GAX_WEB_SEARCH_PROVIDER", Default: "auto", Description: "Search backend (auto/you/brave/octen/exa/firecrawl/tinyfish/keenable/duckduckgo)", Kind: KindString, Runtime: true},
	{Key: "web.timeout", EnvVar: "GAX_WEB_TIMEOUT", Default: "30s", Description: "HTTP timeout for web_fetch and web_search", Kind: KindDuration, Runtime: true},
	{Key: "web.max_bytes", EnvVar: "GAX_WEB_MAX_BYTES", Default: "2m", Description: "Max response bytes read by web_fetch", Kind: KindBytes, Min: 1024, Runtime: true},
	{Key: "web.you_api_key", EnvVar: "YDC_API_KEY", Description: "You.com API key", Kind: KindString},
	{Key: "web.brave_api_key", EnvVar: "BRAVE_API_KEY", Description: "Brave Search API key", Kind: KindString},
	{Key: "web.octen_api_key", EnvVar: "OCTEN_API_KEY", Description: "Octen API key", Kind: KindString},
	{Key: "web.exa_api_key", EnvVar: "EXA_API_KEY", Description: "Exa API key", Kind: KindString},
	{Key: "web.firecrawl_api_key", EnvVar: "FIRECRAWL_API_KEY", Description: "Firecrawl API key", Kind: KindString},
	{Key: "web.tinyfish_api_key", EnvVar: "TINYFISH_API_KEY", Description: "TinyFish API key", Kind: KindString},
	{Key: "web.keenable_api_key", EnvVar: "KEENABLE_API_KEY", Description: "Keenable API key", Kind: KindString},

	// Provider-specific settings
	{Key: "providers.llamacpp.base_url", EnvVar: "GAX_LLAMACPP_BASE_URL", Description: "Full llama-server base URL", Kind: KindString},
	{Key: "providers.llamacpp.api_key", EnvVar: "GAX_LLAMACPP_API_KEY", Description: "Bearer token for llama-server", Kind: KindString},
	{Key: "providers.llamacpp.port", EnvVar: "GAX_LLAMACPP_PORT", Default: "9931", Description: "llama-server port when base_url is unset", Kind: KindInt, Min: 1, Max: 65535},
	{Key: "providers.mock.script", EnvVar: "GAX_MOCK_SCRIPT", Description: "Mock provider script path", Kind: KindString},

	{Key: "providers.openai-compatible.base_url", EnvVar: "GAX_OPENAI_BASE_URL", Description: "OpenAI-compatible base URL", Kind: KindString},
	{Key: "providers.openai-compatible.api_key", EnvVar: "GAX_OPENAI_API_KEY", Description: "OpenAI-compatible bearer token", Kind: KindString},
	{Key: "providers.openai-compatible.display_name", EnvVar: "GAX_OPENAI_DISPLAY_NAME", Description: "OpenAI-compatible display name", Kind: KindString},
	{Key: "providers.openai-compatible.api", EnvVar: "GAX_OPENAI_API", Default: "chat", Description: "Dialect: chat or responses", Kind: KindString},
	{Key: "providers.openai-compatible.reasoning_format", EnvVar: "GAX_OPENAI_REASONING_FORMAT", Default: "flat", Description: "Effort shape: flat or nested", Kind: KindString},
	{Key: "providers.openai-compatible.reasoning_roundtrip", EnvVar: "GAX_OPENAI_REASONING_ROUNDTRIP", Default: "auto", Description: "Replay reasoning: auto, off, or field name", Kind: KindString},
	{Key: "providers.openai-compatible.reasoning_required", EnvVar: "GAX_OPENAI_REASONING_REQUIRED", Default: "auto", Description: "Always send reasoning field (auto/on/off)", Kind: KindString},
	{Key: "providers.openai-compatible.send_cache_key", EnvVar: "GAX_OPENAI_SEND_CACHE_KEY", Default: "auto", Description: "Send prompt cache key", Kind: KindString},
	{Key: "providers.openai-compatible.request_cost", EnvVar: "GAX_OPENAI_REQUEST_COST", Default: "auto", Description: "Request per-response cost", Kind: KindString},
	{Key: "providers.openai-compatible.cache", EnvVar: "GAX_OPENAI_CACHE", Default: "auto", Description: "Send cache breakpoints", Kind: KindString},
	{Key: "providers.openai-compatible.cache_ttl", EnvVar: "GAX_OPENAI_CACHE_TTL", Default: "1h", Description: "Cache TTL (5m or 1h)", Kind: KindString},

	{Key: "providers.anthropic-compatible.base_url", EnvVar: "GAX_ANTHROPIC_BASE_URL", Description: "Anthropic-compatible base URL", Kind: KindString},
	{Key: "providers.anthropic-compatible.api_key", EnvVar: "GAX_ANTHROPIC_API_KEY", Description: "Anthropic-compatible x-api-key", Kind: KindString},
	{Key: "providers.anthropic-compatible.display_name", EnvVar: "GAX_ANTHROPIC_DISPLAY_NAME", Description: "Anthropic-compatible display name", Kind: KindString},
	{Key: "providers.anthropic-compatible.max_tokens", EnvVar: "GAX_ANTHROPIC_MAX_TOKENS", Description: "Max output tokens including thinking", Kind: KindInt, Min: 1},
	{Key: "providers.anthropic-compatible.thinking_mode", EnvVar: "GAX_ANTHROPIC_THINKING_MODE", Default: "auto", Description: "Thinking mode (auto/prefer-adaptive/adaptive/budget/off)", Kind: KindString},
	{Key: "providers.anthropic-compatible.thinking_budget", EnvVar: "GAX_ANTHROPIC_THINKING_BUDGET", Description: "Budget-mode thinking tokens", Kind: KindInt, Min: 1},
	{Key: "providers.anthropic-compatible.cache", EnvVar: "GAX_ANTHROPIC_CACHE", Default: "auto", Description: "Send cache breakpoints", Kind: KindString},
	{Key: "providers.anthropic-compatible.cache_ttl", EnvVar: "GAX_ANTHROPIC_CACHE_TTL", Default: "1h", Description: "Cache TTL (5m or 1h)", Kind: KindString},
	{Key: "providers.anthropic-compatible.version", EnvVar: "GAX_ANTHROPIC_VERSION", Default: "2023-06-01", Description: "anthropic-version header", Kind: KindString},
}

var registryByKey = func() map[string]SettingDef {
	m := make(map[string]SettingDef, len(Registry))
	for _, d := range Registry {
		m[d.Key] = d
	}
	return m
}()

// LookupSetting returns the registered definition for key, if any.
func LookupSetting(key string) (SettingDef, bool) {
	d, ok := registryByKey[key]
	return d, ok
}

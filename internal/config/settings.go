package config

// DefaultSettings returns the ordered setting registry. The order is user-visible in /config.
func DefaultSettings() []Setting {
	return []Setting{
		{Key: "preset", EnvVar: "GAX_PRESET", Description: "Preset from presets.<name> to apply at startup; empty disables", KeepEmpty: true},
		{Key: "provider", EnvVar: "GAX_PROVIDER", Description: "Provider id; /provider shows the available choices", KeepEmpty: true},
		{Key: "model", EnvVar: "GAX_MODEL", Description: "Model id (provider-specific; some auto-fill or require it)", KeepEmpty: true},
		{Key: "effort", EnvVar: "GAX_EFFORT", Description: "Reasoning effort (provider-specific); empty omits it", KeepEmpty: true},
		{Key: "system_prompt", EnvVar: "GAX_SYSTEM_PROMPT", Description: "Replace the built-in base prompt (context sections still follow); @path reads a file; (none) sends no system message at all", KeepEmpty: true},
		{Key: "system_prompt_append", EnvVar: "GAX_SYSTEM_PROMPT_APPEND", Description: "Text appended after the base system prompt; @path reads a file", KeepEmpty: true},
		{Key: "no_env", EnvVar: "GAX_NO_ENV", Description: "Skip the Environment section in the system prompt", Choices: "on|off"},
		{Key: "no_agents_md", EnvVar: "GAX_NO_AGENTS_MD", Description: "Skip AGENTS.md project instructions in the system prompt", Choices: "on|off"},
		{Key: "no_skills", EnvVar: "GAX_NO_SKILLS", Description: "Skip the skills listing in the system prompt", Choices: "on|off"},
		{Key: "no_subagents", EnvVar: "GAX_NO_SUBAGENTS", Description: "Skip the subagents section in the system prompt", Choices: "on|off"},
		{Key: "no_tasks", EnvVar: "GAX_NO_TASKS", Description: "Disable background tasks: bash timeouts kill instead of detaching, and the task tools are not offered", Choices: "on|off"},

		{Key: "markdown", EnvVar: "GAX_MARKDOWN", Default: "1", Description: "Render Markdown in the terminal (TTY only; piped output is always raw)", Choices: "on|off", Editable: true},
		{Key: "show_reasoning", EnvVar: "GAX_SHOW_REASONING", Description: "Show reasoning/CoT deltas live (default off)", Choices: "on|off", Editable: true},
		{Key: "sort_models", EnvVar: "GAX_SORT_MODELS", Default: "auto", Description: "Sort the /model picker newest-first; auto uses the provider's own default", Choices: "auto|on|off", Editable: true},
		{Key: "context_limit", EnvVar: "GAX_CONTEXT_LIMIT", Description: "Manual context-window size for the % display; overrides auto-detect", Kind: KindTokens, Editable: true},
		{Key: "display_width", EnvVar: "GAX_DISPLAY_WIDTH", Default: "auto", Description: "Content width: auto uses full width through 110 columns and 100 beyond that; terminal always uses full width; a number sets an exact width", Choices: "auto|terminal", Example: "100", Kind: KindInt, Min: 20, Editable: true},
		{Key: "notify", EnvVar: "GAX_NOTIFY", Default: "auto", Description: "Desktop-notification style: auto, bel, osc9, off (auto detects from the terminal)", Choices: "auto|bel|osc9|off", Editable: true},
		{Key: "theme", EnvVar: "GAX_THEME", Default: "auto", Description: "Color theme: auto, dark, light, ansi, off (auto detects from the terminal)", Choices: "auto|dark|light|ansi|off", Editable: true},
		{Key: "tint", EnvVar: "GAX_TINT", Default: "teal", Description: "Identity tint for model output; an active preset's own tint wins until set here. Ignored by the ansi and off themes", Choices: "teal|violet|rose|sage", Editable: true},

		{Key: "keep_awake", EnvVar: "GAX_KEEP_AWAKE", Default: "1", Description: "Inhibit idle system sleep while a turn is running (display may still blank)", Choices: "on|off", Editable: true},
		{Key: "compact.auto", EnvVar: "GAX_COMPACT_AUTO", Default: "1", Description: "Auto-summarize the conversation when it nears the context window (manual /compact still works)", Choices: "on|off", Editable: true},
		{Key: "compact.threshold", EnvVar: "GAX_COMPACT_THRESHOLD", Default: "85", Description: "Auto-compact when context usage reaches this percent of the window", Kind: KindInt, Min: 1, Max: 100, Editable: true},
		{Key: "max_turns", EnvVar: "GAX_MAX_TURNS", Default: "0", Description: "Model round-trips per user turn before interactive pauses for confirmation and one-shot aborts; 0 is unlimited", Kind: KindInt, Editable: true},

		{Key: "catalog.url", EnvVar: "GAX_CATALOG_URL", Default: "https://models.dev/api.json", Description: "Model-metadata catalog endpoint (models.dev api.json shape); empty disables fetching", KeepEmpty: true},
		{Key: "catalog.refresh", EnvVar: "GAX_CATALOG_REFRESH", Default: "24h", Description: "Re-fetch the cached model catalog when older than this; 0 disables fetching", Kind: KindDuration},

		{Key: "no_session", EnvVar: "GAX_NO_SESSION", Default: "auto", Description: "Skip recording conversations and typed prompts; auto skips both for dev providers (mock)", Choices: "auto|on|off"},
		{Key: "session_retention_days", EnvVar: "GAX_SESSION_RETENTION_DAYS", Default: "30", Description: "Delete sessions after this many inactive days; 0 disables pruning", Kind: KindInt, Max: 36500},
		{Key: "transcript", EnvVar: "GAX_TRANSCRIPT", Description: "Path to mirror the Ctrl-T transcript view; empty disables", KeepEmpty: true},
		{Key: "trace", EnvVar: "GAX_TRACE", Description: "Path to a wire-level HTTP/SSE trace dump; empty disables", KeepEmpty: true},

		{Key: "image_input", EnvVar: "GAX_IMAGE_INPUT", Default: "auto", Description: "Let the model view images via the read tool; auto detects per provider/model", Choices: "auto|on|off", Editable: true},
		{Key: "tool_output_cap", EnvVar: "GAX_TOOL_OUTPUT_CAP", Default: "50k", Description: "Max bytes captured from a tool's output", Kind: KindSize, Editable: true},
		{Key: "bash.timeout", EnvVar: "GAX_BASH_TIMEOUT", Default: "2m", Description: "Default bash-tool command timeout: the command detaches into a background task (kills when tasks are disabled); 0 disables", Kind: KindDuration, Editable: true},
		{Key: "bash.timeout_max", EnvVar: "GAX_BASH_TIMEOUT_MAX", Default: "30m", Description: "Ceiling on the model's per-call bash timeout; 0 disables", Kind: KindDuration, Editable: true},
		{Key: "bash.timeout_grace", EnvVar: "GAX_BASH_TIMEOUT_GRACE", Default: "2s", Description: "Grace window between SIGTERM and SIGKILL for bash commands; 0 skips", Kind: KindDuration, Max: 300000, Editable: true},
		{Key: "bash.background_yield", EnvVar: "GAX_BASH_BACKGROUND_YIELD", Default: "5s", Description: "Initial output window before an explicitly backgrounded bash command detaches into a task", Kind: KindDuration, Editable: true},
		{Key: "bash.shell", EnvVar: "GAX_BASH_SHELL", Description: "Shell for the bash tool, a $PATH name or path (default: bash, else sh)"},
		{Key: "task.wait_timeout", EnvVar: "GAX_TASK_WAIT_TIMEOUT", Default: "10m", Description: "Default task_wait timeout when the model omits one (kill waits default to immediate)", Kind: KindDuration, Editable: true},
		{Key: "task.max_running", EnvVar: "GAX_TASK_MAX_RUNNING", Default: "32", Description: "Maximum concurrently running background tasks", Kind: KindInt, Min: 1, Max: 64, Editable: true},

		{Key: "http.max_retries", EnvVar: "GAX_HTTP_MAX_RETRIES", Default: "4", Description: "Additional retries for transient HTTP failures", Kind: KindInt, Max: 100, Editable: true},
		{Key: "http.retry_base", EnvVar: "GAX_HTTP_RETRY_BASE", Default: "1s", Description: "Base backoff between HTTP retries", Kind: KindDuration, Min: 1, Editable: true},
		{Key: "http.idle_timeout", EnvVar: "GAX_HTTP_IDLE_TIMEOUT", Default: "10m", Description: "Silence on a streaming response before giving up; 0 disables", Kind: KindDuration, Editable: true},

		{Key: "providers.openai-compatible.base_url", EnvVar: "GAX_OPENAI_BASE_URL", Description: "Base URL of the OpenAI-compatible endpoint"},
		{Key: "providers.openai-compatible.api_key", EnvVar: "GAX_OPENAI_API_KEY", Description: "Bearer token for the OpenAI-compatible endpoint", Secret: true},
		{Key: "providers.openai-compatible.display_name", EnvVar: "GAX_OPENAI_DISPLAY_NAME", Description: "Display name for the provider in the banner and picker"},
		{Key: "providers.openai-compatible.api", EnvVar: "GAX_OPENAI_API", Description: "Request protocol: chat (Chat Completions) or responses", Choices: "chat|responses"},
		{Key: "providers.openai-compatible.reasoning_format", EnvVar: "GAX_OPENAI_REASONING_FORMAT", Description: "Reasoning request dialect: flat or nested", Choices: "flat|nested"},
		{Key: "providers.openai-compatible.reasoning_roundtrip", EnvVar: "GAX_REASONING_ROUNDTRIP", Description: "Replay reasoning text to the model (off/on, or a field name)", KeepEmpty: true},
		{Key: "providers.openai-compatible.send_cache_key", EnvVar: "GAX_OPENAI_SEND_CACHE_KEY", Description: "Send a stable prompt_cache_key (prefix-cache hint); auto uses the provider default", Choices: "auto|on|off"},
		{Key: "providers.openai-compatible.request_cost", EnvVar: "GAX_OPENAI_REQUEST_COST", Description: "Request usage accounting (`usage: {include: true}`) for per-response cost; auto uses the provider default", Choices: "auto|on|off"},
		{Key: "providers.openai-compatible.cache", EnvVar: "GAX_OPENAI_CACHE", Description: "Send prompt cache_control breakpoints (routers fronting Anthropic models, which cache only on request); auto uses the provider default", Choices: "auto|on|off"},
		{Key: "providers.openai-compatible.cache_ttl", EnvVar: "GAX_OPENAI_CACHE_TTL", Description: "Cache breakpoint TTL: 5m or 1h (default 1h, suiting an interactive agent's pauses)", Choices: "5m|1h"},

		{Key: "providers.anthropic-compatible.base_url", EnvVar: "GAX_ANTHROPIC_BASE_URL", Description: "Base URL of the Anthropic-compatible /v1 endpoint"},
		{Key: "providers.anthropic-compatible.api_key", EnvVar: "GAX_ANTHROPIC_API_KEY", Description: "x-api-key token for the Anthropic-compatible endpoint", Secret: true},
		{Key: "providers.anthropic-compatible.display_name", EnvVar: "GAX_ANTHROPIC_DISPLAY_NAME", Description: "Display name for the provider in the banner and picker"},
		{Key: "providers.anthropic-compatible.max_tokens", EnvVar: "GAX_ANTHROPIC_MAX_TOKENS", Description: "Max output tokens (thinking + text) per response; unset follows the model's own cap", Kind: KindInt, Min: 1},
		{Key: "providers.anthropic-compatible.thinking_mode", EnvVar: "GAX_ANTHROPIC_THINKING_MODE", Description: "Thinking mode: auto follows model metadata, prefer-adaptive also assumes adaptive for unlisted models; adaptive, budget, and off pin", Choices: "auto|prefer-adaptive|adaptive|budget|off"},
		{Key: "providers.anthropic-compatible.thinking_budget", EnvVar: "GAX_ANTHROPIC_THINKING_BUDGET", Description: "Budget-mode thinking tokens (default: max_tokens - 1)", Kind: KindInt, Min: 1},
		{Key: "providers.anthropic-compatible.cache", EnvVar: "GAX_ANTHROPIC_CACHE", Description: "Send prompt cache_control breakpoints; off only for endpoints that reject them", Choices: "auto|on|off"},
		{Key: "providers.anthropic-compatible.cache_ttl", EnvVar: "GAX_ANTHROPIC_CACHE_TTL", Description: "Cache breakpoint TTL: 5m or 1h (default 1h, suiting an interactive agent's pauses)", Choices: "5m|1h"},
		{Key: "providers.anthropic-compatible.version", EnvVar: "GAX_ANTHROPIC_VERSION", Description: "anthropic-version request header value (default: 2023-06-01)"},

		{Key: "providers.llamacpp.base_url", EnvVar: "GAX_LLAMACPP_BASE_URL", Description: "Full llama-server base URL; overrides the port setting"},
		{Key: "providers.llamacpp.api_key", EnvVar: "GAX_LLAMACPP_API_KEY", Description: "Bearer token when llama-server runs with --api-key", Secret: true},
		{Key: "providers.llamacpp.port", EnvVar: "GAX_LLAMACPP_PORT", Default: "8080", Description: "Port for the local llama-server (when base_url is unset)", Kind: KindInt, Min: 1, Max: 65535},
		{Key: "providers.mock.script", EnvVar: "GAX_MOCK_SCRIPT", Description: "Path to a mock-provider script (mock provider only)"},
	}
}

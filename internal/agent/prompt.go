package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/skill"
)

const baseSystemPrompt = `You are gax, a minimalist terminal-native coding agent.

Workflow & restraint:
- Read files before editing; prefer native tools (read, edit, write) over shell commands for file operations.
- Prefer separate tool calls over chaining unrelated shell commands with && or ; so each step is clear and interruptible.
- Never run destructive git commands (reset --hard, push --force, clean -fd) without explicit user request.
- Verify changes with the project's build/test commands before claiming completion.`

// PromptOptions controls system prompt assembly.
type PromptOptions struct {
	CWD       string
	Model     string
	ShellName string
	Store     *config.Store
	MCPIndex  string
	Raw       bool
	Bare      bool
}

// BuildSystemPrompt assembles the system prompt according to configuration flags.
func BuildSystemPrompt(opts PromptOptions) string {
	if opts.Raw {
		return ""
	}
	store := opts.Store
	if store == nil {
		store = config.NewStore()
	}

	var sections []string
	customSys := store.String("system_prompt")
	if customSys == "(none)" {
		return ""
	}
	if customSys != "" {
		sections = append(sections, resolvePromptValue(customSys, platform.ResolveDirs().Config))
	} else {
		sections = append(sections, baseSystemPrompt)
	}
	if app := store.String("system_prompt_append"); app != "" {
		sections = append(sections, resolvePromptValue(app, platform.ResolveDirs().Config))
	}

	if !store.Bool("no_env") {
		sections = append(sections, fmt.Sprintf("# Environment\n- cwd: %s\n- shell: %s\n- model: %s",
			opts.CWD, opts.ShellName, opts.Model))
	}

	if !opts.Bare {
		if !store.Bool("no_agents_md") {
			if agents := loadAgentsMD(opts.CWD); agents != "" {
				sections = append(sections, "# Project Instructions (AGENTS.md)\n"+agents)
			}
		}
		if !store.Bool("no_skills") {
			dirs := platform.ResolveDirs()
			skills := skill.Discover(skill.Options{Home: dirs.Home, CWD: opts.CWD})
			if len(skills) > 0 {
				var b strings.Builder
				b.WriteString("# Skills\nCall the `skill` tool to load a skill when a task matches its description:\n")
				for _, s := range skills {
					fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
				}
				sections = append(sections, strings.TrimRight(b.String(), "\n"))
			}
		}
		if !store.Bool("no_subagents") {
			var b strings.Builder
			b.WriteString("# Subagents\nFor independent parallel work, you may invoke `gax -p \"<task>\"` via `bash` (bounded by GAX_SUBAGENT_DEPTH). Use `--preset <name>` to select a configured preset role.")
			for name, p := range store.Presets() {
				if p.Description != "" {
					fmt.Fprintf(&b, "\n- preset `%s`: %s", name, p.Description)
				}
			}
			sections = append(sections, b.String())
		}
		if opts.MCPIndex != "" {
			sections = append(sections, opts.MCPIndex)
		}
	}

	return strings.Join(sections, "\n\n")
}

func resolvePromptValue(val, configDir string) string {
	if !strings.HasPrefix(val, "@") {
		return val
	}
	p := strings.TrimPrefix(val, "@")
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(platform.ResolveDirs().Home, p[2:])
	} else if !filepath.IsAbs(p) && configDir != "" {
		p = filepath.Join(configDir, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return val
	}
	return strings.TrimSpace(string(b))
}

func loadAgentsMD(cwd string) string {
	dirs := platform.ResolveDirs()
	var parts []string
	if dirs.Config != "" {
		if b, err := os.ReadFile(filepath.Join(dirs.Config, "AGENTS.md")); err == nil && len(b) > 0 {
			parts = append(parts, strings.TrimSpace(string(b)))
		}
	}
	if cwd == "" {
		return strings.Join(parts, "\n\n")
	}
	var chain []string
	cur := cwd
	for {
		chain = append(chain, cur)
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if b, err := os.ReadFile(filepath.Join(chain[i], "AGENTS.md")); err == nil && len(b) > 0 {
			parts = append(parts, strings.TrimSpace(string(b)))
		}
	}
	return strings.Join(parts, "\n\n")
}

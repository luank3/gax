package skill

var builtinSkills = []Entry{
	{
		Name:        "code-intelligence",
		Description: "Structural code search and symbol navigation using ast-grep, ctags, rg, and git history without whole-file reads.",
		Path:        "gax:skills/code-intelligence/SKILL.md",
		DisplayPath: "gax:skills/code-intelligence/SKILL.md",
		BuiltinBody: `---
name: code-intelligence
description: Structural code search and symbol navigation using ast-grep, ctags, rg, and git history without whole-file reads.
---
# Code Intelligence
- Prefer ` + "`rg -n`" + ` or ` + "`ast-grep`" + ` to locate definitions and call sites before calling ` + "`read`" + `.
- Use ` + "`read`" + ` with ` + "`offset`" + ` and ` + "`limit`" + ` around matching lines rather than reading whole files.
- Use ` + "`git log -S`" + ` or ` + "`git blame`" + ` when investigating why a line or invariant exists.
`,
	},
	{
		Name:        "customize-gax",
		Description: "Configure gax settings, providers, presets, and MCP servers in config.json.",
		Path:        "gax:skills/customize-gax/SKILL.md",
		DisplayPath: "gax:skills/customize-gax/SKILL.md",
		BuiltinBody: `---
name: customize-gax
description: Configure gax settings, providers, presets, and MCP servers in config.json.
---
# Customizing gax
- User configuration lives at ` + "`~/.config/gax/config.json`" + `.
- Keep secrets in environment variables (` + "`$VAR`" + ` or ` + "`api_key_env`" + `).
- Use ` + "`/config`" + ` in the REPL to inspect resolved values and sources.
`,
	},
}

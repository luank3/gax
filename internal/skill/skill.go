// Package skill discovers SKILL.md files and provides built-in skills.
package skill

import (
	"os"
	"path/filepath"
	"strings"
)

// Entry describes one discovered or built-in skill.
type Entry struct {
	Name        string
	Description string
	Path        string
	DisplayPath string
	BuiltinBody string
}

// Load returns the full SKILL.md text for entry.
func (e Entry) Load() (string, error) {
	if e.BuiltinBody != "" {
		return e.BuiltinBody, nil
	}
	raw, err := os.ReadFile(e.Path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Options configures skill discovery roots.
type Options struct {
	Home string
	CWD  string
}

// Discover searches project .agents/skills up to the git root, ~/.config/gax/skills,
// ~/.agents/skills, and built-in skills. Nearest match wins per skill name.
func Discover(opts Options) []Entry {
	seen := make(map[string]bool)
	var out []Entry

	addDir := func(dir, displayPrefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, de := range entries {
			if !de.IsDir() {
				continue
			}
			name := de.Name()
			if seen[name] {
				continue
			}
			skillFile := filepath.Join(dir, name, "SKILL.md")
			raw, err := os.ReadFile(skillFile)
			if err != nil {
				continue
			}
			seen[name] = true
			out = append(out, Entry{
				Name:        name,
				Description: parseDescription(string(raw)),
				Path:        skillFile,
				DisplayPath: filepath.Join(displayPrefix, name, "SKILL.md"),
			})
		}
	}

	if opts.CWD != "" {
		cur := opts.CWD
		for {
			addDir(filepath.Join(cur, ".agents", "skills"), ".agents/skills")
			if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	if opts.Home != "" {
		addDir(filepath.Join(opts.Home, ".config", "gax", "skills"), "~/.config/gax/skills")
		addDir(filepath.Join(opts.Home, ".agents", "skills"), "~/.agents/skills")
	}

	for _, b := range builtinSkills {
		if seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		out = append(out, b)
	}
	return out
}

// parseDescription extracts `description` from YAML frontmatter, supporting
// single-line values and `>` / `|` block scalars.
func parseDescription(doc string) string {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" {
			break
		}
		if !strings.HasPrefix(trimmed, "description:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
		if rest != ">" && rest != "|" && rest != ">-" && rest != "|-" {
			return strings.Trim(rest, `"'`)
		}
		var parts []string
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "---" {
				break
			}
			if len(l) > 0 && l[0] != ' ' && l[0] != '\t' {
				break
			}
			parts = append(parts, strings.TrimSpace(l))
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

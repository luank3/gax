// Package skill carries the skills gax ships inside the binary. A built-in skill is listed with the
// discovered ones, but its SKILL.md lives in the executable, so the read tool serves it from here
// under the gax: path scheme instead of the filesystem.
package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/luank3/gax/internal/config"
)

// Scheme prefixes the path of a built-in skill: gax:skills/<name>/SKILL.md.
const Scheme = "gax:"

// settingsMarker is replaced by the generated table of registered settings, so the shipped document
// cannot drift from the registry it documents.
const settingsMarker = "<!-- settings-table -->"

//go:embed skills
var documents embed.FS

// Skill is one built-in SKILL.md.
type Skill struct {
	Name string
	Path string
	Body string
}

var builtin = load()

// Builtin returns the built-in skills in name order.
func Builtin() []Skill { return append([]Skill(nil), builtin...) }

// Read returns the body of the built-in skill at path and whether path named one.
func Read(path string) (string, bool) {
	for _, entry := range builtin {
		if entry.Path == path {
			return entry.Body, true
		}
	}
	return "", false
}

func load() []Skill {
	entries, err := fs.ReadDir(documents, "skills")
	if err != nil {
		return nil
	}
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, err := documents.ReadFile("skills/" + entry.Name() + "/SKILL.md")
		if err != nil {
			continue
		}
		skills = append(skills, Skill{
			Name: entry.Name(),
			Path: Scheme + "skills/" + entry.Name() + "/SKILL.md",
			Body: render(string(data)),
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

func render(body string) string {
	if !strings.Contains(body, settingsMarker) {
		return body
	}
	return strings.Replace(body, settingsMarker, settingsTable(), 1)
}

func settingsTable() string {
	var out strings.Builder
	out.WriteString("| Setting | Environment variable | Default | Changeable | Notes |\n")
	out.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, setting := range config.DefaultSettings() {
		changeable := "restart"
		if setting.Editable {
			changeable = "live"
		}
		fmt.Fprintf(&out, "| `%s` | `%s` | %s | %s | %s |\n",
			setting.Key, setting.EnvVar, defaultCell(setting), changeable, notesCell(setting))
	}
	return out.String()
}

func defaultCell(setting config.Setting) string {
	if setting.Default == "" {
		return "—"
	}
	return "`" + setting.Default + "`"
}

func notesCell(setting config.Setting) string {
	notes := setting.Description
	if setting.Choices != "" && !mentionsEveryChoice(notes, setting.Choices) {
		notes += " (values: " + strings.ReplaceAll(setting.Choices, "|", ", ") + ")"
	}
	if setting.Secret {
		notes += " (secret: never print the value)"
	}
	return strings.ReplaceAll(notes, "|", "\\|")
}

// mentionsEveryChoice keeps the table from repeating enumerations a description already carries.
func mentionsEveryChoice(description, choices string) bool {
	words := strings.FieldsFunc(strings.ToLower(description), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
	for _, choice := range strings.Split(choices, "|") {
		if !slices.Contains(words, strings.ToLower(choice)) {
			return false
		}
	}
	return true
}

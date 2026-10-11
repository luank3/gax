package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/skill"
)

func skillDefinition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Name: "skill",
		Description: "List the available skills, or load one by name. A skill is a document of instructions for a " +
			"kind of task: loading it puts those instructions in context for the rest of the session. Call this with " +
			"no arguments to list what is available, then call it again with `name` to load the one whose description " +
			"matches the task. The loaded document is followed with the other tools (read, edit, write, bash).",
		Params: []provider.ToolParam{
			{Name: "name", Type: "string", Description: "Skill to load. Omit to list the available skills."},
		},
	}
}

func (registry *Registry) runSkill(arguments string, _ int, _ DisplaySink) Result {
	if registry.noSkills() {
		return Result{Text: "skills are disabled (no_skills)"}
	}
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	name := ""
	if raw, present := object["name"]; present && string(raw) != "null" {
		value, ok := object.string("name")
		if !ok {
			return Result{Text: "'name' must be a string"}
		}
		name = strings.TrimSpace(value)
	}
	entries := skill.Discover(skill.Options{Home: os.Getenv("HOME"), CWD: registry.bashWorkingDirectory()})
	if name == "" {
		return Result{Text: skillListing(entries)}
	}
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		body, err := entry.Load()
		if err != nil {
			return Result{Text: fmt.Sprintf("error loading skill '%s': %s", name, err)}
		}
		return Result{Text: sanitizeUTF8(strings.TrimRight(body, "\n"))}
	}
	return Result{Text: fmt.Sprintf("unknown skill '%s' (%s)", name, availableSkills(entries))}
}

// skillListing mirrors the listing the system prompt used to carry: one line per skill, with the
// description that tells the model when to load it.
func skillListing(entries []skill.Entry) string {
	if len(entries) == 0 {
		return "no skills are available"
	}
	var builder strings.Builder
	for index, entry := range entries {
		if index > 0 {
			builder.WriteString("\n")
		}
		if entry.Description != "" {
			fmt.Fprintf(&builder, "- %s: %s (%s)", entry.Name, entry.Description, entry.DisplayPath)
			continue
		}
		fmt.Fprintf(&builder, "- %s (%s)", entry.Name, entry.DisplayPath)
	}
	return builder.String()
}

func availableSkills(entries []skill.Entry) string {
	if len(entries) == 0 {
		return "no skills are available"
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return "available: " + strings.Join(names, ", ")
}

func (registry *Registry) noSkills() bool {
	if registry == nil {
		return false
	}
	if registry.store != nil {
		return registry.store.Bool("no_skills")
	}
	return registry.options.NoSkills
}

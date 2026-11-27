// Package slash defines REPL slash commands and shell-like Tab completion.
package slash

import (
	"sort"
	"strings"
)

// Command describes one REPL slash command.
type Command struct {
	Name        string
	ArgHint     string
	Description string
}

// Commands lists all REPL slash commands in /help order.
var Commands = []Command{
	{Name: "/new", ArgHint: "[preset]", Description: "Start a fresh conversation (/clear alias)"},
	{Name: "/clear", ArgHint: "[preset]", Description: "Alias for /new"},
	{Name: "/resume", Description: "Pick a past session for this directory"},
	{Name: "/fork", ArgHint: "[n]", Description: "Create a new session before an earlier prompt"},
	{Name: "/undo", ArgHint: "[n]", Description: "Roll back conversation before an earlier prompt"},
	{Name: "/provider", ArgHint: "[id]", Description: "Switch provider"},
	{Name: "/model", ArgHint: "[id]", Description: "Switch model"},
	{Name: "/effort", ArgHint: "[level]", Description: "Set reasoning effort"},
	{Name: "/preset", ArgHint: "[name]", Description: "Apply a config-defined preset"},
	{Name: "/preset-save", ArgHint: "<name> [tint]", Description: "Save current selection as a preset"},
	{Name: "/config", ArgHint: "[key [value]]", Description: "Inspect or override settings"},
	{Name: "/compact", ArgHint: "[focus]", Description: "Summarize older context"},
	{Name: "/copy", Description: "Copy latest assistant response"},
	{Name: "/tasks", ArgHint: "[kill <id>|all]", Description: "List or stop background tasks"},
	{Name: "/session", Description: "Show session selection and usage totals"},
	{Name: "/usage", Description: "Query provider account usage"},
	{Name: "/login", ArgHint: "[provider]", Description: "Log in to a provider account"},
	{Name: "/logout", ArgHint: "[provider]", Description: "Remove a gax-managed login"},
	{Name: "/help", Description: "List commands and keyboard shortcuts"},
}

// Complete returns matching command names or argument choices for prefix.
func Complete(input string, argChoices func(cmd string) []string) (matches []string, commonPrefix string) {
	if !strings.HasPrefix(input, "/") {
		return nil, ""
	}
	if !strings.Contains(input, " ") {
		for _, c := range Commands {
			if strings.HasPrefix(c.Name, input) {
				matches = append(matches, c.Name)
			}
		}
		sort.Strings(matches)
		return matches, longestCommonPrefix(matches)
	}
	cmd, rest, _ := strings.Cut(input, " ")
	if argChoices == nil {
		return nil, ""
	}
	for _, choice := range argChoices(cmd) {
		if strings.HasPrefix(choice, rest) {
			matches = append(matches, cmd+" "+choice)
		}
	}
	sort.Strings(matches)
	return matches, longestCommonPrefix(matches)
}

func longestCommonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0]
	for _, s := range items[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

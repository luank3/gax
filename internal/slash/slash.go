// Package slash parses and describes gax's interactive slash commands.
package slash

import (
	"strings"
	"unicode/utf8"
)

type Outcome uint8

const (
	NotCommand Outcome = iota
	Handled
	Unknown
	BadUsage
)

// Command describes one user-visible command. Registry order is the upstream /help and
// completion order. Usage is empty for commands that do not take arguments.
type Command struct {
	Name    string
	Alias   string
	Summary string
	Usage   string
}

type Parsed struct {
	Outcome     Outcome
	Name        string
	Argument    string
	HasArgument bool
	Command     *Command
}

var commandRegistry = []Command{
	{Name: "new", Alias: "clear", Summary: "start a fresh conversation", Usage: "[preset]"},
	{Name: "resume", Summary: "resume a past conversation"},
	{Name: "undo", Summary: "revert conversation to before an earlier message", Usage: "[turns back]"},
	{Name: "fork", Summary: "branch a new session before an earlier message", Usage: "[turns back]"},
	{Name: "provider", Summary: "switch provider, then model and effort"},
	{Name: "model", Summary: "switch model, then effort"},
	{Name: "effort", Summary: "set reasoning effort"},
	{Name: "preset", Summary: "switch to a config-defined preset", Usage: "[name]"},
	{Name: "preset-save", Summary: "save the current selection as a preset", Usage: "<name> [tint]"},
	{Name: "config", Summary: "view or change settings", Usage: "[key [value]]"},
	{Name: "compact", Summary: "summarize the conversation to free up context", Usage: "[focus]"},
	{Name: "copy", Summary: "copy last response to clipboard"},
	{Name: "tasks", Summary: "list background tasks", Usage: "[kill <id>... | kill all]"},
	{Name: "session", Summary: "show this session's info and usage totals"},
	{Name: "usage", Summary: "show provider account usage"},
	{Name: "login", Summary: "log in to a provider account, managed by gax", Usage: "[provider]"},
	{Name: "logout", Summary: "log out and remove a gax-managed login", Usage: "[provider]"},
	{Name: "help", Summary: "show this help"},
}

// Commands returns a copy of the registry so callers cannot mutate parser behavior.
func Commands() []Command { return append([]Command(nil), commandRegistry...) }

func Parse(line string) Parsed {
	name, argument, hasArgument, ok := parseCommand(line)
	if !ok {
		return Parsed{Outcome: NotCommand}
	}
	parsed := Parsed{Name: name, Argument: argument, HasArgument: hasArgument}
	command := findCommand(name)
	if command == nil {
		parsed.Outcome = Unknown
		return parsed
	}
	copy := *command
	parsed.Command = &copy
	if parsed.HasArgument && command.Usage == "" {
		parsed.Outcome = BadUsage
		return parsed
	}
	parsed.Outcome = Handled
	return parsed
}

func parseCommand(line string) (name, argument string, hasArgument, ok bool) {
	if len(line) == 0 || line[0] != '/' {
		return "", "", false, false
	}
	end := 1
	for end < len(line) && !isSpace(line[end]) {
		if !isNameByte(line[end]) {
			return "", "", false, false
		}
		end++
	}
	if end == 1 {
		return "", "", false, false
	}
	name = line[1:end]
	start := end
	for start < len(line) && isSpace(line[start]) {
		start++
	}
	if start < len(line) {
		end := len(line)
		for end > start && isSpace(line[end-1]) {
			end--
		}
		return name, line[start:end], true, true
	}
	return name, "", false, true
}

func findCommand(name string) *Command {
	for index := range commandRegistry {
		command := &commandRegistry[index]
		if command.Name == name || command.Alias == name {
			return command
		}
	}
	return nil
}

// CompleteName implements the upstream shell-like command-name completion. A unique match is
// returned with a trailing space; ambiguous matches return only their shared prefix.
func CompleteName(prefix string) (string, bool) {
	matches := nameMatches(prefix)
	switch len(matches) {
	case 0:
		return "", false
	case 1:
		return matches[0] + " ", true
	default:
		shared := sharedPrefix(matches)
		if len(shared) <= len(prefix) {
			return "", false
		}
		return shared, true
	}
}

// NameCandidates returns the space-separated matching spellings when a prefix is ambiguous.
func NameCandidates(prefix string) (string, bool) {
	matches := nameMatches(prefix)
	if len(matches) < 2 {
		return "", false
	}
	var out strings.Builder
	for index, match := range matches {
		if index > 0 {
			out.WriteByte(' ')
		}
		out.WriteByte('/')
		out.WriteString(match)
	}
	return out.String(), true
}

func nameMatches(prefix string) []string {
	matches := make([]string, 0, len(commandRegistry)*2)
	for _, command := range commandRegistry {
		for _, name := range []string{command.Name, command.Alias} {
			if name != "" && strings.HasPrefix(name, prefix) {
				matches = append(matches, name)
			}
		}
	}
	return matches
}

func sharedPrefix(matches []string) string {
	shared := matches[0]
	for _, candidate := range matches[1:] {
		limit := len(shared)
		if len(candidate) < limit {
			limit = len(candidate)
		}
		index := 0
		for index < limit && shared[index] == candidate[index] {
			index++
		}
		shared = shared[:index]
	}
	sharedBytes := len(shared)
	for sharedBytes > 0 && sharedBytes < len(matches[0]) && !utf8.RuneStart(matches[0][sharedBytes]) {
		sharedBytes--
	}
	return matches[0][:sharedBytes]
}

// MatchName reports the byte range to replace when the cursor sits just after a command name.
func MatchName(buffer string, cursor int) (start, end int, ok bool) {
	nameEnd, valid := scanCommandName(buffer)
	if !valid || cursor != nameEnd {
		return 0, 0, false
	}
	return 1, nameEnd, true
}

// Hint returns the registry placeholder when a complete command name has no argument yet.
func Hint(line string) (string, bool) {
	if strings.ContainsRune(line, '\n') {
		return "", false
	}
	nameEnd, valid := scanCommandName(line)
	if !valid {
		return "", false
	}
	command := findCommand(line[1:nameEnd])
	if command == nil || command.Usage == "" {
		return "", false
	}
	argument := nameEnd
	for argument < len(line) && isSpace(line[argument]) {
		argument++
	}
	if argument < len(line) {
		return "", false
	}
	prefix := ""
	if nameEnd == len(line) {
		prefix = " "
	}
	return prefix + command.Usage, true
}

func scanCommandName(line string) (int, bool) {
	if len(line) == 0 || line[0] != '/' {
		return 0, false
	}
	end := 1
	for end < len(line) && isNameByte(line[end]) {
		end++
	}
	if end < len(line) && !isSpace(line[end]) {
		return 0, false
	}
	return end, true
}

func isNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '-'
}

func isSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

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

// Command describes one user-visible command. Registry order is the /help and
// completion order. Usage is empty for commands that do not take arguments.
type Command struct {
	Name    string
	Alias   string
	Summary string
	Usage   string
	// ArgumentChoices marks the commands whose first argument enumerates fixed values. Their
	// argument word belongs to this completer; every other argument word stays with the completers
	// that follow, such as @file mentions.
	ArgumentChoices bool
}

type Parsed struct {
	Outcome     Outcome
	Name        string
	Argument    string
	HasArgument bool
	Command     *Command
}

var commandRegistry = []Command{
	{Name: "new", Alias: "clear", Summary: "start a fresh conversation", Usage: "[preset]", ArgumentChoices: true},
	{Name: "resume", Summary: "resume a past conversation"},
	{Name: "undo", Summary: "revert conversation to before an earlier message", Usage: "[turns back]"},
	{Name: "fork", Summary: "branch a new session before an earlier message", Usage: "[turns back]"},
	{Name: "provider", Summary: "switch provider, then model and effort", Usage: "[id]", ArgumentChoices: true},
	{Name: "model", Summary: "switch model, then effort", Usage: "[id]", ArgumentChoices: true},
	{Name: "effort", Summary: "set reasoning effort", Usage: "[level]", ArgumentChoices: true},
	{Name: "preset", Summary: "switch to a config-defined preset", Usage: "[name]", ArgumentChoices: true},
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

// ArgumentChoices returns the candidate values Tab offers for a command's first argument, in
// display order, or nil when the command does not enumerate one. It runs on Tab, so it must return
// promptly: no network, child processes, or waiting on background work.
type ArgumentChoices func(name string) []string

// Match reports the buffer span the command completer owns at the cursor: the command name, or the
// first argument word of a command that enumerates it. The span starts after the slash, so the
// replacement Complete returns covers the whole command text.
func Match(buffer string, cursor int) (start, end int, ok bool) {
	if cursor < 0 || cursor > len(buffer) {
		return 0, 0, false
	}
	nameEnd, valid := scanCommandName(buffer)
	if !valid || cursor < nameEnd {
		return 0, 0, false
	}
	if cursor < len(buffer) && !isSpace(buffer[cursor]) {
		return 0, 0, false
	}
	// Completion applies on the line's first row only, so a pasted multi-line buffer keeps its text.
	if strings.ContainsRune(buffer[:cursor], '\n') {
		return 0, 0, false
	}
	if cursor > nameEnd {
		command := findCommand(buffer[1:nameEnd])
		if command == nil || !command.ArgumentChoices {
			return 0, 0, false
		}
	}
	return 1, cursor, true
}

// Complete returns the replacement for the command text, which is the prompt after its slash up to
// the cursor. A unique match is returned with a trailing space; ambiguous matches return only their
// shared prefix, matching the shell-like completion.
func Complete(text string, choices ArgumentChoices) (string, bool) {
	candidates, word := commandChoices(text, choices)
	extended, ok := extendCandidates(candidates, word)
	if !ok {
		return "", false
	}
	return text[:len(text)-len(word)] + extended, true
}

// ListCandidates returns the space-separated candidates a repeated Tab lists. A bare slash matches
// every command, which /help already lists in full, so it points there instead.
func ListCandidates(text string, choices ArgumentChoices) (string, bool) {
	if text == "" {
		return "  see /help", true
	}
	candidates, word := commandChoices(text, choices)
	if len(candidates) < 2 {
		return "", false
	}
	// Like a shell listing a directory, candidates show only what follows the typed word's last
	// slash; command names carry the slash the prompt already has before them.
	marker, shared := "", 0
	if word == text {
		marker = "/"
	}
	if index := strings.LastIndexByte(word, '/'); index >= 0 {
		shared = index + 1
	}
	var listing strings.Builder
	for index, candidate := range candidates {
		if index > 0 {
			listing.WriteByte(' ')
		} else {
			listing.WriteString("  ")
		}
		listing.WriteString(marker)
		listing.WriteString(candidate[shared:])
	}
	return listing.String(), true
}

// commandChoices collects the candidates matching the text's last word, which it returns too. The
// word is the command name when no space precedes it.
func commandChoices(text string, choices ArgumentChoices) ([]string, string) {
	wordStart := len(text)
	for wordStart > 0 && !isSpace(text[wordStart-1]) {
		wordStart--
	}
	word := text[wordStart:]
	candidates := make([]string, 0, len(commandRegistry)*2)
	if wordStart == 0 {
		for _, command := range commandRegistry {
			candidates = append(candidates, command.Name)
			if command.Alias != "" {
				candidates = append(candidates, command.Alias)
			}
		}
	} else {
		name := text[:commandNameBytes(text)]
		command := findCommand(name)
		if command == nil || !command.ArgumentChoices {
			return nil, word
		}
		// Only the first argument word enumerates; a later one is left to the other completers.
		if trimSpace(text[len(name):wordStart]) != "" {
			return nil, word
		}
		if choices != nil {
			candidates = append(candidates, choices(command.Name)...)
		}
	}
	kept := candidates[:0]
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, word) {
			kept = append(kept, candidate)
		}
	}
	return kept, word
}

// commandNameBytes returns the length of the leading command-name bytes in text after its slash.
func commandNameBytes(text string) int {
	end := 0
	for end < len(text) && isNameByte(text[end]) {
		end++
	}
	return end
}

func extendCandidates(candidates []string, word string) (string, bool) {
	switch len(candidates) {
	case 0:
		return "", false
	case 1:
		return candidates[0] + " ", true
	}
	shared := sharedPrefix(candidates)
	if len(shared) <= len(word) {
		return "", false
	}
	return shared, true
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

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpace(value[start]) {
		start++
	}
	for end > start && isSpace(value[end-1]) {
		end--
	}
	return value[start:end]
}

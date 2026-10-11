package slash

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandsMatchUpstreamOrder(t *testing.T) {
	commands := Commands()
	if command := commandByName(commands, "provider"); command == nil || command.Summary != "switch provider, then model and effort" {
		t.Fatalf("provider command metadata = %+v", command)
	}
	if command := commandByName(commands, "model"); command == nil || command.Summary != "switch model, then effort" {
		t.Fatalf("model command metadata = %+v", command)
	}
	want := []string{
		"new/clear", "resume", "undo", "fork", "provider", "model", "effort", "preset",
		"preset-save", "config", "compact", "copy", "tasks", "session", "usage", "login",
		"logout", "help",
	}
	got := make([]string, 0, len(commands))
	for _, command := range commands {
		name := command.Name
		if command.Alias != "" {
			name += "/" + command.Alias
		}
		got = append(got, name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command registry order = %v, want %v", got, want)
	}
}

func commandByName(commands []Command, name string) *Command {
	for index := range commands {
		if commands[index].Name == name {
			return &commands[index]
		}
	}
	return nil
}

func TestParseSlashOutcomes(t *testing.T) {
	tests := []struct {
		line      string
		outcome   Outcome
		name      string
		argument  string
		hasArg    bool
		canonical string
	}{
		{line: "hello", outcome: NotCommand},
		{line: "", outcome: NotCommand},
		{line: "/", outcome: NotCommand},
		{line: "/   ", outcome: NotCommand},
		{line: "/tmp/file.go inspect this", outcome: NotCommand},
		{line: "/foo.bar", outcome: NotCommand},
		{line: "/\x1b[2J", outcome: NotCommand},
		{line: "/nonesuch", outcome: Unknown, name: "nonesuch"},
		{line: "/help more", outcome: BadUsage, name: "help", argument: "more", hasArg: true, canonical: "help"},
		{line: "/provider\topenai", outcome: BadUsage, name: "provider", argument: "openai", hasArg: true, canonical: "provider"},
		{line: "/model\tfast", outcome: BadUsage, name: "model", argument: "fast", hasArg: true, canonical: "model"},
		{line: "/effort\thigh", outcome: BadUsage, name: "effort", argument: "high", hasArg: true, canonical: "effort"},
		{line: "/new review fast", outcome: Handled, name: "new", argument: "review fast", hasArg: true, canonical: "new"},
		{line: "/preset alpha \t ", outcome: Handled, name: "preset", argument: "alpha", hasArg: true, canonical: "preset"},
		{line: "/new review fast ", outcome: Handled, name: "new", argument: "review fast", hasArg: true, canonical: "new"},
		{line: "/clear", outcome: Handled, name: "clear", canonical: "new"},
		{line: "/new   ", outcome: Handled, name: "new", canonical: "new"},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			parsed := Parse(test.line)
			if parsed.Outcome != test.outcome || parsed.Name != test.name ||
				parsed.Argument != test.argument || parsed.HasArgument != test.hasArg {
				t.Fatalf("Parse(%q) = %+v", test.line, parsed)
			}
			if test.canonical == "" {
				if parsed.Command != nil {
					t.Fatalf("unexpected command metadata: %+v", parsed.Command)
				}
			} else if parsed.Command == nil || parsed.Command.Name != test.canonical {
				t.Fatalf("command metadata = %+v, want %q", parsed.Command, test.canonical)
			}
		})
	}
}

func TestSlashNameCandidatesAndCompletion(t *testing.T) {
	completions := map[string]string{
		"mo": "model ", "help": "help ", "cle": "clear ", "pre": "preset",
		"preset": "", "preset-": "preset-save ", "c": "", "": "", "zzz": "",
	}
	for prefix, want := range completions {
		got, ok := CompleteName(prefix)
		if (want != "") != ok || got != want {
			t.Errorf("CompleteName(%q) = %q, %t; want %q, %t", prefix, got, ok, want, want != "")
		}
	}
	candidates := map[string]string{
		"c": "/clear /config /compact /copy", "preset": "/preset /preset-save", "mo": "", "zzz": "",
	}
	for prefix, want := range candidates {
		got, ok := NameCandidates(prefix)
		if (want != "") != ok || got != want {
			t.Errorf("NameCandidates(%q) = %q, %t; want %q, %t", prefix, got, ok, want, want != "")
		}
	}
	all, ok := NameCandidates("")
	if !ok || !strings.HasPrefix(all, "/new /clear /resume ") {
		t.Fatalf("all command candidates = %q, %t", all, ok)
	}
}

func TestMatchSlashNameAtCursor(t *testing.T) {
	tests := []struct {
		buffer string
		cursor int
		start  int
		end    int
		match  bool
	}{
		{buffer: "/mo", cursor: 3, start: 1, end: 3, match: true},
		{buffer: "/", cursor: 1, start: 1, end: 1, match: true},
		{buffer: "/mo x", cursor: 3, start: 1, end: 3, match: true},
		{buffer: "/mo x", cursor: 5},
		{buffer: "/mo", cursor: 2},
		{buffer: "/home/x", cursor: 7},
		{buffer: "hello", cursor: 5},
		{buffer: "@foo", cursor: 4},
		{buffer: "", cursor: 0},
	}
	for _, test := range tests {
		t.Run(test.buffer, func(t *testing.T) {
			start, end, matched := MatchName(test.buffer, test.cursor)
			if start != test.start || end != test.end || matched != test.match {
				t.Fatalf("MatchName(%q, %d) = (%d, %d, %t)", test.buffer, test.cursor, start, end, matched)
			}
		})
	}
}

func TestSlashHint(t *testing.T) {
	tests := map[string]string{
		"/new": " [preset]", "/new ": "[preset]", "/new   ": "[preset]",
		"/preset": " [name]", "/provider": "", "/clear": " [preset]", "/mo": "", "/pre": "",
		"/": "", "/zzz": "", "/zzz x": "", "/model": "", "/model ": "", "/effort": "",
		"/model foo": "", "/new foo": "", "/new\n": "",
	}
	for line, want := range tests {
		got, ok := Hint(line)
		if (want != "") != ok || got != want {
			t.Errorf("Hint(%q) = %q, %t; want %q, %t", line, got, ok, want, want != "")
		}
	}
}

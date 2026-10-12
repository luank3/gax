package slash

import (
	"reflect"
	"testing"
)

func TestCommandsMatchRegistryOrder(t *testing.T) {
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
		{line: "/copy x", outcome: BadUsage, name: "copy", argument: "x", hasArg: true, canonical: "copy"},
		{line: "/provider\topenai", outcome: Handled, name: "provider", argument: "openai", hasArg: true, canonical: "provider"},
		{line: "/model\tfast", outcome: Handled, name: "model", argument: "fast", hasArg: true, canonical: "model"},
		{line: "/effort\thigh", outcome: Handled, name: "effort", argument: "high", hasArg: true, canonical: "effort"},
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

// stubChoices mirrors the values the app supplies for the commands that enumerate their argument.
func stubChoices(name string) []string {
	switch name {
	case "provider":
		return []string{"mock", "openai"}
	case "model":
		return []string{"anthropic/claude-sonnet-4", "openai/gpt-5", "openai/gpt-5-mini"}
	case "effort":
		return []string{"low", "high", "default"}
	case "new", "preset":
		return []string{"fast", "focus", "review"}
	default:
		return nil
	}
}

func TestSlashCompleteNamesAndAliases(t *testing.T) {
	completions := map[string]string{
		"mo": "model ", "help": "help ", "cle": "clear ", "pre": "preset",
		"preset": "", "preset-": "preset-save ", "c": "", "": "", "zzz": "",
	}
	for text, want := range completions {
		got, ok := Complete(text, stubChoices)
		if (want != "") != ok || got != want {
			t.Errorf("Complete(%q) = %q, %t; want %q, %t", text, got, ok, want, want != "")
		}
	}
	listed := map[string]string{
		"c": "  /clear /config /compact /copy", "preset": "  /preset /preset-save", "mo": "",
		"zzz": "", "": "  see /help",
	}
	for text, want := range listed {
		got, ok := ListCandidates(text, stubChoices)
		if (want != "") != ok || got != want {
			t.Errorf("ListCandidates(%q) = %q, %t; want %q, %t", text, got, ok, want, want != "")
		}
	}
	ambiguous, ok := ListCandidates("p", stubChoices)
	if !ok || ambiguous != "  /provider /preset /preset-save" {
		t.Fatalf("name candidates for 'p' = %q, %t", ambiguous, ok)
	}
}

func TestSlashCompleteSelectionArguments(t *testing.T) {
	completions := map[string]string{
		"provider o": "provider openai ", "provider ": "", "effort h": "effort high ",
		"model op": "model openai/gpt-5", "model gpt": "", "effort high h": "",
	}
	for text, want := range completions {
		got, ok := Complete(text, stubChoices)
		if (want != "") != ok || got != want {
			t.Errorf("Complete(%q) = %q, %t; want %q, %t", text, got, ok, want, want != "")
		}
	}
	listed := map[string]string{
		"provider ": "  mock openai", "effort ": "  low high default",
		"model ": "  anthropic/claude-sonnet-4 openai/gpt-5 openai/gpt-5-mini",
		// Past a slash, the listing drops the part every candidate shares.
		"model openai/":    "  gpt-5 gpt-5-mini",
		"model openai/gpt": "  gpt-5 gpt-5-mini",
		// An argument word past the first one stays with the other completers.
		"effort high ": "", "compact r": "",
	}
	for text, want := range listed {
		got, ok := ListCandidates(text, stubChoices)
		if (want != "") != ok || got != want {
			t.Errorf("ListCandidates(%q) = %q, %t; want %q, %t", text, got, ok, want, want != "")
		}
	}
}

func TestSlashCompleteArgumentKeepsCommandText(t *testing.T) {
	completions := map[string]string{
		"preset r": "preset review ", "preset  re": "preset  review ",
		"new re": "new review ", "clear re": "clear review ", "preset c": "",
		"preset review r": "", "zzz r": "",
	}
	for text, want := range completions {
		got, ok := Complete(text, stubChoices)
		if (want != "") != ok || got != want {
			t.Errorf("Complete(%q) = %q, %t; want %q, %t", text, got, ok, want, want != "")
		}
	}
}

func TestMatchSlashCommandTextAtCursor(t *testing.T) {
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
		// The span runs from the name through the argument word ending at the cursor.
		{buffer: "/preset x", cursor: 9, start: 1, end: 9, match: true},
		{buffer: "/preset ", cursor: 8, start: 1, end: 8, match: true},
		{buffer: "/preset x", cursor: 8},
		{buffer: "/mo x", cursor: 5},
		{buffer: "/compact @src", cursor: 13},
		{buffer: "/zzz x", cursor: 6},
		{buffer: "/mo", cursor: 2},
		{buffer: "/new\nfoo", cursor: 8},
		{buffer: "/home/x", cursor: 7},
		{buffer: "hello", cursor: 5},
		{buffer: "@foo", cursor: 4},
		{buffer: "", cursor: 0},
	}
	for _, test := range tests {
		t.Run(test.buffer, func(t *testing.T) {
			start, end, matched := Match(test.buffer, test.cursor)
			if start != test.start || end != test.end || matched != test.match {
				t.Fatalf("Match(%q, %d) = (%d, %d, %t)", test.buffer, test.cursor, start, end, matched)
			}
		})
	}
}

func TestSlashHint(t *testing.T) {
	tests := map[string]string{
		"/new": " [preset]", "/new ": "[preset]", "/new   ": "[preset]",
		"/preset": " [name]", "/provider": " [id]", "/clear": " [preset]", "/mo": "", "/pre": "",
		"/": "", "/zzz": "", "/zzz x": "", "/model": " [id]", "/model ": "[id]", "/effort": " [level]",
		"/model foo": "", "/new foo": "", "/new\n": "",
	}
	for line, want := range tests {
		got, ok := Hint(line)
		if (want != "") != ok || got != want {
			t.Errorf("Hint(%q) = %q, %t; want %q, %t", line, got, ok, want, want != "")
		}
	}
}

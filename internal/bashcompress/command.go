// Package bashcompress reduces the model-facing output of common shell commands.
//
// The filter set follows rtk (github.com/rtk-ai/rtk): each command family has one compact shape,
// capped the way rtk caps it, and a result that dropped lines reports so, because the bash tool
// then keeps the full output recoverable. Filters never run on a command whose captured bytes
// belong to something else (a pipeline, a redirect, a second command).
package bashcompress

import "strings"

// Invocation is one parsed simple command.
type Invocation struct {
	// Program is the command word's basename, lowercased for matching.
	Program string
	// Args are the arguments after the command word, with quotes and escapes resolved.
	Args []string
}

// commandPrefixes are words that wrap another command and change neither its output nor its shape.
var commandPrefixes = map[string]bool{
	"sudo":    true,
	"command": true,
	"env":     true,
	"nohup":   true,
}

// Parse splits one shell command line into the program it runs and its arguments. It reports
// false for anything that is not a single simple command: a pipeline, a redirect, a command list,
// a command substitution, or a line whose quoting does not close. Those shapes mean the captured
// bytes are not the program's own output, so no filter may touch them.
func Parse(command string) (Invocation, bool) {
	words, ok := splitWords(command)
	if !ok || len(words) == 0 {
		return Invocation{}, false
	}
	for len(words) > 0 {
		word := words[0]
		if assignment(word) {
			words = words[1:]
			continue
		}
		if commandPrefixes[strings.ToLower(base(word))] {
			words = words[1:]
			continue
		}
		break
	}
	if len(words) == 0 {
		return Invocation{}, false
	}
	program := strings.ToLower(base(words[0]))
	if program == "" {
		return Invocation{}, false
	}
	return Invocation{Program: program, Args: words[1:]}, true
}

// base strips a directory prefix from a command word.
func base(word string) string {
	if index := strings.LastIndexByte(word, '/'); index >= 0 {
		return word[index+1:]
	}
	return word
}

// assignment reports whether a word is a leading VAR=value environment assignment.
func assignment(word string) bool {
	equals := strings.IndexByte(word, '=')
	if equals <= 0 {
		return false
	}
	for index, character := range word[:equals] {
		if character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

// splitWords splits a command line on unquoted whitespace, resolving quotes and backslash
// escapes. It reports false for an unclosed quote, for an unquoted shell operator (which would
// make the captured bytes another command's output), and for a command substitution, which is
// expanded before the program ever sees its arguments.
func splitWords(command string) ([]string, bool) {
	words := make([]string, 0, 8)
	var word strings.Builder
	started := false
	quote := byte(0)
	for index := 0; index < len(command); index++ {
		character := command[index]
		if character == '\\' && quote != '\'' {
			if index+1 >= len(command) {
				return nil, false
			}
			index++
			word.WriteByte(command[index])
			started = true
			continue
		}
		if quote == 0 {
			switch character {
			case '|', '&', ';', '<', '>', '\n', '\r':
				return nil, false
			}
		}
		if quote != '\'' {
			switch {
			case character == '`':
				return nil, false
			case character == '$' && index+1 < len(command) && command[index+1] == '(':
				return nil, false
			}
		}
		if quote == 0 && (character == ' ' || character == '\t') {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		if quote == 0 && (character == '\'' || character == '"') {
			quote = character
			started = true
			continue
		}
		if quote != 0 && character == quote {
			quote = 0
			continue
		}
		word.WriteByte(character)
		started = true
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, word.String())
	}
	return words, true
}

// firstPositional returns the first argument that is not an option, stopping at "--" like the
// programs themselves do.
func (invocation Invocation) firstPositional() string {
	for _, arg := range invocation.Args {
		if arg == "--" {
			return ""
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return arg
		}
	}
	return ""
}

// flagValue returns the value of the first `-x value`, `-xvalue`, or `--name=value` argument that
// carries one, and whether it was found.
func (invocation Invocation) flagValue(short byte, long string) (string, bool) {
	for index, arg := range invocation.Args {
		switch {
		case long != "" && arg == "--"+long:
			if index+1 < len(invocation.Args) {
				return invocation.Args[index+1], true
			}
			return "", false
		case long != "" && strings.HasPrefix(arg, "--"+long+"="):
			return arg[len(long)+3:], true
		case short != 0 && arg == "-"+string(short):
			if index+1 < len(invocation.Args) {
				return invocation.Args[index+1], true
			}
			return "", false
		case short != 0 && len(arg) > 2 && arg[0] == '-' && arg[1] == short:
			return arg[2:], true
		}
	}
	return "", false
}

// hasFlag reports whether a short or long flag is present.
func (invocation Invocation) hasFlag(short byte, long string) bool {
	for _, arg := range invocation.Args {
		if arg == "--" {
			return false
		}
		if short != 0 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") &&
			strings.IndexByte(arg[1:], short) >= 0 {
			return true
		}
		if long != "" && (arg == "--"+long || strings.HasPrefix(arg, "--"+long+"=")) {
			return true
		}
	}
	return false
}

// subcommand returns the first positional argument, or "" when the invocation carries none.
func (invocation Invocation) subcommand() string {
	return invocation.firstPositional()
}

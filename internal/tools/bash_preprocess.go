package tools

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
)

type bashPathQuote uint8

const (
	bashPathUnquoted bashPathQuote = iota
	bashPathDoubleQuoted
	bashPathSingleQuoted
)

// preprocessBashArguments removes only a leading no-op cd from the arguments passed to bash. The
// caller still keeps the original tool call in conversation history.
func preprocessBashArguments(arguments, cwd, home string) string {
	object, errText := parseArguments(arguments)
	if errText != "" {
		return arguments
	}
	command, ok := object.string("command")
	if !ok {
		return arguments
	}
	offset := stripBashCDPrefix(command, cwd, home)
	if offset == 0 {
		return arguments
	}
	rewritten, err := json.Marshal(command[offset:])
	if err != nil {
		return arguments
	}
	object["command"] = rewritten
	rewrittenArguments, err := json.Marshal(object)
	if err != nil {
		return arguments
	}
	return string(rewrittenArguments)
}

// stripBashCDPrefix returns the byte offset after `cd <target> &&` when the target can be proven
// to resolve to cwd. Unsupported shell syntax is left untouched rather than approximated.
func stripBashCDPrefix(command, cwd, home string) int {
	if command == "" || cwd == "" {
		return 0
	}
	cursor := 0
	for cursor < len(command) && isBashHorizontalSpace(command[cursor]) {
		cursor++
	}
	if len(command)-cursor < 3 || command[cursor] != 'c' || command[cursor+1] != 'd' || !isBashHorizontalSpace(command[cursor+2]) {
		return 0
	}
	cursor += 2
	for cursor < len(command) && isBashHorizontalSpace(command[cursor]) {
		cursor++
	}

	quote := bashPathUnquoted
	var token string
	if cursor < len(command) && command[cursor] == '\'' {
		quote = bashPathSingleQuoted
		cursor++
		start := cursor
		for cursor < len(command) && command[cursor] != '\'' {
			cursor++
		}
		if cursor == len(command) {
			return 0
		}
		token = command[start:cursor]
		cursor++
	} else if cursor < len(command) && command[cursor] == '"' {
		quote = bashPathDoubleQuoted
		cursor++
		start := cursor
		for cursor < len(command) && command[cursor] != '"' {
			if command[cursor] == '\\' {
				return 0
			}
			cursor++
		}
		if cursor == len(command) {
			return 0
		}
		token = command[start:cursor]
		cursor++
	} else {
		start := cursor
		for cursor < len(command) && !isBashTokenDelimiter(command[cursor]) {
			cursor++
		}
		if cursor == start {
			return 0
		}
		token = command[start:cursor]
	}

	if quote != bashPathUnquoted && cursor < len(command) && !isBashHorizontalSpace(command[cursor]) && command[cursor] != '&' && command[cursor] != ';' {
		return 0
	}
	for cursor < len(command) && isBashHorizontalSpace(command[cursor]) {
		cursor++
	}
	if len(command)-cursor < 2 || command[cursor] != '&' || command[cursor+1] != '&' {
		return 0
	}
	cursor += 2
	for cursor < len(command) && isBashHorizontalSpace(command[cursor]) {
		cursor++
	}
	if cursor == len(command) {
		return 0
	}

	resolved, ok := resolveBashCDTarget(token, quote, cwd, home)
	if !ok || !bashPathsEqual(resolved, cwd) {
		return 0
	}
	return cursor
}

func resolveBashCDTarget(token string, quote bashPathQuote, cwd, home string) (string, bool) {
	if token == "." {
		return cwd, true
	}
	if quote == bashPathSingleQuoted {
		return token, true
	}
	if quote == bashPathUnquoted && strings.HasPrefix(token, "~") {
		if home == "" || !unquotedBashExpansionSafe(home) {
			return "", false
		}
		if token == "~" {
			return home, true
		}
		if token[1] != '/' {
			return "", false
		}
		for index := 2; index < len(token); index++ {
			if !isBashPathSafe(token[index]) {
				return "", false
			}
		}
		return home + token[1:], true
	}

	var expansion string
	prefixLength := 0
	if strings.HasPrefix(token, "$HOME") && (len(token) == len("$HOME") || token[len("$HOME")] == '/') {
		expansion, prefixLength = home, len("$HOME")
	} else if strings.HasPrefix(token, "${HOME}") {
		expansion, prefixLength = home, len("${HOME}")
	}
	if prefixLength > 0 {
		if expansion == "" || quote == bashPathUnquoted && !unquotedBashExpansionSafe(expansion) {
			return "", false
		}
		for index := prefixLength; index < len(token); index++ {
			if !isBashTokenSafe(token[index], quote) {
				return "", false
			}
		}
		return expansion + token[prefixLength:], true
	}

	windowsDrive := runtime.GOOS == "windows" && len(token) > 2 && token[1] == ':' && token[2] == '/' && filepath.IsAbs(token)
	if len(token) == 0 || token[0] != '/' && !windowsDrive {
		return "", false
	}
	for index := 0; index < len(token); index++ {
		if windowsDrive && index == 1 {
			continue
		}
		if !isBashTokenSafe(token[index], quote) {
			return "", false
		}
	}
	return token, true
}

func isBashHorizontalSpace(value byte) bool {
	return value == ' ' || value == '\t'
}

func isBashTokenDelimiter(value byte) bool {
	switch value {
	case ' ', '\t', ';', '&', '|', '<', '>', '(', ')', '`', '\\', '"', '\'':
		return true
	default:
		return false
	}
}

func isBashTokenSafe(value byte, quote bashPathQuote) bool {
	if quote == bashPathDoubleQuoted {
		return value != '$' && value != '`'
	}
	return isBashPathSafe(value)
}

func isBashPathSafe(value byte) bool {
	if value >= 0x80 || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
		return true
	}
	switch value {
	case '/', '.', '_', '-', '+':
		return true
	default:
		return false
	}
}

func unquotedBashExpansionSafe(value string) bool {
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case ' ', '\t', '\n', '*', '?', '[':
			return false
		}
	}
	return true
}

func bashPathsEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		left = filepath.ToSlash(left)
		right = filepath.ToSlash(right)
	}
	for len(left) > 1 && strings.HasSuffix(left, "/") {
		left = left[:len(left)-1]
	}
	for len(right) > 1 && strings.HasSuffix(right, "/") {
		right = right[:len(right)-1]
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

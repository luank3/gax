// Package bashcompress compacts recognized shell command output while
// preserving errors, failing tests, and a never-worse size invariant.
package bashcompress

import (
	"path/filepath"
	"strings"
)

// Invocation is a parsed single simple shell command.
type Invocation struct {
	Program string
	Args    []string
}

// Result is a compacted command output.
type Result struct {
	Text    string
	Omitted bool
}

// Parse extracts a single simple command; pipelines, chains (&&, ||, ;),
// and redirections return false so complex commands stay verbatim.
func Parse(command string) (Invocation, bool) {
	tokens, ok := tokenizeSimple(command)
	if !ok || len(tokens) == 0 {
		return Invocation{}, false
	}
	idx := 0
	for idx < len(tokens) && isEnvAssign(tokens[idx]) {
		idx++
	}
	if idx >= len(tokens) {
		return Invocation{}, false
	}
	prog := strings.ToLower(filepath.Base(tokens[idx]))
	return Invocation{Program: prog, Args: tokens[idx+1:]}, true
}

func isEnvAssign(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	return eq > 0 && !strings.HasPrefix(tok, "-")
}

func tokenizeSimple(s string) ([]string, bool) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			inToken = true
		case ' ', '\t', '\n', '\r':
			if inToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inToken = false
			}
		case '|', '&', ';', '<', '>', '`', '$':
			if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
				return nil, false
			}
			if ch != '$' {
				return nil, false
			}
			cur.WriteByte(ch)
			inToken = true
		default:
			cur.WriteByte(ch)
			inToken = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, true
}

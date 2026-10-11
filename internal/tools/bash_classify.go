package tools

import "strings"

type bashCommandClass uint8

const (
	bashUnknown bashCommandClass = iota
	bashExploration
	bashFilter
	bashInert
)

type bashCommandSpec struct {
	valueOptions     string
	requiredOperands int
}

var bashCommandSpecs = map[string]bashCommandSpec{
	"cat": {requiredOperands: 1}, "less": {valueOptions: "joP", requiredOperands: 1},
	"more": {valueOptions: "n", requiredOperands: 1}, "nl": {valueOptions: "bdfhilnpsvw", requiredOperands: 1},
	"grep": {valueOptions: "ABCDdmef", requiredOperands: 2}, "egrep": {valueOptions: "ABCDdmef", requiredOperands: 2},
	"fgrep": {valueOptions: "ABCDdmef", requiredOperands: 2}, "rg": {valueOptions: "ABCmtg", requiredOperands: 1},
	"ag": {valueOptions: "ABCm", requiredOperands: 1}, "ack": {valueOptions: "ABCm", requiredOperands: 1},
	"head": {valueOptions: "nc", requiredOperands: 1}, "tail": {valueOptions: "nc", requiredOperands: 1},
	"wc": {requiredOperands: 1}, "sort": {valueOptions: "kStTo", requiredOperands: 1},
	"uniq": {valueOptions: "fsw", requiredOperands: 1}, "cut": {valueOptions: "bcdf", requiredOperands: 1},
	"sed": {requiredOperands: 2}, "awk": {requiredOperands: 2}, "tac": {valueOptions: "s", requiredOperands: 1},
	"rev": {requiredOperands: 1}, "fold": {valueOptions: "w", requiredOperands: 1},
	"expand": {valueOptions: "t", requiredOperands: 1}, "unexpand": {valueOptions: "t", requiredOperands: 1},
	"paste": {valueOptions: "d", requiredOperands: 1}, "comm": {requiredOperands: 1},
	"join": {valueOptions: "12teoav", requiredOperands: 2}, "column": {valueOptions: "csoN", requiredOperands: 1},
}

func isExplorationBash(command string) bool {
	if strings.TrimSpace(command) == "" || bashCommandHasDisqualifier(command) {
		return false
	}
	state := bashClassifierState{}
	if !forEachBashSegment(command, func(segment string, previous bashSeparator) bool {
		if previous != bashPipe {
			state.statementHasProducer = false
		}
		if strings.TrimSpace(segment) == "" {
			return true
		}
		classification := classifyBashSegment(segment)
		if classification == bashUnknown {
			return false
		}
		if classification == bashFilter {
			return state.statementHasProducer
		}
		state.statementHasProducer = true
		if classification == bashExploration {
			state.hasSubstantive = true
		}
		return true
	}) {
		return false
	}
	return state.hasSubstantive
}

type bashClassifierState struct {
	hasSubstantive       bool
	statementHasProducer bool
}

type bashSeparator uint8

const (
	bashStart bashSeparator = iota
	bashPipe
	bashStatement
)

func forEachBashSegment(command string, visit func(string, bashSeparator) bool) bool {
	start, offset := 0, 0
	previous := bashStart
	var quote byte
	for offset < len(command) {
		c := command[offset]
		if quote != 0 {
			if quote == '"' && c == '\\' && offset+1 < len(command) {
				offset += 2
			} else if c == quote {
				quote = 0
				offset++
			} else {
				offset++
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			offset++
			continue
		}
		if c == '\\' && offset+1 < len(command) {
			offset += 2
			continue
		}
		length, separator := 0, bashStatement
		switch {
		case (c == '&' && byteAt(command, offset+1) == '&') || (c == '|' && byteAt(command, offset+1) == '|'):
			length = 2
		case c == ';' || c == '\n' || c == '\r':
			length = 1
		case c == '|':
			length, separator = 1, bashPipe
		}
		if length == 0 {
			offset++
			continue
		}
		if !visit(command[start:offset], previous) {
			return false
		}
		previous = separator
		offset += length
		start = offset
	}
	return visit(command[start:], previous)
}

func byteAt(value string, index int) byte {
	if index < 0 || index >= len(value) {
		return 0
	}
	return value[index]
}

func bashCommandHasDisqualifier(command string) bool {
	var quote byte
	for offset := 0; offset < len(command); offset++ {
		c := command[offset]
		if quote != 0 {
			if quote == '"' && c == '\\' && offset+1 < len(command) {
				offset++
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			if quote == '"' && (c == '`' || c == '$' && byteAt(command, offset+1) == '(') {
				return true
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '\\' && offset+1 < len(command) {
			offset++
			continue
		}
		if c == '`' || c == '$' && byteAt(command, offset+1) == '(' ||
			(c == '<' || c == '>') && byteAt(command, offset+1) == '(' || c == '<' && byteAt(command, offset+1) == '<' {
			return true
		}
		if c == '&' && byteAt(command, offset+1) != '&' &&
			(offset == 0 || command[offset-1] != '>' && command[offset-1] != '&') {
			return true
		}
		if c == '>' {
			end := allowedBashStderrRedirectEnd(command, offset)
			if end == 0 {
				return true
			}
			offset = end - 1
		}
	}
	return false
}

func allowedBashStderrRedirectEnd(command string, offset int) int {
	if offset < 1 || command[offset-1] != '2' || offset >= 2 && !strings.ContainsRune(" \t\n\r&|;", rune(command[offset-2])) {
		return 0
	}
	if byteAt(command, offset+1) == '&' && byteAt(command, offset+2) == '1' {
		return offset + 3
	}
	target := offset + 1
	for target < len(command) && (command[target] == ' ' || command[target] == '\t') {
		target++
	}
	if strings.HasPrefix(command[target:], "/dev/null") {
		return target + len("/dev/null")
	}
	return 0
}

func bashWords(value string) []string {
	words := make([]string, 0, 8)
	for offset := 0; offset < len(value); {
		for offset < len(value) && isBashWhitespace(value[offset]) {
			offset++
		}
		if offset >= len(value) {
			break
		}
		var word strings.Builder
		var quote byte
		for offset < len(value) {
			c := value[offset]
			if quote != 0 {
				if c == quote {
					quote = 0
					offset++
					continue
				}
				if quote == '"' && c == '\\' && offset+1 < len(value) {
					word.WriteByte(value[offset+1])
					offset += 2
					continue
				}
				word.WriteByte(c)
				offset++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				offset++
				continue
			}
			if c == '\\' && offset+1 < len(value) {
				word.WriteByte(value[offset+1])
				offset += 2
				continue
			}
			if isBashWhitespace(c) {
				break
			}
			word.WriteByte(c)
			offset++
		}
		if word.Len() > 0 {
			words = append(words, word.String())
		}
	}
	return words
}

func isBashWhitespace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isBashVariableAssignment(word string) bool {
	if word == "" || !(word[0] == '_' || word[0] >= 'a' && word[0] <= 'z' || word[0] >= 'A' && word[0] <= 'Z') {
		return false
	}
	return strings.ContainsRune(word, '=')
}

func classifyBashSegment(segment string) bashCommandClass {
	words := bashWords(segment)
	if len(words) == 0 {
		return bashUnknown
	}
	index := 0
	consumedPrefix := false
	for index < len(words) {
		command := words[index]
		next := index + 1
		neutral := false
		switch {
		case command == "cd" || command == "pushd":
			neutral = true
			for next < len(words) {
				if !strings.HasPrefix(words[next], "-") {
					next++
					break
				}
				next++
			}
		case command == "popd" || isBashVariableAssignment(command):
			neutral = true
		case command == "env":
			neutral = true
			for next < len(words) && isBashVariableAssignment(words[next]) {
				next++
			}
		}
		if !neutral {
			break
		}
		consumedPrefix = true
		index = next
	}
	if index >= len(words) {
		if consumedPrefix {
			return bashExploration
		}
		return bashUnknown
	}
	body := words[index:]
	command := body[0]
	classification := classifyBashCommand(command)
	if isBashInertCommand(command) {
		classification = bashInert
	} else if command == "git" {
		classification = classifyBashGit(body)
	} else if classification != bashUnknown {
		if bashKnownCommandWrites(command, body) {
			classification = bashUnknown
		} else if spec, ok := bashCommandSpecs[command]; ok && countBashOperands(body, spec) < spec.requiredOperands {
			classification = bashFilter
		}
	} else if command == "xargs" {
		classification = classifyBashXargs(body)
	} else if command == "sed" && bashSedEditsInPlace(body) {
		classification = bashUnknown
	} else if isBashFormatFilter(command) {
		classification = classifyBashFormatFilter(command, body)
	}
	return classification
}

func classifyBashCommand(command string) bashCommandClass {
	switch command {
	case "cat", "less", "more", "nl", "grep", "egrep", "fgrep", "rg", "ag", "ack", "ls", "eza", "exa", "tree", "find", "fd", "stat", "file", "pwd", "realpath", "readlink", "which", "whereis", "basename", "dirname", "du", "df", "id", "whoami", "hostname", "uname":
		return bashExploration
	default:
		return bashUnknown
	}
}

func isBashInertCommand(command string) bool {
	switch command {
	case "echo", "printf", "true", "false", ":":
		return true
	default:
		return false
	}
}

func isBashFormatFilter(command string) bool {
	switch command {
	case "wc", "sort", "uniq", "cut", "tr", "awk", "sed", "head", "tail", "tac", "rev", "fold", "expand", "unexpand", "paste", "comm", "join", "column":
		return true
	default:
		return false
	}
}

func countBashOperands(words []string, spec bashCommandSpec) int {
	count := 0
	for index := 1; index < len(words); index++ {
		token := words[index]
		if len(token) > 1 && token[0] == '-' && token[1] != '-' {
			consumeNext := false
			for offset := 1; offset < len(token); offset++ {
				if strings.ContainsRune(spec.valueOptions, rune(token[offset])) {
					consumeNext = offset+1 == len(token)
					break
				}
			}
			if consumeNext && index+1 < len(words) {
				index++
			}
			continue
		}
		if strings.HasPrefix(token, "--") && len(token) > 2 {
			continue
		}
		count++
	}
	return count
}

func bashKnownCommandWrites(command string, words []string) bool {
	if command == "find" || command == "fd" {
		for _, word := range words[1:] {
			if isBashMutatingSearchOption(word) {
				return true
			}
		}
	}
	if command == "tree" && hasBashOutputOption(words[1:]) {
		return true
	}
	if command == "less" || command == "more" {
		for _, word := range words[1:] {
			if len(word) > 1 && word[0] == '-' && (word[1] == 'o' || word[1] == 'O') || word == "--log-file" || strings.HasPrefix(word, "--log-file=") {
				return true
			}
		}
	}
	return false
}

func isBashMutatingSearchOption(token string) bool {
	switch token {
	case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls", "-x", "-X", "--exec", "--exec-batch":
		return true
	default:
		return strings.HasPrefix(token, "--exec=") || strings.HasPrefix(token, "--exec-batch=")
	}
}

func hasBashOutputOption(words []string) bool {
	for _, word := range words {
		if strings.HasPrefix(word, "-o") || word == "--output" || strings.HasPrefix(word, "--output=") {
			return true
		}
	}
	return false
}

func classifyBashFormatFilter(command string, words []string) bashCommandClass {
	if command == "tr" {
		return bashFilter
	}
	spec, ok := bashCommandSpecs[command]
	if !ok {
		return bashUnknown
	}
	if countBashOperands(words, spec) < spec.requiredOperands {
		return bashFilter
	}
	if command == "sort" && hasBashOutputOption(words[1:]) || command == "awk" && bashAwkWrites(words[1:]) || command == "sed" && bashSedFilterWrites(words[1:]) {
		return bashUnknown
	}
	return bashExploration
}

func bashSedScriptWrites(script string) bool {
	for offset := 0; offset < len(script); offset++ {
		command := script[offset]
		if command != 'w' && command != 'W' && command != 'e' {
			continue
		}
		next := byteAt(script, offset+1)
		if next != ' ' && next != '\t' && next != 0 && next != ';' && next != '\n' && next != '}' {
			continue
		}
		start := offset
		for start > 0 && (script[start-1] == ' ' || script[start-1] == '\t') {
			start--
		}
		if start == 0 || !isBashAlnum(script[start-1]) && script[start-1] != '-' {
			return true
		}
	}
	return false
}

func bashAwkWrites(words []string) bool {
	for index, token := range words {
		if token == "-i" && index+1 < len(words) && words[index+1] == "inplace" {
			return true
		}
		if strings.HasPrefix(token, "-i") && strings.TrimPrefix(token, "-i") == "inplace" || strings.Contains(token, "system(") {
			return true
		}
	}
	return false
}

func bashSedFilterWrites(words []string) bool {
	for _, word := range words {
		if bashSedScriptWrites(word) {
			return true
		}
	}
	return false
}

func bashSedEditsInPlace(words []string) bool {
	for _, word := range words[1:] {
		if strings.HasPrefix(word, "--in-place") {
			return true
		}
		if len(word) > 1 && word[0] == '-' && word[1] != '-' {
			for _, option := range word[1:] {
				if option == '.' {
					break
				}
				if option == 'i' {
					return true
				}
			}
		}
	}
	return false
}

func isBashAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func classifyBashGit(words []string) bashCommandClass {
	valueOptions := map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true}
	for index := 1; index < len(words); index++ {
		token := words[index]
		if !strings.HasPrefix(token, "-") {
			if !isReadOnlyBashGitSubcommand(token) {
				return bashUnknown
			}
			if (token == "log" || token == "show" || token == "diff") && hasBashOutputOption(words[index+1:]) {
				return bashUnknown
			}
			return bashExploration
		}
		if valueOptions[token] {
			index++
		}
	}
	return bashUnknown
}

func isReadOnlyBashGitSubcommand(command string) bool {
	switch command {
	case "grep", "ls-files", "ls-tree", "log", "show", "diff", "status", "blame", "rev-parse", "describe", "shortlog", "cat-file", "rev-list", "show-ref", "for-each-ref":
		return true
	default:
		return false
	}
}

func classifyBashXargs(words []string) bashCommandClass {
	for _, token := range words[1:] {
		if strings.HasPrefix(token, "-") && len(token) > 1 {
			continue
		}
		return classifyBashCommand(token)
	}
	return bashUnknown
}

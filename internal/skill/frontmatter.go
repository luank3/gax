package skill

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func frontmatterScalarLine(content []byte, key string) string {
	cursor := 0
	switch {
	case strings.HasPrefix(string(content), "---\n"):
		cursor = 4
	case strings.HasPrefix(string(content), "---\r\n"):
		cursor = 5
	default:
		return ""
	}

	valueStart, valueEnd := -1, -1
	inValue := false
	for cursor < len(content) {
		lineEnd := cursor
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		line := content[cursor:lineEnd]
		if frontmatterFence(line) {
			if valueStart < 0 {
				return ""
			}
			return foldFrontmatterLine(decodeFrontmatterScalar(content[valueStart:valueEnd]))
		}
		if inValue && (len(line) == 0 || frontmatterSpace(line[0])) {
			valueEnd = lineEnd
		} else if valueStart < 0 && frontmatterKeyLine(line, key) {
			valueStart = cursor + len(key) + 1
			valueEnd = lineEnd
			inValue = true
		} else {
			inValue = false
		}
		if lineEnd == len(content) {
			break
		}
		cursor = lineEnd + 1
	}
	return ""
}

func frontmatterFence(line []byte) bool {
	return string(line) == "---" || string(line) == "---\r"
}

func frontmatterKeyLine(line []byte, key string) bool {
	if len(line) <= len(key) || string(line[:len(key)]) != key || line[len(key)] != ':' {
		return false
	}
	return len(line) == len(key)+1 || frontmatterSpace(line[len(key)+1])
}

func frontmatterSpace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == '\r'
}

func decodeFrontmatterScalar(value []byte) []byte {
	position := 0
	for {
		for position < len(value) && frontmatterSpace(value[position]) {
			position++
		}
		if position == len(value) || value[position] != '#' {
			break
		}
		position = frontmatterLineEnd(value, position)
	}
	if position == len(value) {
		return nil
	}

	switch value[position] {
	case '"':
		decoded, _, ok := decodeFrontmatterDoubleQuoted(value, position+1)
		if !ok {
			return nil
		}
		return decoded
	case '\'':
		decoded, ok := decodeFrontmatterSingleQuoted(value, position+1)
		if !ok {
			return nil
		}
		return decoded
	case '|', '>':
		position++
		for position < len(value) && (value[position] == '+' || value[position] == '-' || value[position] >= '0' && value[position] <= '9') {
			position++
		}
		for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
			position++
		}
		if position < len(value) && value[position] == '#' {
			position = frontmatterLineEnd(value, position)
		}
		if position < len(value) && value[position] != '\r' && value[position] != '\n' {
			return nil
		}
		return cleanFrontmatterBytes(value[position:])
	default:
		start := position
		for position < len(value) {
			if value[position] == '#' && position > start && frontmatterSpace(value[position-1]) {
				break
			}
			position++
		}
		return cleanFrontmatterBytes(value[start:position])
	}
}

func decodeFrontmatterDoubleQuoted(value []byte, position int) ([]byte, int, bool) {
	decoded := make([]byte, 0, len(value)-position)
	for position < len(value) {
		character := value[position]
		position++
		if character == '"' {
			return decoded, position, true
		}
		if character == '\\' && position < len(value) {
			decoded, position = decodeFrontmatterEscape(decoded, value, position)
			continue
		}
		decoded = append(decoded, character)
	}
	return nil, position, false
}

func decodeFrontmatterSingleQuoted(value []byte, position int) ([]byte, bool) {
	decoded := make([]byte, 0, len(value)-position)
	for position < len(value) {
		if value[position] == '\'' {
			if position+1 == len(value) || value[position+1] != '\'' {
				return decoded, true
			}
			decoded = append(decoded, '\'')
			position += 2
			continue
		}
		decoded = append(decoded, value[position])
		position++
	}
	return nil, false
}

func decodeFrontmatterEscape(decoded, value []byte, position int) ([]byte, int) {
	if value[position] == '\r' || value[position] == '\n' {
		if value[position] == '\r' && position+1 < len(value) && value[position+1] == '\n' {
			position++
		}
		position++
		for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
			position++
		}
		return decoded, position
	}
	if escaped, ok := namedFrontmatterEscape(value[position]); ok {
		return append(decoded, escaped...), position + 1
	}
	digits := 0
	switch value[position] {
	case 'x':
		digits = 2
	case 'u':
		digits = 4
	case 'U':
		digits = 8
	}
	if digits != 0 && position+1+digits <= len(value) {
		codepoint, err := strconv.ParseUint(string(value[position+1:position+1+digits]), 16, 32)
		if err == nil && codepoint <= utf8.MaxRune && (codepoint < 0xD800 || codepoint > 0xDFFF) {
			return utf8.AppendRune(decoded, rune(codepoint)), position + 1 + digits
		}
	}
	return append(decoded, '\\'), position
}

func namedFrontmatterEscape(character byte) ([]byte, bool) {
	var codepoint rune
	switch character {
	case '0':
		codepoint = 0
	case 'a':
		codepoint = 0x07
	case 'b':
		codepoint = 0x08
	case 't', '\t':
		codepoint = '\t'
	case 'n':
		codepoint = '\n'
	case 'v':
		codepoint = 0x0B
	case 'f':
		codepoint = 0x0C
	case 'r':
		codepoint = '\r'
	case 'e':
		codepoint = 0x1B
	case ' ':
		codepoint = ' '
	case '"':
		codepoint = '"'
	case '/':
		codepoint = '/'
	case '\\':
		codepoint = '\\'
	case 'N':
		codepoint = 0x85
	case '_':
		codepoint = 0xA0
	case 'L':
		codepoint = 0x2028
	case 'P':
		codepoint = 0x2029
	default:
		return nil, false
	}
	return utf8.AppendRune(nil, codepoint), true
}

func frontmatterLineEnd(value []byte, position int) int {
	for position < len(value) && value[position] != '\n' {
		position++
	}
	return position
}

func cleanFrontmatterBytes(value []byte) []byte {
	return []byte(cleanText(string(value)))
}

func foldFrontmatterLine(value []byte) string {
	value = cleanFrontmatterBytes(value)
	var folded strings.Builder
	folded.Grow(len(value))
	pendingSpace := false
	for position := 0; position < len(value); {
		character, size := utf8.DecodeRune(value[position:])
		if frontmatterLineBreak(character) {
			pendingSpace = folded.Len() > 0
			position += size
			continue
		}
		if pendingSpace {
			folded.WriteByte(' ')
			pendingSpace = false
		}
		folded.Write(value[position : position+size])
		position += size
	}
	return folded.String()
}

func frontmatterLineBreak(character rune) bool {
	switch character {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0x2028, 0x2029:
		return true
	default:
		return false
	}
}

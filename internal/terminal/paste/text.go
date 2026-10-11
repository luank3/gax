// Package paste handles clipboard images, text, and file drops for the terminal editor.
package paste

import (
	"strings"
)

func NormalizeText(text []byte) string {
	normalized := make([]byte, 0, len(text))
	for index, value := range text {
		if value == 0 {
			continue
		}
		if value == '\r' {
			if index+1 < len(text) && text[index+1] == '\n' {
				continue
			}
			value = '\n'
		}
		normalized = append(normalized, value)
	}
	return string(normalized)
}

func FileURIsToPaths(text string) (string, bool) {
	var output strings.Builder
	converted := 0
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		path, ok := fileURIToPath(line)
		if !ok {
			return "", false
		}
		if converted > 0 {
			output.WriteByte('\n')
		}
		if isImagePath(path) {
			output.WriteString("[pasted image: ")
			output.WriteString(path)
			output.WriteByte(']')
		} else {
			output.WriteString(path)
		}
		converted++
	}
	if converted == 0 {
		return "", false
	}
	output.WriteByte(' ')
	return output.String(), true
}

func fileURIToPath(uri string) (string, bool) {
	const scheme = "file://"
	if !strings.HasPrefix(uri, scheme) {
		return "", false
	}
	path := uri[len(scheme):]
	if !strings.HasPrefix(path, "/") {
		slash := strings.IndexByte(path, '/')
		if slash < 0 || path[:slash] != "localhost" {
			return "", false
		}
		path = path[slash:]
	}
	if len(path) == 0 || path[0] != '/' {
		return "", false
	}

	decoded := make([]byte, 0, len(path))
	for index := 0; index < len(path); {
		if path[index] == '%' && index+2 < len(path) {
			if value, ok := parseHexByte(path[index+1], path[index+2]); ok {
				if value == 0 {
					return "", false
				}
				decoded = append(decoded, value)
				index += 3
				continue
			}
		}
		decoded = append(decoded, path[index])
		index++
	}
	return string(decoded), true
}

func parseHexByte(high, low byte) (byte, bool) {
	upper := func(value byte) (byte, bool) {
		switch {
		case value >= '0' && value <= '9':
			return value - '0', true
		case value >= 'a' && value <= 'f':
			return value - 'a' + 10, true
		case value >= 'A' && value <= 'F':
			return value - 'A' + 10, true
		default:
			return 0, false
		}
	}
	highValue, highOK := upper(high)
	lowValue, lowOK := upper(low)
	if !highOK || !lowOK {
		return 0, false
	}
	return highValue<<4 | lowValue, true
}

func isImagePath(path string) bool {
	extension := path[strings.LastIndexByte(path, '.')+1:]
	switch strings.ToLower(extension) {
	case "png", "jpg", "jpeg", "gif", "webp":
		return true
	default:
		return false
	}
}

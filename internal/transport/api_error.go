package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const maxAPIErrorBytes = 200

func FormatAPIError(status int, body string) string {
	if body == "" {
		return formatStatus(status, "")
	}
	content := body
	if sseData := unwrapSSEError(body); sseData != nil {
		content = *sseData
	}
	if message, ok := jsonMessage([]byte(content)); ok && message != "" {
		return formatStatus(status, truncateUTF8(message, maxAPIErrorBytes))
	}
	return formatStatus(status, truncateUTF8(stripHTMLAndFlatten(content), maxAPIErrorBytes))
}

func FormatModelListError(name, baseURL string, hasKey bool, status int) string {
	if name == "" {
		name = "provider"
	}
	switch {
	case status == 401 || status == 403:
		if hasKey {
			return fmt.Sprintf("%s rejected the API key (HTTP %d) — check it and retry", name, status)
		}
		return fmt.Sprintf("%s requires an API key (HTTP %d) — none is configured", name, status)
	case status >= 200 && status < 300:
		return fmt.Sprintf("%s sent an empty or truncated /models response", name)
	case status != 0:
		return fmt.Sprintf("listing %s models failed (HTTP %d)", name, status)
	default:
		return fmt.Sprintf("could not reach %s at %s", name, baseURL)
	}
}

func extractJSONMessage(data []byte) string {
	message, _ := jsonMessage(data)
	return message
}

func jsonMessage(data []byte) (string, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return "", false
	}
	if raw := object["error"]; len(raw) > 0 {
		if raw[0] == '"' {
			var message string
			if json.Unmarshal(raw, &message) == nil {
				return message, true
			}
		} else if raw[0] == '{' {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil {
				if message, ok := rawJSONString(nested["message"]); ok {
					return message, true
				}
			}
		}
	}
	return rawJSONString(object["message"])
}

func rawJSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func unwrapSSEError(body string) *string {
	var fallback, foundError *string
	parser := NewSSEParser(func(event SSEEvent) bool {
		if event.Data == "" {
			return true
		}
		_, hasMessage := jsonMessage([]byte(event.Data))
		isError := event.Name == "error" || hasMessage
		if isError {
			copy := event.Data
			foundError = &copy
			return false
		}
		if fallback == nil {
			copy := event.Data
			fallback = &copy
		}
		return true
	})
	parser.Feed([]byte(body))
	parser.Finalize()
	if foundError != nil {
		return foundError
	}
	return fallback
}

func formatStatus(status int, message string) string {
	if status > 0 {
		if message != "" {
			return fmt.Sprintf("HTTP %d: %s", status, message)
		}
		return fmt.Sprintf("HTTP %d", status)
	}
	if message != "" {
		return message
	}
	return "request failed"
}

func truncateUTF8(message string, maxBytes int) string {
	if len(message) <= maxBytes {
		return message
	}
	end := maxBytes
	for end > 0 && message[end]&0xc0 == 0x80 {
		end--
	}
	return message[:end] + "..."
}

func stripHTMLAndFlatten(body string) string {
	var output bytes.Buffer
	inTag := false
	previousSpace := true
	var hiddenElement string
	for index := 0; index < len(body); index++ {
		value := body[index]
		if hiddenElement != "" {
			if value == '<' && index+2 < len(body) && body[index+1] == '/' &&
				isTagStart(body[index+2:], hiddenElement) {
				hiddenElement = ""
			} else {
				continue
			}
		}
		if inTag {
			if value == '>' {
				inTag = false
			}
			continue
		}
		if value == '<' && index+1 < len(body) && isTagLead(body[index+1]) {
			inTag = true
			if isTagStart(body[index+1:], "style") {
				hiddenElement = "style"
			} else if isTagStart(body[index+1:], "script") {
				hiddenElement = "script"
			}
			continue
		}
		if value <= 0x20 || value == ' ' {
			if !previousSpace {
				output.WriteByte(' ')
				previousSpace = true
			}
			continue
		}
		output.WriteByte(value)
		previousSpace = false
	}
	return strings.TrimSuffix(output.String(), " ")
}

func isTagLead(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '/' || value == '!' || value == '?'
}

func isTagStart(text, tag string) bool {
	if len(text) < len(tag) {
		return false
	}
	if !strings.EqualFold(text[:len(tag)], tag) {
		return false
	}
	if len(text) == len(tag) {
		return false
	}
	switch text[len(tag)] {
	case ' ', '\t', '\n', '\r', '>', '/':
		return true
	default:
		return false
	}
}

package anthropic

import (
	"encoding/json"
	"strconv"
	"strings"
)

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func jsonString(raw json.RawMessage) *string {
	if len(raw) == 0 || raw[0] != '"' {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func jsonInt(raw json.RawMessage) *int64 {
	if len(raw) == 0 || raw[0] == '"' || strings.ContainsAny(string(raw), ".eE") {
		return nil
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func integerValue(raw json.RawMessage) int64 {
	if value := jsonInt(raw); value != nil {
		return *value
	}
	return 0
}

func firstString(current *string, raw json.RawMessage) *string {
	if current != nil {
		return current
	}
	value := jsonString(raw)
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string { return &value }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

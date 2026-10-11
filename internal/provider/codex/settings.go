package codex

import (
	"os"
	"strings"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
)

// LoadDefaults reads Codex's top-level model defaults from ~/.codex/config.toml.
func LoadDefaults(home string) (model, effort string) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", ""
		}
	}
	data, err := systemfs.ReadFile(systempath.ExpandHome("~/.codex/config.toml", home))
	if err != nil {
		return "", ""
	}
	return topLevelTOMLString(string(data), "model"), topLevelTOMLString(string(data), "model_reasoning_effort")
}

func topLevelTOMLString(contents, key string) string {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			return ""
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) < 2 || value[0] != '\'' && value[0] != '"' {
			return ""
		}
		quote := value[0]
		var result strings.Builder
		for index := 1; index < len(value); index++ {
			current := value[index]
			if current == quote {
				return result.String()
			}
			if current == '\\' && quote == '"' && index+1 < len(value) {
				index++
				switch value[index] {
				case 'b':
					result.WriteByte('\b')
				case 't':
					result.WriteByte('\t')
				case 'n':
					result.WriteByte('\n')
				case 'f':
					result.WriteByte('\f')
				case 'r':
					result.WriteByte('\r')
				default:
					result.WriteByte(value[index])
				}
				continue
			}
			result.WriteByte(current)
		}
		return ""
	}
	return ""
}

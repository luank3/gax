package session

import (
	"os"
	"path/filepath"
	"strings"
)

// AppendHistory appends a prompt line to the per-cwd history file.
func AppendHistory(stateDir, cwd, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || stateDir == "" {
		return nil
	}
	dir := Dir(stateDir, cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "history"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	escaped := strings.ReplaceAll(prompt, "\n", `\n`)
	_, err = f.WriteString(escaped + "\n")
	return err
}

// LoadHistory returns per-cwd prompt history entries, oldest first.
func LoadHistory(stateDir, cwd string) []string {
	if stateDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(Dir(stateDir, cwd), "history"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		out = append(out, strings.ReplaceAll(line, `\n`, "\n"))
	}
	return out
}

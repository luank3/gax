// Package theme defines semantic terminal color roles and identity tints.
package theme

import "os"

// Palette holds ANSI SGR escape sequences for semantic UI roles.
type Palette struct {
	Reset   string
	Dim     string
	Bold    string
	Accent  string
	User    string
	Tool    string
	Error   string
	DiffAdd string
	DiffDel string
}

// Resolve builds a Palette from theme ("auto", "dark", "light", "ansi", "off") and tint.
func Resolve(themeName, tint string, isTTY bool) Palette {
	if !isTTY || themeName == "off" || os.Getenv("NO_COLOR") != "" {
		return Palette{}
	}
	if themeName == "ansi" {
		return Palette{
			Reset:   "\x1b[0m",
			Dim:     "\x1b[2m",
			Bold:    "\x1b[1m",
			Accent:  "\x1b[36m",
			User:    "\x1b[34m",
			Tool:    "\x1b[33m",
			Error:   "\x1b[31m",
			DiffAdd: "\x1b[32m",
			DiffDel: "\x1b[31m",
		}
	}
	accent := "\x1b[38;5;37m" // teal
	switch tint {
	case "violet":
		accent = "\x1b[38;5;141m"
	case "rose":
		accent = "\x1b[38;5;204m"
	case "sage":
		accent = "\x1b[38;5;108m"
	}
	if themeName == "light" {
		return Palette{
			Reset:   "\x1b[0m",
			Dim:     "\x1b[38;5;242m",
			Bold:    "\x1b[1m",
			Accent:  accent,
			User:    "\x1b[38;5;25m",
			Tool:    "\x1b[38;5;130m",
			Error:   "\x1b[38;5;160m",
			DiffAdd: "\x1b[38;5;28m",
			DiffDel: "\x1b[38;5;160m",
		}
	}
	return Palette{
		Reset:   "\x1b[0m",
		Dim:     "\x1b[38;5;245m",
		Bold:    "\x1b[1m",
		Accent:  accent,
		User:    "\x1b[38;5;75m",
		Tool:    "\x1b[38;5;179m",
		Error:   "\x1b[38;5;203m",
		DiffAdd: "\x1b[38;5;78m",
		DiffDel: "\x1b[38;5;203m",
	}
}

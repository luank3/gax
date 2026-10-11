// Package theme provides the semantic terminal roles shared by the UI renderers.
package theme

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	reset        = "\x1b[0m"
	bold         = "\x1b[1m"
	dim          = "\x1b[2m"
	boldOff      = "\x1b[22m"
	underline    = "\x1b[4m"
	underlineOff = "\x1b[24m"
	fgDefault    = "\x1b[39m"
	red          = "\x1b[31m"
	green        = "\x1b[32m"
	yellow       = "\x1b[33m"
	cyan         = "\x1b[36m"
	brightPink   = "\x1b[95m"
)

type Role uint8

const (
	Accent Role = iota
	Chrome
	ChromeDim
	Stance
	CodeInline
	CodeBlock
	Heading
	Link
	Add
	Remove
	OK
	Error
	Warn
	RoleCount
)

type roleStyle struct {
	open  string
	close string
}

// Palette is a resolved theme and tint pair. Its role table is immutable after construction.
type Palette struct {
	name  string
	tint  string
	roles [RoleCount]roleStyle
}

func (palette Palette) Name() string     { return palette.name }
func (palette Palette) TintName() string { return palette.tint }

func (palette Palette) Open(role Role) string {
	if role >= RoleCount {
		return ""
	}
	return palette.roles[role].open
}

func (palette Palette) Close(role Role) string {
	if role >= RoleCount {
		return ""
	}
	return palette.roles[role].close
}

// TintOpen returns the model-stance color for tint in palettes that support tinting.
func (palette Palette) TintOpen(tint string) string {
	if tint == "" || palette.name != "dark" && palette.name != "light" {
		return ""
	}
	candidate, warnings := New(palette.name, tint, func(string) (string, bool) { return "", false })
	if len(warnings) != 0 {
		return ""
	}
	return candidate.Open(Stance)
}

// New resolves a theme and tint. Lookup may be nil to read the process environment.
func New(name, tint string, lookup func(string) (string, bool)) (Palette, []string) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	warnings := make([]string, 0, 2)
	requestedName, requestedTint := name, tint
	name = strings.ToLower(name)
	if name == "" {
		name = "auto"
	}
	if name == "auto" {
		name = detect(lookup)
	} else if name != "ansi" && name != "dark" && name != "light" && name != "off" {
		warnings = append(warnings, "unknown theme '"+requestedName+"' (expected auto, dark, light, ansi, or off)")
		name = detect(lookup)
	}
	tint = strings.ToLower(tint)
	if tint == "" {
		tint = "teal"
	}
	if tint != "teal" && tint != "violet" && tint != "rose" && tint != "sage" {
		warnings = append(warnings, "unknown tint '"+requestedTint+"' (expected teal, violet, rose, or sage)")
		tint = "teal"
	}
	palette := Palette{name: name, tint: tint}
	switch name {
	case "ansi":
		palette.set(Accent, brightPink, fgDefault)
		palette.set(Chrome, cyan, fgDefault)
		palette.set(ChromeDim, dim+cyan, fgDefault+boldOff)
		palette.set(Stance, cyan, fgDefault)
		palette.set(CodeInline, cyan, fgDefault)
		palette.set(CodeBlock, dim, boldOff)
		palette.set(Heading, bold, boldOff)
		palette.set(Link, underline, underlineOff)
		palette.set(Add, green, fgDefault)
		palette.set(Remove, red, fgDefault)
		palette.set(OK, green, fgDefault)
		palette.set(Error, red, fgDefault)
		palette.set(Warn, yellow, fgDefault)
	case "dark":
		palette.set(Accent, indexed(173), fgDefault)
		palette.set(Chrome, indexed(37), fgDefault)
		palette.set(ChromeDim, indexed(23), fgDefault)
		palette.set(Stance, indexed(38), fgDefault)
		palette.set(CodeInline, indexed(38), fgDefault)
		palette.set(CodeBlock, indexed(31), fgDefault)
		palette.set(Heading, bold+indexed(38), boldOff+fgDefault)
		palette.set(Link, link256(31), "\x1b[24;39m")
		palette.set(Add, indexed(34), fgDefault)
		palette.set(Remove, indexed(160), fgDefault)
		palette.set(OK, indexed(28), fgDefault)
		palette.set(Error, indexed(160), fgDefault)
		palette.set(Warn, indexed(178), fgDefault)
	case "light":
		palette.set(Accent, indexed(130), fgDefault)
		palette.set(Chrome, indexed(30), fgDefault)
		palette.set(ChromeDim, indexed(66), fgDefault)
		palette.set(Stance, indexed(31), fgDefault)
		palette.set(CodeInline, indexed(31), fgDefault)
		palette.set(CodeBlock, indexed(31), fgDefault)
		palette.set(Heading, bold+indexed(31), boldOff+fgDefault)
		palette.set(Link, link256(31), "\x1b[24;39m")
		palette.set(Add, indexed(28), fgDefault)
		palette.set(Remove, indexed(124), fgDefault)
		palette.set(OK, indexed(28), fgDefault)
		palette.set(Error, indexed(160), fgDefault)
		palette.set(Warn, indexed(136), fgDefault)
	case "off":
		palette.set(ChromeDim, dim, boldOff)
		palette.set(CodeBlock, dim, boldOff)
		palette.set(Heading, bold, boldOff)
		palette.set(Link, underline, underlineOff)
	}
	palette.applyTint()
	return palette, warnings
}

func (palette *Palette) set(role Role, open, close string) {
	palette.roles[role] = roleStyle{open: open, close: close}
}

func (palette *Palette) applyTint() {
	if palette.name != "dark" && palette.name != "light" {
		return
	}
	var stance, inline, block, heading, link string
	switch palette.tint {
	case "violet":
		if palette.name == "dark" {
			stance, inline, block, heading, link = indexed(140), indexed(140), indexed(97), bold+indexed(140), link256(97)
		} else {
			stance, inline, block, heading, link = indexed(97), indexed(97), indexed(97), bold+indexed(97), link256(97)
		}
	case "rose":
		if palette.name == "dark" {
			stance, inline, block, heading, link = indexed(211), indexed(211), indexed(168), bold+indexed(211), link256(168)
		} else {
			stance, inline, block, heading, link = indexed(132), indexed(132), indexed(132), bold+indexed(132), link256(132)
		}
	case "sage":
		if palette.name == "dark" {
			stance, inline, block, heading, link = indexed(114), indexed(114), indexed(71), bold+indexed(114), link256(71)
		} else {
			stance, inline, block, heading, link = indexed(71), indexed(71), indexed(71), bold+indexed(71), link256(71)
		}
	default:
		return
	}
	palette.roles[Stance].open = stance
	palette.roles[CodeInline].open = inline
	palette.roles[CodeBlock].open = block
	palette.roles[Heading].open = heading
	palette.roles[Link].open = link
}

func indexed(color int) string { return "\x1b[38;5;" + strconv.Itoa(color) + "m" }
func link256(color int) string { return "\x1b[4;38;5;" + strconv.Itoa(color) + "m" }

func detect(lookup func(string) (string, bool)) string {
	if value, ok := lookup("NO_COLOR"); ok && value != "" {
		return "off"
	}
	term, _ := lookup("TERM")
	if term == "" || term == "dumb" {
		return "off"
	}
	colorterm, _ := lookup("COLORTERM")
	if !strings.Contains(term, "256color") && colorterm == "" {
		return "ansi"
	}
	colorfgbg, _ := lookup("COLORFGBG")
	if separator := strings.LastIndexByte(colorfgbg, ';'); separator >= 0 {
		background := colorfgbg[separator+1:]
		if background == "7" || background == "15" {
			return "light"
		}
	}
	return "dark"
}

var (
	activePalette atomic.Pointer[Palette]
	configureMu   sync.Mutex
	lastBadTheme  string
	lastBadTint   string
)

func init() {
	initial, _ := New("ansi", "teal", func(string) (string, bool) { return "", false })
	activePalette.Store(&initial)
}

// Configure resolves the active process palette and returns newly observed configuration warnings.
func Configure(name, tint string) []string {
	palette, warnings := New(name, tint, nil)
	configureMu.Lock()
	defer configureMu.Unlock()
	filtered := warnings[:0]
	for _, warning := range warnings {
		if strings.HasPrefix(warning, "unknown theme ") {
			if name == lastBadTheme {
				continue
			}
			lastBadTheme = name
		} else {
			if tint == lastBadTint {
				continue
			}
			lastBadTint = tint
		}
		filtered = append(filtered, warning)
	}
	activePalette.Store(&palette)
	return filtered
}

func Current() Palette            { return *activePalette.Load() }
func Open(role Role) string       { return Current().Open(role) }
func Close(role Role) string      { return Current().Close(role) }
func TintOpen(tint string) string { return Current().TintOpen(tint) }

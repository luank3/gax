// Package picker contains the selection state shared by terminal pickers.
package picker

import (
	"strings"
	"unicode/utf8"

	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	MarkerCells             = 2
	CurrentTagCells         = 11
	DetailSeparatorCells    = 2
	DimDetailSeparatorCells = 3
)

type Direction int

const (
	Previous Direction = -1
	Next     Direction = 1
)

type Item struct {
	Label       string
	Detail      string
	Dim         bool
	Current     bool
	LabelColor  string
	Description string
}

type Options struct {
	Title              string
	Items              []Item
	EmptyMessage       string
	InitialIndex       int
	RepeatClippedLabel bool
	DisplayColumns     func(int) int
}

// Core stores the query, filtered item indexes, selection, and visible range.
type Core struct {
	options      Options
	query        []byte
	matches      []int
	selection    int
	firstVisible int
	viewportRows int
}

func NewCore(options Options) *Core {
	core := &Core{options: options, matches: make([]int, 0, len(options.Items))}
	core.updateMatches()
	core.SelectItem(options.InitialIndex)
	return core
}

func Match(text, query string) bool {
	if query == "" {
		return true
	}
	for start := 0; start < len(query); {
		for start < len(query) && query[start] == ' ' {
			start++
		}
		end := start
		for end < len(query) && query[end] != ' ' {
			end++
		}
		if start < end && !containsFoldASCII(text, query[start:end]) {
			return false
		}
		start = end
	}
	return true
}

func containsFoldASCII(text, term string) bool {
	if term == "" {
		return true
	}
	for start := 0; start+len(term) <= len(text); start++ {
		index := 0
		for index < len(term) && asciiLower(text[start+index]) == asciiLower(term[index]) {
			index++
		}
		if index == len(term) {
			return true
		}
	}
	return false
}

func asciiLower(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

// TextCells returns the first line's display width plus one cell for an ellipsis
// when more text follows a line break.
func TextCells(text string) int {
	lineEnd := strings.IndexAny(text, "\r\n")
	if lineEnd < 0 {
		return terminalwidth.Cells(text)
	}
	return terminalwidth.Cells(text[:lineEnd]) + 1
}

func LabelCells(item Item, terminalColumns int) int {
	rowCells := terminalColumns - MarkerCells
	if rowCells < 1 {
		rowCells = 1
	}
	currentCells := 0
	if item.Current {
		currentCells = CurrentTagCells
	}
	separatorCells := DetailSeparatorCells
	if item.Dim {
		separatorCells = DimDetailSeparatorCells
	}
	available := rowCells - currentCells
	labelCells := available
	if item.Detail != "" {
		natural := TextCells(item.Label)
		detail := TextCells(item.Detail)
		adjusted := available - separatorCells - detail
		minimum := available / 2
		if minimum > adjusted {
			labelCells = minimum
		} else {
			labelCells = adjusted
		}
		if natural < labelCells {
			labelCells = natural
		}
	}
	if labelCells < 1 {
		return 1
	}
	return labelCells
}

func (core *Core) Query() string {
	if core == nil {
		return ""
	}
	return string(core.query)
}

func (core *Core) MatchIndices() []int {
	if core == nil {
		return nil
	}
	return append([]int(nil), core.matches...)
}

func (core *Core) Selection() int {
	if core == nil {
		return 0
	}
	return core.selection
}

func (core *Core) FirstVisible() int {
	if core == nil {
		return 0
	}
	return core.firstVisible
}

func (core *Core) ViewportRows() int {
	if core == nil {
		return 0
	}
	return core.viewportRows
}

func (core *Core) Item(index int) (Item, bool) {
	if core == nil || index < 0 || index >= len(core.options.Items) {
		return Item{}, false
	}
	return core.options.Items[index], true
}

func (core *Core) SelectedItem() (Item, bool) {
	if core == nil || core.selection < 0 || core.selection >= len(core.matches) {
		return Item{}, false
	}
	return core.Item(core.matches[core.selection])
}

func (core *Core) SetViewportRows(rows int) {
	if core == nil {
		return
	}
	core.viewportRows = rows
	core.clampView()
}

func (core *Core) SetQuery(query string) {
	if core == nil {
		return
	}
	core.query = append(core.query[:0], query...)
	core.updateMatches()
}

func (core *Core) AppendQuery(text string) {
	if core == nil || text == "" {
		return
	}
	core.query = append(core.query, text...)
	core.updateMatches()
}

func (core *Core) TruncateQuery(length int) {
	if core == nil || length >= len(core.query) {
		return
	}
	if length < 0 {
		length = 0
	}
	for length > 0 && !utf8.RuneStart(core.query[length]) {
		length--
	}
	core.query = core.query[:length]
	core.updateMatches()
}

func (core *Core) Move(direction Direction) {
	if core == nil || len(core.matches) == 0 {
		return
	}
	if direction == Previous && core.selection > 0 {
		core.selection--
	} else if direction == Next && core.selection+1 < len(core.matches) {
		core.selection++
	}
	core.clampView()
}

func (core *Core) Page(direction Direction) {
	if core == nil || len(core.matches) == 0 {
		return
	}
	step := core.viewportRows / 2
	if step < 1 {
		step = 1
	}
	if direction == Previous {
		core.selection -= step
		if core.selection < 0 {
			core.selection = 0
		}
	} else if direction == Next {
		core.selection += step
		if core.selection >= len(core.matches) {
			core.selection = len(core.matches) - 1
		}
	}
	core.centerSelection()
}

func (core *Core) SelectFirst() {
	if core == nil {
		return
	}
	core.selection = 0
	core.clampView()
}

func (core *Core) SelectLast() {
	if core == nil {
		return
	}
	core.selection = len(core.matches) - 1
	if core.selection < 0 {
		core.selection = 0
	}
	core.clampView()
}

func (core *Core) SelectItem(itemIndex int) {
	if core == nil {
		return
	}
	for match, index := range core.matches {
		if index == itemIndex {
			core.selection = match
			core.centerSelection()
			return
		}
	}
}

func (core *Core) updateMatches() {
	core.matches = core.matches[:0]
	query := string(core.query)
	for index, item := range core.options.Items {
		if Match(item.Label, query) {
			core.matches = append(core.matches, index)
		}
	}
	core.selection = 0
	core.firstVisible = 0
}

func (core *Core) clampView() {
	if len(core.matches) == 0 {
		core.selection, core.firstVisible = 0, 0
		return
	}
	visible := core.viewportRows
	if visible < 1 {
		visible = 1
	}
	if core.selection >= len(core.matches) {
		core.selection = len(core.matches) - 1
	}
	if core.selection < core.firstVisible {
		core.firstVisible = core.selection
	} else if core.selection >= core.firstVisible+visible {
		core.firstVisible = core.selection - visible + 1
	}
	if len(core.matches) <= visible {
		core.firstVisible = 0
	} else if core.firstVisible+visible > len(core.matches) {
		core.firstVisible = len(core.matches) - visible
	}
}

func (core *Core) centerSelection() {
	visible := core.viewportRows
	if visible < 1 {
		visible = 1
	}
	if len(core.matches) <= visible {
		core.firstVisible = 0
		return
	}
	half := visible / 2
	if core.selection > half {
		core.firstVisible = core.selection - half
	} else {
		core.firstVisible = 0
	}
	if core.firstVisible+visible > len(core.matches) {
		core.firstVisible = len(core.matches) - visible
	}
}

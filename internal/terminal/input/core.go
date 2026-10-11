// Package input implements the terminal-independent editor state used by the TTY reader.
package input

import (
	"strings"
	"unicode/utf8"
)

const HistoryLimit = 1000

// Core owns an editable byte buffer, a UTF-8 byte cursor, and command history. Cursor offsets
// are byte offsets so slices and completion spans match the wire format used by the terminal UI.
type Core struct {
	buffer     []byte
	cursor     int
	history    []string
	historyPos int
	draft      *string
}

func NewCore() *Core { return &Core{} }

func (core *Core) Text() string {
	if core == nil {
		return ""
	}
	return string(core.buffer)
}

func (core *Core) Cursor() int {
	if core == nil {
		return 0
	}
	return core.cursor
}

func (core *Core) SetCursor(offset int) {
	if core == nil {
		return
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(core.buffer) {
		offset = len(core.buffer)
	}
	core.cursor = offset
}

func (core *Core) SetBuffer(text string) {
	if core == nil {
		return
	}
	core.buffer = append(core.buffer[:0], text...)
	core.cursor = len(core.buffer)
}

func (core *Core) Insert(text string) {
	if core == nil || text == "" {
		return
	}
	core.cursor = clamp(core.cursor, 0, len(core.buffer))
	core.buffer = append(core.buffer, make([]byte, len(text))...)
	copy(core.buffer[core.cursor+len(text):], core.buffer[core.cursor:len(core.buffer)-len(text)])
	copy(core.buffer[core.cursor:], text)
	core.cursor += len(text)
}

// CommitPaste inserts text after applying filter when it returns a replacement.
func (core *Core) CommitPaste(text string, filter func(string) (string, bool)) {
	if filter != nil {
		if replacement, ok := filter(text); ok {
			text = replacement
		}
	}
	core.Insert(text)
}

// ReplaceSpan replaces the byte range [start,end). A nil replacement deletes the range.
func (core *Core) ReplaceSpan(start, end int, replacement *string) {
	if core == nil || start < 0 || start > end || end > len(core.buffer) {
		return
	}
	var inserted string
	if replacement != nil {
		inserted = *replacement
	}
	buffer := make([]byte, 0, len(core.buffer)-(end-start)+len(inserted))
	buffer = append(buffer, core.buffer[:start]...)
	buffer = append(buffer, inserted...)
	buffer = append(buffer, core.buffer[end:]...)
	core.buffer = buffer
	core.cursor = start + len(inserted)
}

func (core *Core) LineStart() int {
	if core == nil {
		return 0
	}
	cursor := clamp(core.cursor, 0, len(core.buffer))
	for cursor > 0 && core.buffer[cursor-1] != '\n' {
		cursor--
	}
	return cursor
}

func (core *Core) LineEnd() int {
	if core == nil {
		return 0
	}
	cursor := clamp(core.cursor, 0, len(core.buffer))
	for cursor < len(core.buffer) && core.buffer[cursor] != '\n' {
		cursor++
	}
	return cursor
}

func (core *Core) MoveLeft() {
	if core != nil && core.cursor > 0 {
		core.cursor = previousUTF8(core.buffer, core.cursor)
	}
}

func (core *Core) MoveRight() {
	if core != nil && core.cursor < len(core.buffer) {
		core.cursor = nextUTF8(core.buffer, core.cursor)
	}
}

func (core *Core) MoveWordLeft() {
	if core != nil {
		core.cursor = alnumWordStart(core.buffer, clamp(core.cursor, 0, len(core.buffer)))
	}
}

func (core *Core) MoveWordRight() {
	if core != nil {
		core.cursor = scanAlnumRight(core.buffer, clamp(core.cursor, 0, len(core.buffer)))
	}
}

func (core *Core) DeleteBack() {
	if core == nil || core.cursor <= 0 {
		return
	}
	previous := previousUTF8(core.buffer, core.cursor)
	core.erase(previous, core.cursor-previous)
}

func (core *Core) DeleteForward() {
	if core == nil || core.cursor >= len(core.buffer) {
		return
	}
	next := nextUTF8(core.buffer, core.cursor)
	core.erase(core.cursor, next-core.cursor)
}

func (core *Core) KillToEndOfLine() {
	if core == nil {
		return
	}
	end := core.LineEnd()
	if end == core.cursor && end < len(core.buffer) && core.buffer[end] == '\n' {
		end++
	}
	core.erase(core.cursor, end-core.cursor)
}

func (core *Core) KillToStartOfLine() {
	if core == nil {
		return
	}
	start := core.LineStart()
	core.erase(start, core.cursor-start)
}

func (core *Core) KillWordBack() {
	if core == nil {
		return
	}
	start := whitespaceWordStart(core.buffer, clamp(core.cursor, 0, len(core.buffer)))
	core.erase(start, core.cursor-start)
}

func (core *Core) KillWordBackAlnum() {
	if core == nil {
		return
	}
	start := alnumWordStart(core.buffer, clamp(core.cursor, 0, len(core.buffer)))
	core.erase(start, core.cursor-start)
}

func (core *Core) KillWordForward() {
	if core == nil {
		return
	}
	end := scanAlnumRight(core.buffer, clamp(core.cursor, 0, len(core.buffer)))
	core.erase(core.cursor, end-core.cursor)
}

func (core *Core) erase(position, count int) {
	if position < 0 || position >= len(core.buffer) || count <= 0 {
		return
	}
	end := position + count
	if end > len(core.buffer) {
		end = len(core.buffer)
	}
	copy(core.buffer[position:], core.buffer[end:])
	core.buffer = core.buffer[:len(core.buffer)-(end-position)]
	if core.cursor > end {
		core.cursor -= end - position
	} else if core.cursor > position {
		core.cursor = position
	}
}

func whitespaceWordStart(text []byte, end int) int {
	for end > 0 && asciiSpace(text[end-1]) {
		end--
	}
	for end > 0 && !asciiSpace(text[end-1]) {
		end--
	}
	return end
}

func alnumWordStart(text []byte, end int) int {
	for end > 0 && !wordByte(text[end-1]) {
		end--
	}
	for end > 0 && wordByte(text[end-1]) {
		end--
	}
	return end
}

func scanAlnumRight(text []byte, start int) int {
	for start < len(text) && !wordByte(text[start]) {
		start++
	}
	for start < len(text) && wordByte(text[start]) {
		start++
	}
	return start
}

func wordByte(value byte) bool {
	return value >= 0x80 || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func asciiSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

func nextUTF8(buffer []byte, offset int) int {
	if offset >= len(buffer) {
		return len(buffer)
	}
	_, size := utf8.DecodeRune(buffer[offset:])
	if size <= 0 {
		return offset + 1
	}
	return offset + size
}

func previousUTF8(buffer []byte, offset int) int {
	if offset <= 0 {
		return 0
	}
	candidate := offset - 1
	continuations := 0
	for candidate > 0 && buffer[candidate]&0xC0 == 0x80 && continuations < 3 {
		candidate--
		continuations++
	}
	if buffer[candidate]&0xC0 == 0x80 || continuations == 0 {
		return offset - 1
	}
	_, size := utf8.DecodeRune(buffer[candidate:offset])
	if size != offset-candidate {
		return offset - 1
	}
	return candidate
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// AddHistory removes an older exact duplicate and appends the entry unless it is empty or already
// newest. The oldest entry is evicted when HistoryLimit is exceeded.
func (core *Core) AddHistory(line string) bool {
	if core == nil || line == "" {
		return false
	}
	if len(core.history) > 0 && core.history[len(core.history)-1] == line {
		core.historyPos = len(core.history)
		return false
	}
	kept := core.history[:0]
	for _, entry := range core.history {
		if entry != line {
			kept = append(kept, entry)
		}
	}
	core.history = append(kept, line)
	if len(core.history) > HistoryLimit {
		core.history = append([]string(nil), core.history[len(core.history)-HistoryLimit:]...)
	}
	core.historyPos = len(core.history)
	core.draft = nil
	return true
}

func (core *Core) HistoryEntries() []string {
	if core == nil {
		return nil
	}
	return append([]string(nil), core.history...)
}

func (core *Core) HistoryLen() int {
	if core == nil {
		return 0
	}
	return len(core.history)
}

// HistoryPosition is the current history index; HistoryLen denotes the live draft position.
func (core *Core) HistoryPosition() int {
	if core == nil {
		return 0
	}
	return core.historyPos
}

// ResetHistoryNavigation starts a new prompt at the live draft position and discards a saved draft.
func (core *Core) ResetHistoryNavigation() {
	if core == nil {
		return
	}
	core.historyPos = len(core.history)
	core.draft = nil
}

// AcceptHistory selects a search result and optionally retains the live draft for Down navigation.
func (core *Core) AcceptHistory(index int, draft string, saveDraft bool) bool {
	if core == nil || index < 0 || index >= len(core.history) {
		return false
	}
	core.historyPos = index
	if saveDraft {
		value := draft
		core.draft = &value
	}
	return true
}

// HistorySearch returns the first matching history index while moving from start in direction
// -1 (older) or +1 (newer). It returns -1 for invalid or unmatched searches.
func (core *Core) HistorySearch(query string, start, direction int) int {
	if core == nil || query == "" || len(core.history) == 0 || direction != -1 && direction != 1 {
		return -1
	}
	for index := start; index >= 0 && index < len(core.history); index += direction {
		if strings.Contains(core.history[index], query) {
			return index
		}
	}
	return -1
}

func (core *Core) HistoryPrev() {
	if core == nil || len(core.history) == 0 {
		return
	}
	if core.historyPos >= len(core.history) {
		draft := core.Text()
		core.draft = &draft
		core.historyPos = len(core.history)
	}
	if core.historyPos > 0 {
		core.loadHistory(core.historyPos - 1)
	}
}

func (core *Core) HistoryNext() {
	if core == nil || core.historyPos >= len(core.history) {
		return
	}
	core.loadHistory(core.historyPos + 1)
}

func (core *Core) loadHistory(position int) {
	if position < len(core.history) {
		core.SetBuffer(core.history[position])
		core.historyPos = position
		return
	}
	if core.draft != nil {
		core.SetBuffer(*core.draft)
	} else {
		core.SetBuffer("")
	}
	core.draft = nil
	core.historyPos = len(core.history)
}

func EncodeHistory(entry string) string {
	var encoded strings.Builder
	encoded.Grow(len(entry) * 2)
	for _, value := range []byte(entry) {
		switch value {
		case '\\':
			encoded.WriteString("\\\\")
		case '\n':
			encoded.WriteString("\\n")
		default:
			encoded.WriteByte(value)
		}
	}
	return encoded.String()
}

func DecodeHistory(encoded string) string {
	var decoded strings.Builder
	decoded.Grow(len(encoded))
	for index := 0; index < len(encoded); index++ {
		if encoded[index] == '\\' && index+1 < len(encoded) {
			switch encoded[index+1] {
			case '\\':
				decoded.WriteByte('\\')
				index++
				continue
			case 'n':
				decoded.WriteByte('\n')
				index++
				continue
			}
		}
		decoded.WriteByte(encoded[index])
	}
	return decoded.String()
}

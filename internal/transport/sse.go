package transport

type SSEEvent struct {
	Name string
	Data string
}

type SSEParser struct {
	line     []byte
	event    []byte
	data     []byte
	callback func(SSEEvent) bool
	stopped  bool
}

func NewSSEParser(callback func(SSEEvent) bool) *SSEParser {
	return &SSEParser{callback: callback}
}

func (parser *SSEParser) Feed(chunk []byte) {
	lineStart := 0
	for index, char := range chunk {
		if char != '\n' {
			continue
		}
		parser.line = append(parser.line, chunk[lineStart:index]...)
		parser.processLine(parser.line)
		parser.line = parser.line[:0]
		lineStart = index + 1
	}
	if lineStart < len(chunk) {
		parser.line = append(parser.line, chunk[lineStart:]...)
	}
}

func (parser *SSEParser) Finalize() {
	if len(parser.line) > 0 {
		parser.processLine(parser.line)
		parser.line = parser.line[:0]
	}
	if len(parser.event) > 0 || len(parser.data) > 0 {
		parser.emit()
	}
}

func (parser *SSEParser) processLine(line []byte) {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if len(line) == 0 {
		parser.emit()
		return
	}
	if line[0] == ':' {
		return
	}
	colon := -1
	for index, char := range line {
		if char == ':' {
			colon = index
			break
		}
	}
	fieldLength := len(line)
	value := []byte(nil)
	if colon >= 0 {
		fieldLength = colon
		value = line[colon+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	field := line[:fieldLength]
	switch string(field) {
	case "event":
		parser.event = append(parser.event[:0], value...)
	case "data":
		if len(parser.data) > 0 {
			parser.data = append(parser.data, '\n')
		}
		parser.data = append(parser.data, value...)
	}
}

func (parser *SSEParser) emit() {
	if len(parser.event) == 0 && len(parser.data) == 0 {
		return
	}
	if !parser.stopped && parser.callback != nil {
		event := SSEEvent{Name: string(parser.event), Data: string(parser.data)}
		if !parser.callback(event) {
			parser.stopped = true
		}
	}
	parser.event = parser.event[:0]
	parser.data = parser.data[:0]
}

// Package markdown contains the terminal Markdown scanner and renderer.
package markdown

// LineKind classifies the beginning of one physical Markdown line.
type LineKind uint8

const (
	LineIncomplete LineKind = iota
	LineText
	LineBlank
	LineHeading
	LineFence
	LineThematic
	LineBullet
	LineOrdered
	LineBlockquote
	LinePipe
)

// LineInfo describes a line prefix. An incomplete classification can still carry a definite
// block kind, such as a provisional bullet which could become a thematic break.
type LineInfo struct {
	Kind            LineKind
	IndentLength    int
	MarkerLength    int
	Marker          byte
	Complete        bool
	NormalizeIndent bool
}

// ScanLine classifies the available bytes at the start of a physical line. Set final when no
// more bytes can arrive; this resolves prefixes which would otherwise be ambiguous.
func ScanLine(line string, final bool) LineInfo {
	markerOffset := 0
	hasTab := false
	for markerOffset < len(line) && (line[markerOffset] == ' ' || line[markerOffset] == '\t') {
		if line[markerOffset] == '\t' {
			hasTab = true
		}
		markerOffset++
	}
	indent := markerOffset
	if markerOffset >= len(line) {
		if !final {
			return incomplete(indent)
		}
		return lineInfo(LineText, indent)
	}
	if line[markerOffset] == '\n' {
		info := lineInfo(LineBlank, indent)
		info.NormalizeIndent = true
		return info
	}

	blockIndent := !hasTab && markerOffset <= 3
	marker := line[markerOffset]
	if blockIndent && marker == '#' {
		end := markerOffset
		for end < len(line) && end-markerOffset < 6 && line[end] == '#' {
			end++
		}
		if end >= len(line) {
			if final {
				return lineInfo(LineText, indent)
			}
			return incomplete(indent)
		}
		if line[end] == ' ' || line[end] == '\n' {
			info := lineInfo(LineHeading, indent)
			info.Marker = marker
			info.MarkerLength = end - markerOffset
			info.NormalizeIndent = true
			return info
		}
		return lineInfo(LineText, indent)
	}
	if blockIndent && marker == '`' {
		available := len(line) - markerOffset
		if available < 3 {
			if final {
				return lineInfo(LineText, indent)
			}
			return incomplete(indent)
		}
		if line[markerOffset+1] == '`' && line[markerOffset+2] == '`' {
			end := markerOffset + 3
			for end < len(line) && line[end] == '`' {
				end++
			}
			info := lineInfo(LineFence, indent)
			info.Marker = marker
			info.MarkerLength = end - markerOffset
			info.NormalizeIndent = true
			for end < len(line) && line[end] != '\n' {
				end++
			}
			if end >= len(line) {
				if final {
					return lineInfo(LineText, indent)
				}
				info.Complete = false
			}
			return info
		}
		return lineInfo(LineText, indent)
	}

	bullet := false
	if !hasTab && (marker == '*' || marker == '-' || marker == '+') {
		if markerOffset+1 >= len(line) {
			if !final {
				return incomplete(indent)
			}
		} else if line[markerOffset+1] == ' ' {
			bullet = true
		}
	}
	if blockIndent && (marker == '-' || marker == '*' || marker == '_' || marker == '=') {
		end, count := markerOffset, 0
		for end < len(line) && (line[end] == marker || line[end] == ' ' || line[end] == '\t' || line[end] == '\r') {
			if line[end] == marker {
				count++
			}
			end++
		}
		if end >= len(line) {
			if final && count >= 3 {
				info := lineInfo(LineThematic, indent)
				info.Marker, info.MarkerLength, info.NormalizeIndent = marker, count, true
				return info
			}
			if !final && bullet {
				info := lineInfo(LineBullet, indent)
				info.Marker, info.MarkerLength, info.Complete = marker, 1, false
				return info
			}
			if final {
				return lineInfo(LineText, indent)
			}
			return incomplete(indent)
		}
		if line[end] == '\n' && count >= 3 {
			info := lineInfo(LineThematic, indent)
			info.Marker, info.MarkerLength, info.NormalizeIndent = marker, count, true
			return info
		}
	}
	if bullet {
		info := lineInfo(LineBullet, indent)
		info.Marker, info.MarkerLength = marker, 1
		return info
	}
	if !hasTab && marker >= '0' && marker <= '9' {
		end := markerOffset
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end >= len(line) || end+1 >= len(line) {
			if final {
				return lineInfo(LineText, indent)
			}
			return incomplete(indent)
		}
		if (line[end] == '.' || line[end] == ')') && line[end+1] == ' ' {
			info := lineInfo(LineOrdered, indent)
			info.Marker = line[end]
			info.MarkerLength = end - markerOffset + 1
			return info
		}
	}
	if blockIndent && marker == '>' {
		info := lineInfo(LineBlockquote, indent)
		info.Marker, info.MarkerLength, info.NormalizeIndent = marker, 1, true
		return info
	}
	if blockIndent && marker == '|' {
		info := lineInfo(LinePipe, indent)
		info.Marker, info.MarkerLength, info.NormalizeIndent = marker, 1, true
		return info
	}
	return lineInfo(LineText, indent)
}

func lineInfo(kind LineKind, indent int) LineInfo {
	return LineInfo{Kind: kind, IndentLength: indent, Complete: true}
}

func incomplete(indent int) LineInfo {
	return LineInfo{Kind: LineIncomplete, IndentLength: indent}
}

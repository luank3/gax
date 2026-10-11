package web

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxEntityLength bounds an entity reference; anything longer is left as written text.
const maxEntityLength = 32

type tokenKind uint8

const (
	tokenText tokenKind = iota
	tokenStartTag
	tokenEndTag
)

type token struct {
	kind        tokenKind
	name        string
	attrs       map[string]string
	selfClosing bool
	text        string
}

func (t token) attr(name string) string {
	return t.attrs[name]
}

func (t token) hasClass(name string) bool {
	for _, entry := range strings.Fields(t.attrs["class"]) {
		if entry == name {
			return true
		}
	}
	return false
}

// rawTextElements hold character data rather than markup, so their content is scanned as one text
// run: a "<" inside a script or a style rule cannot be mistaken for a tag.
var rawTextElements = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true, "xmp": true, "plaintext": true,
}

// scanHTML walks a lenient HTML token stream. `emit` returns false to stop scanning; malformed
// markup never fails, it degrades to text.
func scanHTML(document string, emit func(token) bool) {
	for index := 0; index < len(document); {
		next := strings.IndexByte(document[index:], '<')
		if next < 0 {
			emit(token{kind: tokenText, text: document[index:]})
			return
		}
		if next > 0 && !emit(token{kind: tokenText, text: document[index : index+next]}) {
			return
		}
		index += next
		switch {
		case strings.HasPrefix(document[index:], "<!--"):
			end := strings.Index(document[index+4:], "-->")
			if end < 0 {
				return
			}
			index += 4 + end + 3
		case strings.HasPrefix(document[index:], "<!"), strings.HasPrefix(document[index:], "<?"):
			end := strings.IndexByte(document[index:], '>')
			if end < 0 {
				return
			}
			index += end + 1
		case strings.HasPrefix(document[index:], "</"):
			end := strings.IndexByte(document[index:], '>')
			if end < 0 {
				return
			}
			name := tagName(document[index+2 : index+end])
			index += end + 1
			if name == "" {
				if !emit(token{kind: tokenText, text: "</"}) {
					return
				}
				continue
			}
			if !emit(token{kind: tokenEndTag, name: name}) {
				return
			}
		default:
			if index+1 >= len(document) || !isTagNameStart(document[index+1]) {
				if !emit(token{kind: tokenText, text: "<"}) {
					return
				}
				index++
				continue
			}
			name, attrs, selfClosing, after := parseStartTag(document, index)
			index = after
			if !emit(token{kind: tokenStartTag, name: name, attrs: attrs, selfClosing: selfClosing}) {
				return
			}
			if !rawTextElements[name] || selfClosing {
				continue
			}
			close := indexOfCloseTag(document, index, name)
			if close < 0 {
				emit(token{kind: tokenText, text: document[index:]})
				return
			}
			if close > index && !emit(token{kind: tokenText, text: document[index:close]}) {
				return
			}
			index = close
		}
	}
}

func parseStartTag(document string, start int) (name string, attrs map[string]string, selfClosing bool, after int) {
	index := start + 1
	nameStart := index
	for index < len(document) && isTagNameChar(document[index]) {
		index++
	}
	name = strings.ToLower(document[nameStart:index])
	for index < len(document) {
		for index < len(document) && isSpaceByte(document[index]) {
			index++
		}
		if index >= len(document) {
			break
		}
		if document[index] == '>' {
			return name, attrs, false, index + 1
		}
		if document[index] == '/' {
			if index+1 < len(document) && document[index+1] == '>' {
				return name, attrs, true, index + 2
			}
			index++
			continue
		}
		attrStart := index
		for index < len(document) && !isSpaceByte(document[index]) &&
			document[index] != '=' && document[index] != '>' && document[index] != '/' {
			index++
		}
		attrName := strings.ToLower(document[attrStart:index])
		value := ""
		valueStart := index
		for index < len(document) && isSpaceByte(document[index]) {
			index++
		}
		if index < len(document) && document[index] == '=' {
			index++
			for index < len(document) && isSpaceByte(document[index]) {
				index++
			}
			switch {
			case index < len(document) && (document[index] == '"' || document[index] == '\''):
				quote := document[index]
				index++
				start := index
				for index < len(document) && document[index] != quote {
					index++
				}
				value = document[start:index]
				if index < len(document) {
					index++
				}
			default:
				start := index
				for index < len(document) && !isSpaceByte(document[index]) && document[index] != '>' {
					index++
				}
				value = document[start:index]
			}
		} else {
			index = valueStart
		}
		if attrName != "" {
			if attrs == nil {
				attrs = make(map[string]string, 4)
			}
			if _, exists := attrs[attrName]; !exists {
				attrs[attrName] = value
			}
		}
	}
	return name, attrs, false, index
}

func indexOfCloseTag(document string, from int, name string) int {
	for index := from; index+2 < len(document); index++ {
		if document[index] != '<' || document[index+1] != '/' {
			continue
		}
		start := index + 2
		if start+len(name) > len(document) || !strings.EqualFold(document[start:start+len(name)], name) {
			continue
		}
		if after := start + len(name); after == len(document) || document[after] == '>' || isSpaceByte(document[after]) {
			return index
		}
	}
	return -1
}

func tagName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || !isTagNameStart(text[0]) {
		return ""
	}
	for index := 0; index < len(text); index++ {
		if !isTagNameChar(text[index]) {
			return strings.ToLower(text[:index])
		}
	}
	return strings.ToLower(text)
}

func isTagNameStart(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isTagNameChar(char byte) bool {
	return isTagNameStart(char) || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == ':' || char == '.'
}

func isSpaceByte(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\f'
}

// elements whose text content is markup, styling, or fallback noise rather than prose.
var skippedElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true, "math": true,
	"iframe": true, "canvas": true, "object": true, "embed": true, "video": true, "audio": true,
	"select": true, "textarea": true,
}

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true,
}

var blockElements = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "body": true, "caption": true,
	"center": true, "colgroup": true, "dd": true, "details": true, "dialog": true, "div": true,
	"dl": true, "dt": true, "fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "hgroup": true, "hr": true, "html": true, "legend": true, "li": true, "main": true,
	"menu": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true, "summary": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true, "thead": true, "title": true,
	"tr": true, "ul": true,
}

// Text converts an HTML document to readable plain text: tags are dropped, block elements become
// line breaks, list items get a bullet, table cells are separated, non-textual elements are
// skipped, entities are decoded, and whitespace is collapsed (except inside <pre>).
func Text(document []byte) string {
	builder := textBuilder{}
	skipDepth := 0
	preDepth := 0
	cells := 0
	scanHTML(string(document), func(t token) bool {
		switch t.kind {
		case tokenText:
			if skipDepth > 0 {
				return true
			}
			builder.text(decodeEntities(t.text), preDepth > 0)
		case tokenStartTag:
			if skippedElements[t.name] {
				if !t.selfClosing && !voidElements[t.name] {
					skipDepth++
				}
				return true
			}
			if skipDepth > 0 {
				return true
			}
			switch t.name {
			case "br", "hr":
				builder.line()
			case "li":
				builder.bullet()
			case "tr":
				cells = 0
			case "td", "th":
				if cells > 0 {
					builder.literal(" | ")
				}
				cells++
			case "pre":
				preDepth++
				builder.line()
			default:
				if blockElements[t.name] {
					builder.line()
				}
			}
		case tokenEndTag:
			if skippedElements[t.name] {
				if skipDepth > 0 && !voidElements[t.name] {
					skipDepth--
				}
				return true
			}
			if skipDepth > 0 {
				return true
			}
			switch t.name {
			case "td", "th":
				// The next cell start emits the separator; the row end emits the line break.
			case "pre":
				if preDepth > 0 {
					preDepth--
				}
				builder.line()
			case "tr":
				builder.line()
			default:
				if blockElements[t.name] {
					builder.line()
				}
			}
		}
		return true
	})
	return builder.String()
}

// textBuilder assembles collapsed text: line breaks and spaces are pending until the next word, so
// no line ever starts or ends with collapsed whitespace.
type textBuilder struct {
	builder   strings.Builder
	lastByte  byte
	lineBreak bool
	space     bool
}

func (b *textBuilder) line() {
	b.lineBreak = true
}

func (b *textBuilder) bullet() {
	b.line()
	b.write("- ")
}

// literal writes a separator without collapsing, skipping it at the start of a line.
func (b *textBuilder) literal(text string) {
	if b.builder.Len() == 0 || b.lastByte == '\n' {
		return
	}
	b.space = false
	b.builder.WriteString(text)
	b.lastByte = text[len(text)-1]
}

func (b *textBuilder) text(run string, preserve bool) {
	if preserve {
		b.flush()
		b.builder.WriteString(run)
		if run != "" {
			b.lastByte = run[len(run)-1]
		}
		return
	}
	for index := 0; index < len(run); {
		runeValue, size := utf8.DecodeRuneInString(run[index:])
		if runeValue != utf8.RuneError || size > 1 {
			if unicode.IsSpace(runeValue) {
				end := index + size
				for end < len(run) {
					next, nextSize := utf8.DecodeRuneInString(run[end:])
					if !unicode.IsSpace(next) {
						break
					}
					end += nextSize
				}
				b.space = true
				index = end
				continue
			}
		}
		end := index + size
		for end < len(run) {
			next, nextSize := utf8.DecodeRuneInString(run[end:])
			if next != utf8.RuneError && unicode.IsSpace(next) {
				break
			}
			if next == utf8.RuneError && nextSize == 1 {
				break
			}
			end += nextSize
		}
		b.write(run[index:end])
		index = end
	}
}

func (b *textBuilder) write(text string) {
	if text == "" {
		return
	}
	b.flush()
	b.builder.WriteString(text)
	b.lastByte = text[len(text)-1]
}

func (b *textBuilder) flush() {
	if b.lineBreak {
		b.lineBreak = false
		if b.builder.Len() > 0 && b.lastByte != '\n' {
			b.builder.WriteByte('\n')
			b.lastByte = '\n'
		}
		b.space = false
	}
	if b.space {
		b.space = false
		if b.builder.Len() > 0 && b.lastByte != '\n' && b.lastByte != ' ' {
			b.builder.WriteByte(' ')
			b.lastByte = ' '
		}
	}
}

func (b *textBuilder) String() string {
	lines := strings.Split(b.builder.String(), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t\r\f\v")
	}
	text := strings.Trim(strings.Join(lines, "\n"), "\n")
	if text == "" {
		return ""
	}
	return text + "\n"
}

// decodeEntities replaces character references; an unknown or malformed reference is left as
// written, so prose that merely contains an "&" survives unchanged.
func decodeEntities(text string) string {
	if !strings.ContainsRune(text, '&') {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for index := 0; index < len(text); {
		if text[index] != '&' {
			builder.WriteByte(text[index])
			index++
			continue
		}
		end := strings.IndexByte(text[index:], ';')
		if end < 0 || end > maxEntityLength {
			builder.WriteByte('&')
			index++
			continue
		}
		runeValue, ok := entityRune(text[index+1 : index+end])
		if !ok {
			builder.WriteByte('&')
			index++
			continue
		}
		builder.WriteRune(runeValue)
		index += end + 1
	}
	return builder.String()
}

func entityRune(name string) (rune, bool) {
	if name == "" {
		return 0, false
	}
	if name[0] == '#' {
		value, base := name[1:], 10
		if len(value) > 0 && (value[0] == 'x' || value[0] == 'X') {
			value, base = value[1:], 16
		}
		if value == "" {
			return 0, false
		}
		code, err := strconv.ParseInt(value, base, 32)
		if err != nil || code <= 0 || code > unicode.MaxRune || (code >= 0xD800 && code <= 0xDFFF) {
			return 0, false
		}
		return rune(code), true
	}
	runeValue, ok := namedEntities[name]
	return runeValue, ok
}

var namedEntities = map[string]rune{
	"amp": '&', "lt": '<', "gt": '>', "quot": '"', "apos": '\'', "nbsp": '\u00a0',
	"copy": '©', "reg": '®', "trade": '™', "hellip": '…', "mdash": '—', "ndash": '–',
	"lsquo": '‘', "rsquo": '’', "ldquo": '“', "rdquo": '”', "laquo": '«', "raquo": '»',
	"lsaquo": '‹', "rsaquo": '›', "bull": '•', "middot": '·', "sect": '§', "para": '¶',
	"dagger": '†', "Dagger": '‡', "permil": '‰', "prime": '′', "Prime": '″', "oline": '‾',
	"frasl": '⁄', "deg": '°', "plusmn": '±', "times": '×', "divide": '÷', "micro": 'µ',
	"frac12": '½', "frac14": '¼', "frac34": '¾', "sup1": '¹', "sup2": '²', "sup3": '³',
	"cent": '¢', "pound": '£', "curren": '¤', "yen": '¥', "euro": '€', "brvbar": '¦',
	"uml": '¨', "ordf": 'ª', "not": '¬', "shy": '\u00ad', "macr": '¯', "acute": '´',
	"cedil": '¸', "ordm": 'º', "iquest": '¿',
	"eacute": 'é', "egrave": 'è', "ecirc": 'ê', "euml": 'ë', "agrave": 'à', "aacute": 'á',
	"acirc": 'â', "atilde": 'ã', "auml": 'ä', "aring": 'å', "aelig": 'æ', "ccedil": 'ç',
	"iacute": 'í', "igrave": 'ì', "icirc": 'î', "iuml": 'ï', "ntilde": 'ñ', "oacute": 'ó',
	"ograve": 'ò', "ocirc": 'ô', "otilde": 'õ', "ouml": 'ö', "oslash": 'ø', "uacute": 'ú',
	"ugrave": 'ù', "ucirc": 'û', "uuml": 'ü', "yacute": 'ý', "yuml": 'ÿ', "szlig": 'ß',
	"Eacute": 'É', "Agrave": 'À', "Aacute": 'Á', "Auml": 'Ä', "Ccedil": 'Ç', "Ouml": 'Ö',
	"Uuml": 'Ü', "Ntilde": 'Ñ',
	"larr": '←', "uarr": '↑', "rarr": '→', "darr": '↓', "harr": '↔', "crarr": '↵',
	"spades": '♠', "clubs": '♣', "hearts": '♥', "diams": '♦', "loz": '◊',
	"le": '≤', "ge": '≥', "ne": '≠', "asymp": '≈', "equiv": '≡', "sim": '∼', "cong": '≅',
	"infin": '∞', "sum": '∑', "prod": '∏', "radic": '√', "int": '∫', "part": '∂', "nabla": '∇',
	"isin": '∈', "notin": '∉', "ni": '∋', "cap": '∩', "cup": '∪', "sub": '⊂', "sup": '⊃',
	"sube": '⊆', "supe": '⊇', "oplus": '⊕', "otimes": '⊗', "perp": '⊥', "sdot": '⋅',
	"forall": '∀', "exist": '∃', "empty": '∅', "there4": '∴', "ang": '∠', "and": '∧', "or": '∨',
	"alpha": 'α', "beta": 'β', "gamma": 'γ', "delta": 'δ', "epsilon": 'ε', "zeta": 'ζ',
	"eta": 'η', "theta": 'θ', "iota": 'ι', "kappa": 'κ', "lambda": 'λ', "mu": 'μ', "nu": 'ν',
	"xi": 'ξ', "pi": 'π', "rho": 'ρ', "sigma": 'σ', "tau": 'τ', "upsilon": 'υ', "phi": 'φ',
	"chi": 'χ', "psi": 'ψ', "omega": 'ω', "Alpha": 'Α', "Beta": 'Β', "Gamma": 'Γ',
	"Delta": 'Δ', "Theta": 'Θ', "Lambda": 'Λ', "Pi": 'Π', "Sigma": 'Σ', "Phi": 'Φ',
	"Psi": 'Ψ', "Omega": 'Ω',
	"ensp": ' ', "emsp": ' ', "thinsp": ' ', "zwnj": '\u200c', "zwj": '\u200d', "lrm": '\u200e',
	"rlm": '\u200f', "star": '☆', "check": '✓', "cross": '✗',
}

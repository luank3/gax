package skill

import "testing"

func TestFrontmatterScalarLineMatchesDocumentedWording(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "---\ndescription: does X\n---\n", "does X"},
		{"plain tab and spaces", "---\ndescription:\tdoes X  \n---\n", "does X"},
		{"plain punctuation", "---\ndescription: Build for AWS: ECS\n---\n", "Build for AWS: ECS"},
		{"plain multiline", "---\ndescription: Auto-detect inflections across all\n  metrics\nname: inflection\n---\n", "Auto-detect inflections across all metrics"},
		{"blank continuation", "---\ndescription:\n  starts on\n\n  the next line\n---\n", "starts on the next line"},
		{"plain comment", "---\ndescription: does X # note\n---\n", "does X"},
		{"hash inside words", "---\ndescription: C# and F#\n---\n", "C# and F#"},
		{"comment before continuation", "---\ndescription: # note\n  does X\n---\n", "does X"},
		{"unindented comment ends value", "---\ndescription: does X\n# note\n  not part of it\n---\n", "does X"},
		{"quoted comment", "---\ndescription: \"does X\" # note\n---\n", "does X"},
		{"single quoted escape", "---\ndescription: 'it''s # not a comment'\n---\n", "it's # not a comment"},
		{"single quoted multiline", "---\ndescription: 'wrapped\n  across lines'\n---\n", "wrapped across lines"},
		{"single quote missing close", "---\ndescription: 'unterminated\n---\n", ""},
		{"double quoted escapes", "---\ndescription: \"say \\\"hi\\\" \\\\ café\\u0021\"\n---\n", "say \"hi\" \\ café!"},
		{"double quoted named escapes", "---\ndescription: \"a\\vb\\fc\\Nd\\Le\\Pf\"\n---\n", "a b c d e f"},
		{"double quoted control escapes", "---\ndescription: \"x\\a\\b\\ey\\_z\"\n---\n", "x\a\b\x1by\u00a0z"},
		{"escaped line breaks", "---\ndescription: \"joined \\\n    lines, no\\\n  space\"\n---\n", "joined lines, nospace"},
		{"invalid escapes stay verbatim", "---\ndescription: \"\\q \\u12 \\uD800\"\n---\n", "\\q \\u12 \\uD800"},
		{"double quote missing close", "---\ndescription: \"unterminated\n---\n", ""},
		{"folded block", "---\ndescription: >\n  Multiline skills description.\n  Use it for this and that.\nname: example\n---\n", "Multiline skills description. Use it for this and that."},
		{"literal block", "---\ndescription: |\n  first\n\n    indented\n  last\n---\n", "first indented last"},
		{"stripped block", "---\ndescription: >-\n  stripped\n---\n", "stripped"},
		{"block header comment", "---\ndescription: |2+ # note\n  kept\n\n---\n", "kept"},
		{"block content hash", "---\ndescription: |\n  # not a comment\n---\n", "# not a comment"},
		{"empty block", "---\ndescription: >\n---\n", ""},
		{"invalid block header", "---\ndescription: >text\n  more\n---\n", ""},
		{"quoted block indicator", "---\ndescription: \"|\"\n---\n", "|"},
		{"key must be top level", "---\nmetadata:\n  description: nested\n---\n", ""},
		{"key must match exactly", "---\ndescriptions: other key\n---\n", ""},
		{"key needs separation", "---\ndescription:glued\n---\n", ""},
		{"first duplicate wins", "---\ndescription: first\ndescription: second\n---\n", "first"},
		{"opening fence required", "description: no fence\n", ""},
		{"opening fence must be first", "\n---\ndescription: late fence\n---\n", ""},
		{"closing fence required", "---\ndescription: unterminated\n", ""},
		{"closing fence without final newline", "---\ndescription: no final newline\n---", "no final newline"},
		{"CRLF", "---\r\ndescription: crlf\r\n  wrapped\r\n---\r\nbody\r\n", "crlf wrapped"},
		{"escaped CRLF", "---\r\ndescription: \"joined \\\r\n  crlf\"\r\n---\r\n", "joined crlf"},
		{"Unicode whitespace folds", "---\ndescription: a\u2028b\u0085c\u2029d\vb\fc\n---\n", "a b c d b c"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := frontmatterScalarLine([]byte(test.input), "description"); got != test.want {
				t.Fatalf("frontmatter scalar = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFrontmatterScalarLineSanitizesInvalidBytesAndNul(t *testing.T) {
	input := []byte("---\ndescription: a\x00b\xff\n---\n")
	if got, want := frontmatterScalarLine(input, "description"), "a\uFFFDb\uFFFD"; got != want {
		t.Fatalf("sanitized scalar = %q, want %q", got, want)
	}
	input = []byte("---\ndescription: \"A\\0B \\x00\"\n---\n")
	if got, want := frontmatterScalarLine(input, "description"), "A\uFFFDB \uFFFD"; got != want {
		t.Fatalf("sanitized escape = %q, want %q", got, want)
	}
}

package web_test

import (
	"testing"

	"github.com/luank3/gax/internal/web"
)

func TestTextConvertsHTMLToReadableText(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			name: "structure",
			document: `<!DOCTYPE html>
<html><head><title>Example Domain</title>
<style>body { color: red; }</style>
<script>var x = 1 < 2;</script>
</head>
<body>
<h1>Example</h1>
<p>This domain is for use in <b>illustrative</b> examples.</p>
<ul><li>one</li><li>two</li></ul>
</body></html>`,
			want: "Example Domain\nExample\nThis domain is for use in illustrative examples.\n- one\n- two\n",
		},
		{
			name:     "entities",
			document: `<p>Tom &amp; Jerry &#39;quoted&#x27; &nbsp;done &unknown; 100% &notanentity</p>`,
			want:     "Tom & Jerry 'quoted' done &unknown; 100% &notanentity\n",
		},
		{
			name:     "preformatted",
			document: "<pre>line 1\n  indented\nline 3</pre>after",
			want:     "line 1\n  indented\nline 3\nafter\n",
		},
		{
			name:     "table",
			document: `<table><tr><th>Name</th><th>Age</th></tr><tr><td>Ann</td><td>31</td></tr></table>`,
			want:     "Name | Age\nAnn | 31\n",
		},
		{
			name:     "malformed markup stays text",
			document: `<p>a < b and <3 cats</p><div class="x" title="a>b">ok</div>`,
			want:     "a < b and <3 cats\nok\n",
		},
		{
			name:     "skipped elements",
			document: `<div>keep <script>document.write("<div>x</div>");</script> tail<noscript>enable js</noscript></div>`,
			want:     "keep tail\n",
		},
		{
			name:     "links and breaks",
			document: `<p>see <a href="https://example.com/a?x=1">the docs</a><br>and more</p><!-- comment -->`,
			want:     "see the docs\nand more\n",
		},
		{
			name:     "plain text passes through",
			document: "just text\nwith lines",
			want:     "just text with lines\n",
		},
		{
			name:     "empty",
			document: "",
			want:     "",
		},
		{
			name:     "collapses blank lines and indentation",
			document: "<div>\n\n   <span> spaced </span>\n\n\n</div>",
			want:     "spaced\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := web.Text([]byte(test.document)); got != test.want {
				t.Fatalf("Text() = %q, want %q", got, test.want)
			}
		})
	}
}

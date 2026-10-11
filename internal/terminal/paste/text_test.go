package paste

import "testing"

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name string
		text []byte
		want string
	}{
		{name: "crlf", text: []byte("a\r\nb\r\n"), want: "a\nb\n"},
		{name: "lone carriage return", text: []byte("a\rb\r"), want: "a\nb\n"},
		{name: "nul bytes", text: []byte{'a', 0, 'b'}, want: "ab"},
		{name: "mixed", text: []byte{'x', '\r', '\n', 0, 'y', '\r', 'z'}, want: "x\ny\nz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeText(test.text); got != test.want {
				t.Fatalf("NormalizeText(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestFileURIsToPaths(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{name: "local file", text: "file:///tmp/a%20b.txt", want: "/tmp/a b.txt ", ok: true},
		{name: "localhost image", text: "file://localhost/a%2Bc.PNG", want: "[pasted image: /a+c.PNG] ", ok: true},
		{name: "mixed uri list", text: "file:///a\n\nfile:///b.jpeg\n", want: "/a\n[pasted image: /b.jpeg] ", ok: true},
		{name: "unescaped percent", text: "file:///a%Q0", want: "/a%Q0 ", ok: true},
		{name: "remote host", text: "file://server/share/a.png", ok: false},
		{name: "mixed list", text: "file:///a\nnot a uri", ok: false},
		{name: "encoded nul", text: "file:///tmp/a%00.png", ok: false},
		{name: "empty", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := FileURIsToPaths(test.text)
			if got != test.want || ok != test.ok {
				t.Fatalf("FileURIsToPaths(%q) = %q, %t; want %q, %t", test.text, got, ok, test.want, test.ok)
			}
		})
	}
}

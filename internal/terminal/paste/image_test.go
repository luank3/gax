package paste

import (
	"encoding/binary"
	"testing"
)

func framedPNG() []byte {
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	chunk := func(kind string, contents []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(contents)))
		data = append(data, length[:]...)
		data = append(data, kind...)
		data = append(data, contents...)
		data = append(data, 0, 0, 0, 0)
	}
	chunk("IHDR", make([]byte, 13))
	chunk("IDAT", nil)
	chunk("IEND", nil)
	return data
}

func TestSniffClipboardImageChecksFramingCompleteness(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		mime string
		ok   bool
	}{
		{name: "complete png", data: framedPNG(), mime: "image/png", ok: true},
		{name: "truncated png", data: framedPNG()[:len(framedPNG())-12], mime: "image/png"},
		{name: "complete jpeg", data: []byte{0xff, 0xd8, 0xff, 0xda, 0, 2, 0x11, 0xff, 0xd9}, mime: "image/jpeg", ok: true},
		{name: "truncated jpeg", data: []byte{0xff, 0xd8, 0xff, 0xda, 0, 2, 0x11}, mime: "image/jpeg"},
		{name: "complete gif", data: framedGIF(), mime: "image/gif", ok: true},
		{name: "truncated gif", data: framedGIF()[:len(framedGIF())-1], mime: "image/gif"},
		{name: "complete webp", data: framedWebP(), mime: "image/webp", ok: true},
		{name: "truncated webp", data: framedWebP()[:len(framedWebP())-1], mime: "image/webp"},
		{name: "unknown", data: []byte("clipboard text")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mime, ok := sniffClipboardImage(test.data)
			if mime != test.mime || ok != test.ok {
				t.Fatalf("sniffClipboardImage() = %q, %t; want %q, %t", mime, ok, test.mime, test.ok)
			}
		})
	}
}

func framedGIF() []byte {
	data := append([]byte("GIF89a"), make([]byte, 7)...)
	data = append(data, 0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0)
	data = append(data, 2, 1, 0, 0, 0x3b)
	return data
}

func framedWebP() []byte {
	data := append([]byte("RIFF"), 0, 0, 0, 0)
	data = append(data, []byte("WEBPVP8L")...)
	data = append(data, 5, 0, 0, 0, 0x2f, 0, 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	return data
}

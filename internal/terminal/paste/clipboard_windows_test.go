//go:build windows

package paste

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestDIBToPNGDecodes24BitRGB(t *testing.T) {
	// Construct a minimal 2x2 24-bit DIB
	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:4], 40)   // biSize
	binary.LittleEndian.PutUint32(header[4:8], 2)    // biWidth = 2
	binary.LittleEndian.PutUint32(header[8:12], 2)   // biHeight = 2
	binary.LittleEndian.PutUint16(header[12:14], 1)  // biPlanes = 1
	binary.LittleEndian.PutUint16(header[14:16], 24) // biBitCount = 24
	binary.LittleEndian.PutUint32(header[16:20], 0)  // biCompression = BI_RGB
	binary.LittleEndian.PutUint32(header[20:24], 16) // biSizeImage: 2 rows * 8 bytes pitch = 16
	// Row pitch: ((2*24 + 31)/32)*4 = (79/32)*4 = 2*4 = 8 bytes per row (6 bytes pixels + 2 bytes padding)
	pixels := make([]byte, 16)
	// Row 0 (bottom row): Red pixel (BGR: 0, 0, 255), Green pixel (BGR: 0, 255, 0)
	pixels[0], pixels[1], pixels[2] = 0, 0, 255
	pixels[3], pixels[4], pixels[5] = 0, 255, 0
	// Row 1 (top row): Blue pixel (BGR: 255, 0, 0), White pixel (BGR: 255, 255, 255)
	pixels[8], pixels[9], pixels[10] = 255, 0, 0
	pixels[11], pixels[12], pixels[13] = 255, 255, 255

	dib := append(header, pixels...)
	pngBytes, err := dibToPNG(dib)
	if err != nil {
		t.Fatalf("dibToPNG() error = %v", err)
	}
	if len(pngBytes) == 0 {
		t.Fatal("dibToPNG() produced empty output")
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 2 || bounds.Dy() != 2 {
		t.Fatalf("decoded image dimensions = %dx%d, want 2x2", bounds.Dx(), bounds.Dy())
	}
}

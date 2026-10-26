//go:build windows

package paste

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	openClipboard              = user32.NewProc("OpenClipboard")
	closeClipboard             = user32.NewProc("CloseClipboard")
	emptyClipboard             = user32.NewProc("EmptyClipboard")
	getClipboardData           = user32.NewProc("GetClipboardData")
	setClipboardData           = user32.NewProc("SetClipboardData")
	isClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	registerClipboardFormat    = user32.NewProc("RegisterClipboardFormatW")

	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	globalAlloc  = kernel32.NewProc("GlobalAlloc")
	globalFree   = kernel32.NewProc("GlobalFree")
	globalLock   = kernel32.NewProc("GlobalLock")
	globalUnlock = kernel32.NewProc("GlobalUnlock")
	globalSize   = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13 // CF_UNICODETEXT
	cfDIB         = 8  // CF_DIB
	cfDIBV5       = 17 // CF_DIBV5
	gmemMoveable  = 0x0002
)

var (
	initPngFormat sync.Once
	pngFormatID   uintptr
)

func pngClipboardFormat() uintptr {
	initPngFormat.Do(func() {
		name, _ := syscall.UTF16PtrFromString("PNG")
		res, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(name)))
		pngFormatID = res
	})
	return pngFormatID
}

func winGlobalLock(hMem uintptr) unsafe.Pointer {
	r1, _, _ := globalLock.Call(hMem)
	if r1 == 0 {
		return nil
	}
	return *(*unsafe.Pointer)(unsafe.Pointer(&r1))
}

func withClipboard(fn func() bool) bool {
	for attempt := 0; attempt < 5; attempt++ {
		r, _, _ := openClipboard.Call(0)
		if r != 0 {
			defer closeClipboard.Call()
			return fn()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func platformClipboardText(ctx context.Context) ([]byte, bool) {
	var result []byte
	var ok bool
	withClipboard(func() bool {
		avail, _, _ := isClipboardFormatAvailable.Call(cfUnicodeText)
		if avail == 0 {
			return false
		}
		hMem, _, _ := getClipboardData.Call(cfUnicodeText)
		if hMem == 0 {
			return false
		}
		ptr := winGlobalLock(hMem)
		if ptr == nil {
			return false
		}
		defer globalUnlock.Call(hMem)

		sz, _, _ := globalSize.Call(hMem)
		if sz == 0 {
			return false
		}
		maxChars := int(sz) / 2
		u16 := unsafe.Slice((*uint16)(ptr), maxChars)
		end := 0
		for end < len(u16) && u16[end] != 0 {
			end++
		}
		text := string(utf16.Decode(u16[:end]))
		if len(text) > maxClipboardText {
			text = text[:maxClipboardText]
		}
		result = []byte(text)
		ok = len(result) > 0
		return ok
	})
	if ok {
		return result, true
	}
	return platformClipboardTextPowerShell(ctx)
}

func copyPlatformClipboard(ctx context.Context, text string) bool {
	u16 := utf16.Encode([]rune(text))
	u16 = append(u16, 0)
	bytesNeeded := uintptr(len(u16) * 2)

	hMem, _, _ := globalAlloc.Call(gmemMoveable, bytesNeeded)
	if hMem == 0 {
		return copyPlatformClipboardPowerShell(ctx, text)
	}
	ptr := winGlobalLock(hMem)
	if ptr == nil {
		globalFree.Call(hMem)
		return copyPlatformClipboardPowerShell(ctx, text)
	}
	dst := unsafe.Slice((*byte)(ptr), bytesNeeded)
	src := unsafe.Slice((*byte)(unsafe.Pointer(&u16[0])), bytesNeeded)
	copy(dst, src)
	globalUnlock.Call(hMem)

	ok := withClipboard(func() bool {
		emptyClipboard.Call()
		res, _, _ := setClipboardData.Call(cfUnicodeText, hMem)
		return res != 0
	})
	if ok {
		return true
	}
	globalFree.Call(hMem)
	return copyPlatformClipboardPowerShell(ctx, text)
}

func platformClipboardImage(ctx context.Context) ([]byte, bool) {
	var result []byte
	var ok bool
	withClipboard(func() bool {
		// 1. Try registered PNG format directly
		if format := pngClipboardFormat(); format != 0 {
			avail, _, _ := isClipboardFormatAvailable.Call(format)
			if avail != 0 {
				hMem, _, _ := getClipboardData.Call(format)
				if hMem != 0 {
					ptr := winGlobalLock(hMem)
					if ptr != nil {
						defer globalUnlock.Call(hMem)
						sz, _, _ := globalSize.Call(hMem)
						if sz > 0 && sz <= uintptr(maxClipboardImage) {
							raw := unsafe.Slice((*byte)(ptr), sz)
							result = bytes.Clone(raw)
							ok = true
							return true
						}
					}
				}
			}
		}

		// 2. Try CF_DIB / CF_DIBV5 (Device-Independent Bitmap)
		for _, dibFmt := range []uintptr{cfDIBV5, cfDIB} {
			avail, _, _ := isClipboardFormatAvailable.Call(dibFmt)
			if avail == 0 {
				continue
			}
			hMem, _, _ := getClipboardData.Call(dibFmt)
			if hMem == 0 {
				continue
			}
			ptr := winGlobalLock(hMem)
			if ptr == nil {
				continue
			}
			sz, _, _ := globalSize.Call(hMem)
			var data []byte
			if sz > 0 && sz <= uintptr(maxClipboardImage) {
				raw := unsafe.Slice((*byte)(ptr), sz)
				data = bytes.Clone(raw)
			}
			globalUnlock.Call(hMem)
			if pngBytes, err := dibToPNG(data); err == nil && len(pngBytes) > 0 {
				result = pngBytes
				ok = true
				return true
			}
		}
		return false
	})
	if ok {
		return result, true
	}
	return platformClipboardImagePowerShell(ctx)
}

func dibToPNG(data []byte) ([]byte, error) {
	if len(data) < 40 {
		return nil, errors.New("dib header too small")
	}
	headerSize := binary.LittleEndian.Uint32(data[0:4])
	if int(headerSize) > len(data) || headerSize < 40 {
		return nil, errors.New("invalid dib header size")
	}
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	heightVal := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	planes := binary.LittleEndian.Uint16(data[12:14])
	bpp := binary.LittleEndian.Uint16(data[14:16])
	compression := binary.LittleEndian.Uint32(data[16:20])

	if planes != 1 || width <= 0 || heightVal == 0 {
		return nil, errors.New("unsupported dib dimensions")
	}
	topDown := heightVal < 0
	height := heightVal
	if topDown {
		height = -height
	}
	if bpp != 24 && bpp != 32 {
		return nil, errors.New("unsupported dib bit count")
	}
	// BI_RGB = 0, BI_BITFIELDS = 3
	if compression != 0 && compression != 3 {
		return nil, errors.New("unsupported dib compression")
	}

	offset := int(headerSize)
	if compression == 3 && headerSize == 40 {
		offset += 12 // 3 RGB bitmasks
	}
	if offset > len(data) {
		return nil, errors.New("pixel offset exceeds dib data")
	}

	rowPitch := ((width*int(bpp) + 31) / 32) * 4
	pixelBytes := data[offset:]
	if len(pixelBytes) < rowPitch*height {
		return nil, errors.New("pixel buffer too small")
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bytesPerPixel := int(bpp) / 8

	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		rowStart := srcY * rowPitch
		for x := 0; x < width; x++ {
			px := rowStart + x*bytesPerPixel
			b := pixelBytes[px]
			g := pixelBytes[px+1]
			r := pixelBytes[px+2]
			a := uint8(255)
			if bytesPerPixel == 4 {
				a = pixelBytes[px+3]
				if a == 0 {
					a = 255
				}
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: a})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Fallbacks using PowerShell
func clipboardPowerShell() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

const clipboardSetup = `[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); Add-Type -AssemblyName System.Windows.Forms; `

func platformClipboardTextPowerShell(ctx context.Context) ([]byte, bool) {
	return runClipboardCommand(ctx, maxClipboardText, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", clipboardSetup+`[Console]::Write([System.Windows.Forms.Clipboard]::GetText())`)
}

func platformClipboardImagePowerShell(ctx context.Context) ([]byte, bool) {
	script := clipboardSetup + `$image = [System.Windows.Forms.Clipboard]::GetImage(); if ($null -eq $image) { exit 1 }; try { $stream = New-Object System.IO.MemoryStream; try { $image.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png); $data = $stream.ToArray(); [Console]::OpenStandardOutput().Write($data, 0, $data.Length) } finally { $stream.Dispose() } } finally { $image.Dispose() }`
	return runClipboardCommand(ctx, maxClipboardImage, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
}

func copyPlatformClipboardPowerShell(ctx context.Context, text string) bool {
	script := clipboardSetup + `$text = [Console]::In.ReadToEnd(); if ($text.Length -eq 0) { [System.Windows.Forms.Clipboard]::Clear() } else { [System.Windows.Forms.Clipboard]::SetText($text) }`
	return runClipboardWriter(ctx, text, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
}

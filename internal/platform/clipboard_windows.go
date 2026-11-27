//go:build windows

package platform

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	kernel32Clip           = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard      = user32.NewProc("OpenClipboard")
	procCloseClipboard     = user32.NewProc("CloseClipboard")
	procEmptyClipboard     = user32.NewProc("EmptyClipboard")
	procGetClipboardData   = user32.NewProc("GetClipboardData")
	procSetClipboardData   = user32.NewProc("SetClipboardData")
	procIsClipboardFormat  = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipFormat = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc        = kernel32Clip.NewProc("GlobalAlloc")
	procGlobalLock         = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock       = kernel32Clip.NewProc("GlobalUnlock")
	procGlobalSize         = kernel32Clip.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

func readClipboard(ctx context.Context) (ClipboardData, error) {
	if r, _, _ := procOpenClipboard.Call(0); r != 0 {
		defer procCloseClipboard.Call()
		pngFmt, _, _ := procRegisterClipFormat.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("PNG"))))
		if pngFmt != 0 {
			if avail, _, _ := procIsClipboardFormat.Call(pngFmt); avail != 0 {
				if b := readGlobalBytes(pngFmt); len(b) > 0 {
					return ClipboardData{ImagePNG: b, MediaType: "image/png"}, nil
				}
			}
		}
		if avail, _, _ := procIsClipboardFormat.Call(cfUnicodeText); avail != 0 {
			if b := readGlobalBytes(cfUnicodeText); len(b) >= 2 {
				u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), len(b)/2)
				return ClipboardData{Text: syscall.UTF16ToString(u16)}, nil
			}
		}
	}
	// Fallback to PowerShell if Win32 clipboard is busy or uses DIB.
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw").Output()
	if err != nil {
		return ClipboardData{}, err
	}
	return ClipboardData{Text: strings.TrimRight(string(out), "\r\n")}, nil
}

func readGlobalBytes(format uintptr) []byte {
	h, _, _ := procGetClipboardData.Call(format)
	if h == 0 {
		return nil
	}
	sz, _, _ := procGlobalSize.Call(h)
	if sz == 0 {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)
	out := make([]byte, sz)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), sz))
	return out
}

func writeClipboard(_ context.Context, text string) error {
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	u16 := syscall.StringToUTF16(text)
	byteLen := len(u16) * 2
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(byteLen))
	if h == 0 {
		return err
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return err
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), byteLen)
	var buf bytes.Buffer
	for _, v := range u16 {
		buf.WriteByte(byte(v))
		buf.WriteByte(byte(v >> 8))
	}
	copy(dst, buf.Bytes())
	procGlobalUnlock.Call(h)
	if res, _, err := procSetClipboardData.Call(cfUnicodeText, h); res == 0 {
		return err
	}
	return nil
}

//go:build windows

package paste

import (
	"context"
	"os"
	"path/filepath"
)

func clipboardPowerShell() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

const clipboardSetup = `[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false); Add-Type -AssemblyName System.Windows.Forms; `

func platformClipboardText(ctx context.Context) ([]byte, bool) {
	return runClipboardCommand(ctx, maxClipboardText, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", clipboardSetup+`[Console]::Write([System.Windows.Forms.Clipboard]::GetText())`)
}

func platformClipboardImage(ctx context.Context) ([]byte, bool) {
	script := clipboardSetup + `$image = [System.Windows.Forms.Clipboard]::GetImage(); if ($null -eq $image) { exit 1 }; try { $stream = New-Object System.IO.MemoryStream; try { $image.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png); $data = $stream.ToArray(); [Console]::OpenStandardOutput().Write($data, 0, $data.Length) } finally { $stream.Dispose() } } finally { $image.Dispose() }`
	return runClipboardCommand(ctx, maxClipboardImage, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
}

func copyPlatformClipboard(ctx context.Context, text string) bool {
	script := clipboardSetup + `$text = [Console]::In.ReadToEnd(); if ($text.Length -eq 0) { [System.Windows.Forms.Clipboard]::Clear() } else { [System.Windows.Forms.Clipboard]::SetText($text) }`
	return runClipboardWriter(ctx, text, clipboardPowerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
}

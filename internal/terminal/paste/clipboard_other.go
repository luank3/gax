//go:build !windows

package paste

import "context"

func platformClipboardText(context.Context) ([]byte, bool)  { return nil, false }
func platformClipboardImage(context.Context) ([]byte, bool) { return nil, false }
func copyPlatformClipboard(context.Context, string) bool    { return false }

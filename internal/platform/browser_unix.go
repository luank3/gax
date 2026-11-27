//go:build !windows

package platform

import (
	"context"
	"os/exec"
	"runtime"
)

func openURL(ctx context.Context, url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.CommandContext(ctx, opener, url).Start()
}

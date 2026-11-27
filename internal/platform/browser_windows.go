//go:build windows

package platform

import (
	"context"
	"os/exec"
)

func openURL(ctx context.Context, url string) error {
	return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url).Start()
}

package paste

import "testing"

func TestX11ClipboardImagePlatformSupport(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd", "openbsd", "netbsd", "dragonfly"} {
		if !supportsX11ClipboardImage(goos) {
			t.Errorf("X11 clipboard image support missing for %s", goos)
		}
	}
	for _, goos := range []string{"darwin", "windows", "plan9", "aix"} {
		if supportsX11ClipboardImage(goos) {
			t.Errorf("X11 clipboard image support unexpectedly enabled for %s", goos)
		}
	}
}

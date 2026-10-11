package browser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenURLHandsTheURLToThePlatformOpener(t *testing.T) {
	directory := t.TempDir()
	recordedURL := filepath.Join(directory, "url.txt")
	release := filepath.Join(directory, "release")
	t.Setenv("GAX_BROWSER_RECORDING", recordedURL)
	t.Setenv("GAX_BROWSER_RELEASE", release)
	script := "#!/bin/sh\n" +
		"while [ ! -e \"$GAX_BROWSER_RELEASE\" ]; do /bin/sleep 0.01; done\n" +
		"/usr/bin/printf '%s' \"$1\" > \"$GAX_BROWSER_RECORDING.tmp\" && /bin/mv \"$GAX_BROWSER_RECORDING.tmp\" \"$GAX_BROWSER_RECORDING\"\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)

	const want = "https://example.test/authorize?x=1&y=2"
	returned := make(chan struct{})
	go func() {
		OpenURL(want)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		_ = os.WriteFile(release, nil, 0o600)
		<-returned
		t.Fatal("OpenURL waited for the browser process to exit")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(recordedURL)
		if err == nil {
			if string(got) != want {
				t.Fatalf("opener received %q, want %q", got, want)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached opener did not receive the URL")
}

func TestPlatformCommandMatchesUpstream(t *testing.T) {
	for _, test := range []struct {
		goos    string
		command string
	}{
		{goos: "darwin", command: "open"},
		{goos: "linux", command: "xdg-open"},
		{goos: "freebsd", command: "xdg-open"},
		{goos: "openbsd", command: "xdg-open"},
	} {
		command, args := openerCommand(test.goos, "https://example.test")
		if command != test.command || len(args) != 1 || args[0] != "https://example.test" {
			t.Errorf("openerCommand(%q) = %q %q", test.goos, command, args)
		}
	}
}

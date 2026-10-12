package app

import (
	"path/filepath"
	"testing"
)

// prepareTTYAppEnv points a test run at a scripted provider. It lives beside, but outside, the
// Linux-only PTY tests: the one-shot tests in app_test.go need it on every platform.
func prepareTTYAppEnv(t *testing.T, baseURL string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", baseURL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_NO_SESSION", "on")
}

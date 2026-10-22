//go:build windows

package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWindowsSessionBucketPreservesUTF8(t *testing.T) {
	cwd := `C:\` + strings.Repeat("界", 50)
	bucket := EncodeCWD(cwd)
	if !utf8.ValidString(bucket) || strings.ContainsAny(bucket, `\/:*?"<>|`) {
		t.Errorf("EncodeCWD(%q) = %q, want valid UTF-8 Windows filename", cwd, bucket)
	}
}

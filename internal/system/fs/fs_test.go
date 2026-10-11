package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileCappedReportsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := ReadFileCapped(path, 2)
	if err != nil || !truncated || string(data) != "ab" {
		t.Fatalf("ReadFileCapped() = (%q, %t, %v), want (ab, true, nil)", data, truncated, err)
	}
	data, truncated, err = ReadFileCapped(path, 3)
	if err != nil || truncated || string(data) != "abc" {
		t.Fatalf("ReadFileCapped(exact) = (%q, %t, %v), want (abc, false, nil)", data, truncated, err)
	}
}

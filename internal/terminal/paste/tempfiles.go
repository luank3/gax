package paste

import (
	"bytes"
	"io"
	"os"

	"github.com/luank3/gax/internal/system/tempfiles"
)

func createTrackedClipboardImage(data []byte, extension string) (string, bool) {
	file, path, err := tempfiles.Create("paste-", extension)
	if err != nil {
		return "", false
	}
	n, writeErr := io.Copy(file, bytes.NewReader(data))
	if writeErr == nil && n != int64(len(data)) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		tempfiles.Untrack(path)
		return "", false
	}
	return path, true
}

// Cleanup removes tracked temporary files created by clipboard capture and other subsystems.
func Cleanup() error {
	return tempfiles.Cleanup()
}

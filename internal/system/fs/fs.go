// Package fs provides bounded reads of regular files for user and system data.
package fs

import (
	"errors"
	"io"
	"os"
)

// ReadFileCapped reads at most limit bytes and reports whether additional bytes were present.
// It rejects non-regular files before reading so special files cannot block a caller.
func ReadFileCapped(path string, limit int64) ([]byte, bool, error) {
	if limit < 0 {
		return nil, false, errors.New("negative file size limit")
	}
	file, err := OpenRegular(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) < limit {
		return data, false, nil
	}
	var extra [1]byte
	count, err := file.Read(extra[:])
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return data, count > 0, nil
}

// ReadFile reads a regular file to EOF without blocking on special files.
func ReadFile(path string) ([]byte, error) {
	file, err := OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// OpenRegular opens a regular file without blocking on a FIFO or device.
func OpenRegular(path string) (*os.File, error) { return openRegular(path) }

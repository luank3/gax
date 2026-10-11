package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/conversation"
	systempath "github.com/luank3/gax/internal/system/path"
)

// ForkFile writes an independent session containing an inherited copy of items and the live
// selection. The source is left untouched.
func ForkFile(sourcePath string, items []conversation.Item, selection Selection) (string, error) {
	header, err := readSessionHeader(sourcePath)
	if err != nil {
		return "", err
	}
	id, fileTime, headerTime, err := newIdentity(time.Now())
	if err != nil {
		return "", err
	}
	sourceID := rawString(header["id"])
	setHeaderString(header, "id", &id)
	setHeaderString(header, "timestamp", &headerTime)
	setHeaderString(header, "forked_from", sourceID)
	setHeaderString(header, "provider", nonempty(selection.Provider))
	setHeaderString(header, "model", nonempty(selection.Model))
	setHeaderString(header, "model_label", differingModelLabel(selection.ModelLabel, selection.Model))
	setHeaderString(header, "effort", nonempty(selection.Effort))
	setHeaderString(header, "preset", nonempty(selection.Preset))

	path := systempath.Join(systempath.Dir(sourcePath), fmt.Sprintf("%s_%s.jsonl", fileTime, id))
	file, err := createForkFile(path)
	if err != nil {
		return "", err
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()
	if err := writeJSONLine(file, header); err != nil {
		_ = file.Close()
		return "", err
	}
	for _, item := range items {
		item.Inherited = true
		if err := writeJSONLine(file, item); err != nil {
			_ = file.Close()
			return "", err
		}
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	created = false
	return path, nil
}

func readSessionHeader(path string) (map[string]json.RawMessage, error) {
	var header map[string]json.RawMessage
	err := readLines(path, func(line []byte) error {
		if header != nil {
			return nil
		}
		object, err := decodeObject(line)
		if err != nil {
			return err
		}
		recordType := rawString(object["type"])
		if recordType == nil || *recordType != "session" {
			return errors.New("session file has no header record")
		}
		header = object
		return nil
	})
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, errors.New("session file has no header record")
	}
	return header, nil
}

func setHeaderString(object map[string]json.RawMessage, key string, value *string) {
	if value == nil || *value == "" {
		delete(object, key)
		return
	}
	encoded, _ := json.Marshal(*value)
	object[key] = encoded
}

func createForkFile(path string) (*os.File, error) {
	directory := systempath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("cannot create fork session file")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return file, nil
}

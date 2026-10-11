package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	systempath "github.com/luank3/gax/internal/system/path"
)

const (
	filenameTimeFormat = "2006-01-02T15-04-05Z"
	headerTimeFormat   = "2006-01-02T15:04:05Z"
)

func StateRoot() (string, error) {
	path, err := statePath("")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, "/"), nil
}

func statePath(relative string) (string, error) {
	path, ok := systempath.XDGHaxPath(os.Getenv("XDG_STATE_HOME"), os.Getenv("HOME"), ".local/state", relative)
	if !ok {
		return "", errors.New("cannot resolve XDG state directory without HOME or XDG_STATE_HOME")
	}
	return path, nil
}

func EncodeCWD(cwd string) string {
	if cwd == "" {
		cwd = "unknown"
	}
	hash := uint64(1469598103934665603)
	for i := 0; i < len(cwd); i++ {
		hash ^= uint64(cwd[i])
		hash *= 1099511628211
	}
	relative := strings.TrimLeft(cwd, "/")
	if relative == "" {
		relative = "root"
	}
	if len(relative) > 79 {
		relative = relative[:79]
	}
	slug := strings.ReplaceAll(relative, "/", "-")
	return fmt.Sprintf("%s.%016x", slug, hash)
}

func Directory(cwd string) (string, error) {
	return statePath("sessions/" + EncodeCWD(cwd))
}

// PromptHistoryPath returns the cwd-scoped prompt history file beside session logs.
func PromptHistoryPath(cwd string) (string, error) {
	if cwd == "" {
		return "", errors.New("cannot resolve prompt history without a working directory")
	}
	directory, err := Directory(cwd)
	if err != nil {
		return "", err
	}
	return systempath.Join(directory, "history"), nil
}

func newIdentity(now time.Time) (string, string, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", "", err
	}
	random[6] = random[6]&0x0f | 0x40
	random[8] = random[8]&0x3f | 0x80
	encoded := make([]byte, 32)
	hex.Encode(encoded, random[:])
	id := fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
	utc := now.UTC()
	return id, utc.Format(filenameTimeFormat), utc.Format(headerTimeFormat), nil
}

func PathIsStandard(path string) bool {
	base := filepath.Base(path)
	if len(base) != 63 || base[20] != '_' || base[57:] != ".jsonl" {
		return false
	}
	for index, char := range base[:20] {
		if index == 4 || index == 7 || index == 13 || index == 16 {
			if char != '-' {
				return false
			}
		} else if index == 10 {
			if char != 'T' {
				return false
			}
		} else if index == 19 {
			if char != 'Z' {
				return false
			}
		} else if char < '0' || char > '9' {
			return false
		}
	}
	id := base[21:57]
	for i, char := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}

func idFromPath(path string) *string {
	if !PathIsStandard(path) {
		return nil
	}
	id := filepath.Base(path)[21:57]
	return &id
}

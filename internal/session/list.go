package session

import (
	"os"
	"sort"
	"strings"
	"time"

	systempath "github.com/luank3/gax/internal/system/path"
)

type Entry struct {
	Path    string
	ID      *string
	ModTime time.Time
}

func List(cwd string, retentionDays int) ([]Entry, error) {
	directory, err := Directory(cwd)
	if err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := RetentionCutoff(retentionDays)
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := systempath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		id := idFromPath(path)
		mtime := info.ModTime()
		if id != nil && !cutoff.IsZero() && mtime.Before(cutoff) {
			continue
		}
		result = append(result, Entry{Path: path, ID: id, ModTime: mtime})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if !left.ModTime.Equal(right.ModTime) {
			return left.ModTime.After(right.ModTime)
		}
		return left.Path > right.Path
	})
	return result, nil
}

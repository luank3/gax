package input

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"

	systempath "github.com/luank3/gax/internal/system/path"
)

const (
	historyRecordMax   = 65536
	historyBloatFactor = 3
)

// HistoryFile loads prompt history and optionally appends submitted prompts to its file. Session
// only entries (for example, a draft cleared with Ctrl-C) stay in memory until submitted.
type HistoryFile struct {
	path              string
	persist           bool
	newestUnpersisted bool
}

// OpenHistory loads existing history without failing interactive startup when the file is absent
// or unreadable. Persistence creates the containing directory with private permissions.
func OpenHistory(core *Core, path string, persist bool) *HistoryFile {
	history := &HistoryFile{path: path, persist: persist}
	if core == nil || path == "" {
		return history
	}
	if persist {
		_ = os.MkdirAll(systempath.Dir(path), 0o700)
	}
	loaded := loadHistoryFile(core, path)
	if persist && loaded > HistoryLimit*historyBloatFactor {
		rewriteHistoryFile(core, path)
	}
	return history
}

// Add records a submitted prompt in memory and persists the newest prompt once when enabled.
func (history *HistoryFile) Add(core *Core, line string) {
	if core == nil || line == "" {
		return
	}
	changed := core.AddHistory(line)
	if !changed && !history.newestUnpersisted {
		return
	}
	history.newestUnpersisted = false
	if history.persist && history.path != "" {
		appendHistoryFile(history.path, line)
	}
}

// AddSession adds a recallable entry without writing it to disk.
func (history *HistoryFile) AddSession(core *Core, line string) {
	if core != nil && line != "" && core.AddHistory(line) {
		history.newestUnpersisted = true
	}
}

func loadHistoryFile(core *Core, path string) int {
	file := openHistoryFile(path, os.O_RDONLY, 0)
	if file == nil {
		return 0
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 4096)
	record := make([]byte, 0, 256)
	overflow := false
	loaded := 0
	for {
		part, readErr := reader.ReadSlice('\n')
		lineEnd := len(part) > 0 && part[len(part)-1] == '\n'
		payload := part
		if lineEnd {
			payload = part[:len(part)-1]
		}
		if len(record)+len(payload) > historyRecordMax-1 {
			overflow = true
		} else if !overflow {
			record = append(record, payload...)
		}
		complete := lineEnd || errors.Is(readErr, io.EOF) && (len(part) > 0 || len(record) > 0 || overflow)
		if complete {
			loaded++
			if !overflow {
				entry := strings.TrimRight(string(record), "\r")
				if entry != "" {
					core.AddHistory(DecodeHistory(entry))
				}
			}
			record = record[:0]
			overflow = false
		}
		if errors.Is(readErr, io.EOF) {
			return loaded
		}
		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			return loaded
		}
	}
}

func appendHistoryFile(path, line string) {
	encoded := EncodeHistory(line)
	if len(encoded)+1 > historyRecordMax {
		return
	}
	file := openHistoryFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if file == nil {
		return
	}
	defer file.Close()
	record := append([]byte(encoded), '\n')
	_, _ = file.Write(record) // Do not retry short writes and risk splitting a record across writers.
}

func rewriteHistoryFile(core *Core, path string) {
	temporary, err := os.CreateTemp(systempath.Dir(path), ".gax-hist.*")
	if err != nil {
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return
	}
	for _, entry := range core.HistoryEntries() {
		if _, err := io.WriteString(temporary, EncodeHistory(entry)+"\n"); err != nil {
			_ = temporary.Close()
			return
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return
	}
	if err := temporary.Close(); err != nil {
		return
	}
	_ = os.Rename(temporaryPath, path)
}

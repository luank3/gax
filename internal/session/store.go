// Package session implements append-only hax v1 JSONL session persistence,
// lazy loading, /undo, /fork, per-directory prompt history, and retention pruning.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
)

// SelectionSnapshot records the active provider/model/effort/preset.
type SelectionSnapshot struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Preset   string `json:"preset,omitempty"`
}

// Summary describes one session file for --resume / /resume listing.
type Summary struct {
	ID        string
	Path      string
	Timestamp time.Time
	Preview   string
	Provider  string
	Model     string
}

// Loaded holds the reconstructed state of a session file.
type Loaded struct {
	Path      string
	Header    core.SessionHeader
	Selection SelectionSnapshot
	AllItems  []core.Item // includes undone items for usage accounting
	LiveItems []core.Item // active items after applying undo records
}

// Dir returns the per-cwd session directory under stateDir.
func Dir(stateDir, cwd string) string {
	return filepath.Join(stateDir, "sessions", platform.ProjectBucket(cwd))
}

// NewUUID generates a random v4-style UUID string.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// Writer appends JSONL records to a session file.
type Writer struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// Create creates a new session file and writes its header record.
func Create(stateDir, cwd string, header core.SessionHeader) (*Writer, error) {
	dir := Dir(stateDir, cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if header.ID == "" {
		header.ID = NewUUID()
	}
	if header.Version == 0 {
		header.Version = 1
	}
	if header.Timestamp == "" {
		header.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if header.CWD == "" {
		header.CWD = cwd
	}
	ts := time.Now().UTC().Format("20060102-150405")
	path := filepath.Join(dir, ts+"_"+header.ID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	w := &Writer{path: path, file: f}

	rec := struct {
		Type string `json:"type"`
		core.SessionHeader
	}{Type: "session", SessionHeader: header}
	if err := w.writeJSON(rec); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// OpenAppend opens an existing session file for appending.
func OpenAppend(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{path: path, file: f}, nil
}

// Path returns the session file path.
func (w *Writer) Path() string { return w.path }

// Close closes the underlying file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// AppendItem writes a conversation item record.
func (w *Writer) AppendItem(it core.Item) error {
	return w.writeJSON(it)
}

// AppendSelection writes a selection snapshot control record.
func (w *Writer) AppendSelection(sel SelectionSnapshot) error {
	return w.writeJSON(struct {
		Type string `json:"type"`
		SelectionSnapshot
	}{Type: "selection", SelectionSnapshot: sel})
}

// AppendUndo writes an undo control record.
func (w *Writer) AppendUndo(keepUserTurns int) error {
	return w.writeJSON(struct {
		Type          string `json:"type"`
		KeepUserTurns int    `json:"keep_user_turns"`
	}{Type: "undo", KeepUserTurns: keepUserTurns})
}

// AppendUserTurn writes a user_turn duration record.
func (w *Writer) AppendUserTurn(elapsedMS int64) error {
	return w.writeJSON(struct {
		Type      string `json:"type"`
		ElapsedMS int64  `json:"elapsed_ms"`
	}{Type: "user_turn", ElapsedMS: elapsedMS})
}

func (w *Writer) writeJSON(v any) error {
	if w == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return fmt.Errorf("session writer closed")
	}
	_, err = w.file.Write(b)
	return err
}

// Load reads a session file lazily, skipping any truncated final line and applying undo records.
func Load(path string) (*Loaded, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &Loaded{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var probe struct {
			Type          string `json:"type"`
			Kind          string `json:"kind"`
			KeepUserTurns int    `json:"keep_user_turns"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			// Skip partial/corrupt line (e.g., crash mid-write).
			continue
		}
		switch probe.Type {
		case "session":
			_ = json.Unmarshal([]byte(line), &res.Header)
			res.Selection = SelectionSnapshot{
				Provider: res.Header.Provider,
				Model:    res.Header.Model,
				Effort:   res.Header.Effort,
				Preset:   res.Header.Preset,
			}
		case "selection":
			var sel SelectionSnapshot
			if json.Unmarshal([]byte(line), &sel) == nil {
				res.Selection = sel
			}
		case "undo":
			res.LiveItems = applyUndo(res.LiveItems, probe.KeepUserTurns)
		default:
			if probe.Kind != "" {
				var it core.Item
				if json.Unmarshal([]byte(line), &it) == nil {
					res.AllItems = append(res.AllItems, it)
					res.LiveItems = append(res.LiveItems, it)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if res.Header.ID == "" {
		return nil, fmt.Errorf("invalid session %q: missing header", path)
	}
	return res, nil
}

func applyUndo(items []core.Item, keepUserTurns int) []core.Item {
	if keepUserTurns <= 0 {
		return nil
	}
	userPrompts := 0
	for i, it := range items {
		if it.Kind == core.KindUser && it.Origin == core.OriginNone {
			userPrompts++
			if userPrompts > keepUserTurns {
				cut := i
				if cut > 0 && items[cut-1].Kind == core.KindTurnBoundary {
					cut--
				}
				return append([]core.Item(nil), items[:cut]...)
			}
		}
	}
	return items
}

// List returns session summaries for cwd, newest first.
func List(stateDir, cwd string) ([]Summary, error) {
	dir := Dir(stateDir, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Summary
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, de.Name())
		loaded, err := Load(p)
		if err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, loaded.Header.Timestamp)
		preview := ""
		for _, it := range loaded.LiveItems {
			if it.Kind == core.KindUser && it.Origin == core.OriginNone && it.Text != "" {
				preview = strings.ReplaceAll(strings.TrimSpace(it.Text), "\n", " ")
				if len(preview) > 80 {
					preview = preview[:77] + "..."
				}
				break
			}
		}
		out = append(out, Summary{
			ID:        loaded.Header.ID,
			Path:      p,
			Timestamp: ts,
			Preview:   preview,
			Provider:  loaded.Selection.Provider,
			Model:     loaded.Selection.Model,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out, nil
}

// FindByID resolves a full UUID or unique prefix in cwd's session directory.
func FindByID(stateDir, cwd, idOrPrefix string) (string, error) {
	list, err := List(stateDir, cwd)
	if err != nil {
		return "", err
	}
	var matches []Summary
	for _, s := range list {
		if s.ID == idOrPrefix {
			return s.Path, nil
		}
		if strings.HasPrefix(s.ID, idOrPrefix) {
			matches = append(matches, s)
		}
	}
	if len(matches) == 1 {
		return matches[0].Path, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous session prefix %q (%d matches)", idOrPrefix, len(matches))
	}
	return "", fmt.Errorf("session %q not found", idOrPrefix)
}

// Latest returns the path of the newest session for cwd, if any.
func Latest(stateDir, cwd string) (string, error) {
	list, err := List(stateDir, cwd)
	if err != nil || len(list) == 0 {
		return "", err
	}
	return list[0].Path, nil
}

// Fork creates a new session copying LiveItems up to keepUserTurns (0 = all) marked Inherited: true.
func Fork(srcPath, stateDir, cwd, gaxVersion string, keepUserTurns int) (*Writer, error) {
	loaded, err := Load(srcPath)
	if err != nil {
		return nil, err
	}
	items := loaded.LiveItems
	if keepUserTurns > 0 {
		items = applyUndo(items, keepUserTurns)
	}
	hdr := loaded.Header
	hdr.ID = NewUUID()
	hdr.Timestamp = time.Now().UTC().Format(time.RFC3339)
	hdr.GaxVersion = gaxVersion
	hdr.ForkedFrom = loaded.Header.ID
	hdr.Provider = loaded.Selection.Provider
	hdr.Model = loaded.Selection.Model
	hdr.Effort = loaded.Selection.Effort
	hdr.Preset = loaded.Selection.Preset

	w, err := Create(stateDir, cwd, hdr)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		it.Inherited = true
		if err := w.AppendItem(it); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	return w, nil
}

// Prune removes session files inactive for more than retentionDays, never touching keepPath or history.
func Prune(stateDir string, retentionDays int, keepPath string) {
	if retentionDays <= 0 || stateDir == "" {
		return
	}
	sessionsRoot := filepath.Join(stateDir, "sessions")
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	keepInfo, keepErr := os.Stat(keepPath)
	hasKeep := keepErr == nil

	dirs, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(sessionsRoot, d.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			p := filepath.Join(sub, f.Name())
			info, err := f.Info()
			if err != nil {
				continue
			}
			if hasKeep && os.SameFile(keepInfo, info) {
				continue
			}
			if info.ModTime().Before(cutoff) {
				_ = os.Remove(p)
			}
		}
	}
}

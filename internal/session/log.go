package session

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/conversation"
	terminalgit "github.com/luank3/gax/internal/system/git"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/version"
)

const FormatVersion = 1

type Selection struct {
	Provider   *string
	Model      *string
	ModelLabel *string
	Effort     *string
	Preset     *string
}

type HeaderInput struct {
	ID         *string
	Timestamp  *string
	CWD        *string
	Provider   *string
	Model      *string
	ModelLabel *string
	Effort     *string
	Preset     *string
	ForkedFrom *string
}

type HeaderRecord struct {
	Type       string  `json:"type"`
	Version    int     `json:"version"`
	GaxVersion string  `json:"gax_version"`
	ID         *string `json:"id,omitempty"`
	Timestamp  *string `json:"timestamp,omitempty"`
	CWD        *string `json:"cwd,omitempty"`
	Provider   *string `json:"provider,omitempty"`
	Model      *string `json:"model,omitempty"`
	ModelLabel *string `json:"model_label,omitempty"`
	Effort     *string `json:"effort,omitempty"`
	Preset     *string `json:"preset,omitempty"`
	GitBranch  *string `json:"git_branch,omitempty"`
	GitCommit  *string `json:"git_commit,omitempty"`
	GitSubject *string `json:"git_subject,omitempty"`
	ForkedFrom *string `json:"forked_from,omitempty"`
}

func NewHeaderRecord(input HeaderInput) HeaderRecord {
	branch, commit, subject := gitState(input.CWD)
	return HeaderRecord{
		Type:       "session",
		Version:    FormatVersion,
		GaxVersion: version.Value,
		ID:         cloneString(input.ID),
		Timestamp:  cloneString(input.Timestamp),
		CWD:        cloneString(input.CWD),
		Provider:   cloneString(input.Provider),
		Model:      cloneString(input.Model),
		ModelLabel: differingModelLabel(input.ModelLabel, input.Model),
		Effort:     cloneString(input.Effort),
		Preset:     cloneString(input.Preset),
		GitBranch:  branch,
		GitCommit:  commit,
		GitSubject: subject,
		ForkedFrom: cloneString(input.ForkedFrom),
	}
}

type selectionRecord struct {
	Type       string  `json:"type"`
	Provider   *string `json:"provider,omitempty"`
	Model      *string `json:"model,omitempty"`
	ModelLabel *string `json:"model_label,omitempty"`
	Effort     *string `json:"effort,omitempty"`
	Preset     *string `json:"preset,omitempty"`
}

type Log struct {
	mu               sync.Mutex
	file             *os.File
	path             string
	headerWritten    bool
	selectionPending bool
	writtenItems     int
	id               *string
	cwd              string
	timestamp        string
	selection        Selection
	closed           bool
}

func NewLog(cwd string, selection Selection) (*Log, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	selection = cloneSelection(selection)
	selection.Preset = nonempty(selection.Preset)
	log := &Log{selection: selection}
	if err := log.prepareFresh(cwd); err != nil {
		return nil, err
	}
	return log, nil
}

func ResumeLog(path string, selection Selection, loadedItemCount int) (*Log, error) {
	selection = cloneSelection(selection)
	selection.Preset = nonempty(selection.Preset)
	file, err := openSessionFile(path, false)
	if err != nil {
		return nil, err
	}
	return &Log{
		file:          file,
		path:          path,
		headerWritten: true,
		writtenItems:  loadedItemCount,
		id:            idFromPath(path),
		selection:     selection,
	}, nil
}

func (log *Log) prepareFresh(cwd string) error {
	directory, err := Directory(cwd)
	if err != nil {
		return err
	}
	id, fileTime, headerTime, err := newIdentity(time.Now())
	if err != nil {
		return err
	}
	log.cwd = cwd
	log.id = &id
	log.timestamp = headerTime
	log.path = systempath.Join(directory, fileTime+"_"+id+".jsonl")
	log.file = nil
	log.headerWritten = false
	log.selectionPending = false
	log.writtenItems = 0
	log.closed = false
	return nil
}

func (log *Log) Path() string {
	if log == nil {
		return ""
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.path
}

func (log *Log) ID() *string {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	return cloneString(log.id)
}

func (log *Log) ResumeHint() *string {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if !log.headerWritten {
		return nil
	}
	return cloneString(log.id)
}

func (log *Log) Materialized() bool {
	if log == nil {
		return false
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.headerWritten
}

func (log *Log) Begin() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.headerWritten {
		return nil
	}
	if err := log.materialize(); err != nil {
		return err
	}
	if err := log.writeHeader(); err != nil {
		return err
	}
	log.headerWritten = true
	return nil
}

func (log *Log) Append(items []conversation.Item) error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed || len(items) <= log.writtenItems {
		return nil
	}
	if err := log.materialize(); err != nil {
		return err
	}
	if !log.headerWritten {
		if err := log.writeHeader(); err != nil {
			return err
		}
		log.headerWritten = true
	}
	if log.selectionPending {
		if err := writeJSONLine(log.file, selectionRecordFor(log.selection)); err != nil {
			return err
		}
		log.selectionPending = false
	}
	for i := log.writtenItems; i < len(items); i++ {
		if err := writeJSONLine(log.file, items[i]); err != nil {
			return err
		}
		log.writtenItems = i + 1
	}
	return nil
}

func (log *Log) SetSelection(selection Selection) {
	if log == nil {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return
	}
	selection = cloneSelection(selection)
	selection.Preset = nonempty(selection.Preset)
	if selectionEqual(log.selection, selection) {
		log.selection.ModelLabel = selection.ModelLabel
		return
	}
	log.selection = selection
	if log.headerWritten {
		log.selectionPending = true
	}
}

func (log *Log) DiscardSelection() {
	if log == nil {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	log.selectionPending = false
}

func (log *Log) Reset() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return nil
	}
	if log.file != nil {
		_ = log.file.Close()
		log.file = nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.path = ""
		return err
	}
	if err := log.prepareFresh(cwd); err != nil {
		log.path = ""
		return err
	}
	return nil
}

func (log *Log) Close() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return nil
	}
	log.closed = true
	if log.file == nil {
		return nil
	}
	err := log.file.Close()
	log.file = nil
	return err
}

func (log *Log) Undo(keepUserTurns, newItemCount int) error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil || !log.headerWritten {
		return nil
	}
	if err := writeJSONLine(log.file, struct {
		Type          string `json:"type"`
		KeepUserTurns int    `json:"keep_user_turns"`
	}{"undo", keepUserTurns}); err != nil {
		return err
	}
	log.writtenItems = newItemCount
	return nil
}

func (log *Log) UserTurn(elapsedMS int64) error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil || !log.headerWritten || elapsedMS < 0 {
		return nil
	}
	return writeJSONLine(log.file, struct {
		Type      string `json:"type"`
		ElapsedMS int64  `json:"elapsed_ms"`
	}{"user_turn", elapsedMS})
}

func (log *Log) materialize() error {
	if log.file != nil {
		return nil
	}
	if log.path == "" {
		return errors.New("session path is unavailable")
	}
	directory := systempath.Dir(log.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(systempath.Dir(directory), 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := openSessionFile(log.path, true)
	if err != nil {
		return err
	}
	log.file = file
	return nil
}

func (log *Log) writeHeader() error {
	input := HeaderInput{
		ID:         log.id,
		Timestamp:  &log.timestamp,
		CWD:        &log.cwd,
		Provider:   log.selection.Provider,
		Model:      log.selection.Model,
		ModelLabel: log.selection.ModelLabel,
		Effort:     log.selection.Effort,
		Preset:     log.selection.Preset,
	}
	return writeJSONLine(log.file, NewHeaderRecord(input))
}

func openSessionFile(path string, create bool) (*os.File, error) {
	flags := syscall.O_RDWR | syscall.O_APPEND | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if create {
		flags = syscall.O_WRONLY | syscall.O_CREAT | syscall.O_TRUNC | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	}
	fd, err := syscall.Open(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("cannot open session file")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	locked := syscall.Flock(int(file.Fd()), syscall.LOCK_SH) == nil
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || locked && !hasLinks(info) {
		_ = file.Close()
		return nil, errors.New("session file was removed or is not regular")
	}
	if !create {
		if info.Size() > 0 {
			var last [1]byte
			if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
				file.Close()
				return nil, err
			}
			if last[0] != '\n' {
				if _, err := file.Write([]byte{'\n'}); err != nil {
					file.Close()
					return nil, err
				}
			}
		}
	}
	return file, nil
}

func writeJSONLine(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func selectionRecordFor(selection Selection) selectionRecord {
	return selectionRecord{
		Type:       "selection",
		Provider:   cloneString(selection.Provider),
		Model:      cloneString(selection.Model),
		ModelLabel: differingModelLabel(selection.ModelLabel, selection.Model),
		Effort:     cloneString(selection.Effort),
		Preset:     cloneString(selection.Preset),
	}
}

func cloneSelection(selection Selection) Selection {
	return Selection{
		Provider:   cloneString(selection.Provider),
		Model:      cloneString(selection.Model),
		ModelLabel: cloneString(selection.ModelLabel),
		Effort:     cloneString(selection.Effort),
		Preset:     cloneString(selection.Preset),
	}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func nonempty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func stringsEqual(a, b *string) bool {
	if a == nil || *a == "" {
		return b == nil || *b == ""
	}
	return b != nil && *a == *b
}

func selectionEqual(a, b Selection) bool {
	return stringsEqual(a.Provider, b.Provider) && stringsEqual(a.Model, b.Model) &&
		stringsEqual(a.Effort, b.Effort) && stringsEqual(a.Preset, b.Preset)
}

func differingModelLabel(label, model *string) *string {
	if label == nil || model == nil || *label == *model {
		return nil
	}
	return cloneString(label)
}

func gitState(cwd *string) (*string, *string, *string) {
	if cwd == nil || *cwd == "" {
		return nil, nil, nil
	}
	return terminalgit.State(*cwd)
}

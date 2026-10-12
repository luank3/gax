package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/luank3/gax/internal/bashcompress"
	"github.com/luank3/gax/internal/subagent"
	"github.com/luank3/gax/internal/system/tempfiles"
)

const bashDrainLimit = 16 * 1024 * 1024
const bashOutputMemoryCapMax = bashDrainLimit - 64*1024
const bashOutputLinesCap = 2000

type commandCapture struct {
	mu             sync.Mutex
	memory         []byte
	memoryCap      int64
	newlineCount   int64
	hasPartialLine bool
	spilled        bool
	file           *os.File
	path           string
	onPreserve     func(string)
	total          int64
	stored         int64
	limit          int64
	binary         bool
	overflow       bool
	limitReached   bool
	taskMode       bool
	writeFailed    bool
	preserved      bool
	onOverflow     func()
	display        DisplaySink
	displayFrom    int64
	displayedBody  bool
	displayLast    byte
	closed         bool
}

func newCommandCapture(memoryCaps ...int) (*commandCapture, error) {
	memoryCap := defaultOutputCap
	if len(memoryCaps) > 0 {
		memoryCap = memoryCaps[0]
	}
	if memoryCap > bashOutputMemoryCapMax {
		memoryCap = bashOutputMemoryCapMax
	}
	if memoryCap < 0 {
		memoryCap = 0
	}
	return &commandCapture{memoryCap: int64(memoryCap), limit: bashDrainLimit}, nil
}

func (capture *commandCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	from := capture.total
	capture.total += int64(len(data))
	if !capture.binary && bytes.IndexByte(data, 0) >= 0 {
		capture.binary = true
	}
	writeCount := int64(len(data))
	newLimit := false
	if capture.taskMode {
		if !capture.overflow && capture.total >= capture.limit {
			capture.overflow = true
			capture.limitReached = true
			newLimit = true
			writeCount = 0
		} else if capture.overflow {
			writeCount = 0
		}
	} else if !capture.limitReached && capture.limit > 0 && capture.total >= capture.limit {
		capture.limitReached = true
		newLimit = true
	}
	var preservePath string
	var preserveCallback func(string)
	if writeCount > 0 {
		storedData := data[:int(writeCount)]
		capture.newlineCount += int64(bytes.Count(storedData, []byte{'\n'}))
		capture.hasPartialLine = storedData[len(storedData)-1] != '\n'
		if !capture.writeFailed || !capture.spilled {
			if !capture.spilled {
				lineCount := capture.newlineCount
				if capture.hasPartialLine {
					lineCount++
				}
				if int64(len(capture.memory))+writeCount > capture.memoryCap || lineCount > bashOutputLinesCap {
					capture.spillLocked()
					if capture.preserved && capture.path != "" {
						preservePath, preserveCallback = capture.path, capture.onPreserve
					}
				}
			}
			if !capture.writeFailed {
				if capture.spilled {
					written, err := capture.file.Write(storedData)
					capture.stored += int64(written)
					if err != nil || written != len(storedData) {
						capture.writeFailed = true
					}
				} else {
					capture.memory = append(capture.memory, storedData...)
					capture.stored += writeCount
				}
			}
		}
	}
	if capture.display != nil && !capture.taskMode && !capture.binary {
		start := int64(0)
		if capture.displayFrom > from {
			start = capture.displayFrom - from
		}
		if start < int64(len(data)) {
			chunk := data[start:]
			_ = capture.display(chunk)
			if len(chunk) > 0 {
				capture.displayedBody = true
				capture.displayLast = chunk[len(chunk)-1]
			}
		}
	}
	kill := capture.onOverflow
	capture.mu.Unlock()
	if preservePath != "" && preserveCallback != nil {
		preserveCallback(preservePath)
	}
	if newLimit && kill != nil {
		kill()
	}
	return len(data), nil
}

// spillLocked moves captured memory to a private tracked file. The caller holds capture.mu.
func (capture *commandCapture) spillLocked() {
	capture.spilled = true
	file, path, err := tempfiles.Create("bash-", ".log")
	if err != nil {
		capture.writeFailed = true
		return
	}
	capture.file = file
	capture.path = path
	if len(capture.memory) > 0 {
		written, writeErr := file.Write(capture.memory)
		if writeErr != nil || written != len(capture.memory) {
			capture.writeFailed = true
		}
		capture.memory = nil
	}
}

func (capture *commandCapture) readAtLocked(from int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if capture.file != nil {
		_, err := capture.file.ReadAt(data, from)
		return err
	}
	copy(data, capture.memory[from:from+int64(len(data))])
	return nil
}

// setDisplay streams any already-captured bytes from `from`, then subscribes to later output.
// Holding the capture lock while invoking the synchronous sink preserves byte order with writes.
func (capture *commandCapture) setDisplay(display DisplaySink, from int64) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if display == nil {
		capture.display = nil
		capture.displayFrom = 0
		capture.displayedBody = false
		capture.displayLast = 0
		return
	}
	capture.displayedBody = false
	capture.displayLast = 0
	if from < 0 {
		from = 0
	}
	if from > capture.stored {
		from = capture.stored
	}
	length := capture.stored - from
	data := make([]byte, length)
	if length > 0 {
		if err := capture.readAtLocked(from, data); err != nil && !errors.Is(err, io.EOF) {
			capture.writeFailed = true
			data = nil
		}
	}
	if len(data) > 0 && !capture.binary && !capture.writeFailed {
		_ = display(data)
		capture.displayedBody = true
		capture.displayLast = data[len(data)-1]
	}
	capture.display = display
	capture.displayFrom = capture.total
}

func (capture *commandCapture) appendDisplayMessage(message string, separateFromBody bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.display == nil || message == "" {
		return
	}
	if separateFromBody && capture.displayedBody {
		_ = capture.display([]byte("\n"))
		capture.displayLast = '\n'
	}
	_ = capture.display([]byte(message))
	capture.displayedBody = true
	capture.displayLast = message[len(message)-1]
}

func (capture *commandCapture) displayTaskWait(body, footer string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.display == nil {
		return
	}
	if !capture.displayedBody && body != "" {
		_ = capture.display([]byte(body))
		capture.displayedBody = true
		capture.displayLast = body[len(body)-1]
	}
	if capture.displayedBody {
		_ = capture.display([]byte("\n"))
		capture.displayLast = '\n'
	}
	if footer != "" {
		_ = capture.display([]byte(footer))
		capture.displayedBody = true
		capture.displayLast = footer[len(footer)-1]
	}
}

func (capture *commandCapture) displayState() (bool, byte) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.displayedBody, capture.displayLast
}

func (capture *commandCapture) setOverflowHandler(handler func()) {
	capture.mu.Lock()
	capture.onOverflow = handler
	overflow := capture.limitReached
	capture.mu.Unlock()
	if overflow && handler != nil {
		handler()
	}
}

func (capture *commandCapture) setTaskMode() {
	capture.mu.Lock()
	capture.taskMode = true
	capture.mu.Unlock()
}

func (capture *commandCapture) preserve() {
	capture.mu.Lock()
	if capture.preserved {
		capture.mu.Unlock()
		return
	}
	capture.preserved = true
	path, onPreserve := capture.path, capture.onPreserve
	capture.mu.Unlock()
	if path != "" && onPreserve != nil {
		onPreserve(path)
	}
}

// preserveFull stores the whole captured output in a tracked file and returns its path, so a
// filter that dropped lines always leaves the full text recoverable. A capture that already
// spilled to a spool file keeps that path.
func (capture *commandCapture) preserveFull(data []byte) string {
	capture.mu.Lock()
	if capture.path != "" {
		capture.preserved = true
		path, onPreserve := capture.path, capture.onPreserve
		capture.mu.Unlock()
		if onPreserve != nil {
			onPreserve(path)
		}
		return path
	}
	onPreserve := capture.onPreserve
	capture.mu.Unlock()

	file, path, err := tempfiles.Create("bash-", ".log")
	if err != nil {
		return ""
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != len(data) {
		_ = os.Remove(path)
		tempfiles.Untrack(path)
		return ""
	}
	capture.mu.Lock()
	if capture.path == "" {
		capture.path = path
		capture.preserved = true
	}
	capture.mu.Unlock()
	if onPreserve != nil {
		onPreserve(path)
	}
	return path
}

func (capture *commandCapture) savedPath() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.path
}

func (capture *commandCapture) snapshot(from int64) ([]byte, int64, int64, bool, bool, bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if from < 0 {
		from = 0
	}
	if from > capture.stored {
		from = capture.stored
	}
	length := capture.stored - from
	data := make([]byte, length)
	if length > 0 {
		if err := capture.readAtLocked(from, data); err != nil && !errors.Is(err, io.EOF) {
			capture.writeFailed = true
			data = nil
		}
	}
	return data, capture.total, capture.stored, capture.binary, capture.overflow, capture.writeFailed
}

func (capture *commandCapture) closeRemove() {
	capture.mu.Lock()
	if capture.closed {
		capture.mu.Unlock()
		return
	}
	capture.closed = true
	file, path, preserved := capture.file, capture.path, capture.preserved
	capture.file = nil
	capture.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if path != "" && !preserved {
		_ = os.Remove(path)
		tempfiles.Untrack(path)
	}
}

type commandTask struct {
	id            string
	name          string
	command       string
	cmd           *exec.Cmd
	capture       *commandCapture
	started       time.Time
	done          chan struct{}
	waitErr       error
	completed     time.Time
	mu            sync.Mutex
	delivered     int64
	collected     bool
	notified      bool
	waitObserved  bool
	manager       *taskManager
	orphansKilled bool
	cleanOnce     sync.Once
}

func (task *commandTask) isDone() bool {
	select {
	case <-task.done:
		return true
	default:
		return false
	}
}

func (task *commandTask) isCollected() bool {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.collected
}

func (task *commandTask) finish() {
	task.mu.Lock()
	task.completed = time.Now()
	manager := task.manager
	task.mu.Unlock()
	close(task.done)
	if manager != nil {
		select {
		case manager.events <- struct{}{}:
		default:
		}
	}
}

func (task *commandTask) cleanup() {
	task.cleanOnce.Do(func() { task.capture.closeRemove() })
}

type taskManager struct {
	mu         sync.Mutex
	tasks      map[string]*commandTask
	order      []*commandTask
	nextID     int
	maxRunning int
	closed     bool
	events     chan struct{}
}

func newTaskManager(maxRunning int) *taskManager {
	return &taskManager{tasks: make(map[string]*commandTask), maxRunning: maxRunning, nextID: 1, events: make(chan struct{}, 64)}
}

func (manager *taskManager) runningCount() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	count := 0
	for _, task := range manager.tasks {
		if !task.isDone() {
			count++
		}
	}
	return count
}

func (manager *taskManager) list() []TaskSnapshot {
	manager.mu.Lock()
	tasks := append([]*commandTask(nil), manager.order...)
	manager.mu.Unlock()

	snapshots := make([]TaskSnapshot, 0, len(tasks))
	for _, task := range tasks {
		if task.isCollected() {
			continue
		}
		running := !task.isDone()
		end := time.Now()
		var waitErr error
		if !running {
			task.mu.Lock()
			if !task.completed.IsZero() {
				end = task.completed
			}
			waitErr = task.waitErr
			task.mu.Unlock()
		}
		snapshot := TaskSnapshot{
			ID: task.id, Command: task.command, Running: running,
			Elapsed: end.Sub(task.started),
		}
		if exitError, ok := waitErr.(*exec.ExitError); ok {
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				snapshot.TermSignal = int(status.Signal())
			} else if exitCode := exitError.ExitCode(); exitCode >= 0 {
				snapshot.ExitCode = exitCode
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func (manager *taskManager) stop(ids []string, all bool, grace time.Duration) int {
	manager.mu.Lock()
	targets := make([]*commandTask, 0, len(manager.order))
	if all {
		for _, task := range manager.order {
			if !task.isCollected() {
				targets = append(targets, task)
			}
		}
	} else {
		seen := make(map[*commandTask]struct{}, len(ids))
		for _, id := range ids {
			if task := manager.tasks[id]; task != nil && !task.isCollected() {
				if _, duplicate := seen[task]; !duplicate {
					targets = append(targets, task)
					seen[task] = struct{}{}
				}
			}
		}
	}
	manager.mu.Unlock()

	running := make([]*commandTask, 0, len(targets))
	for _, task := range targets {
		if !task.isDone() {
			running = append(running, task)
		}
	}
	if len(running) == 0 {
		return 0
	}
	signal := syscall.SIGTERM
	if grace <= 0 {
		signal = syscall.SIGKILL
	}
	for _, task := range running {
		killTaskGroup(task, signal)
	}

	allDone := make(chan struct{})
	go func() {
		for _, task := range running {
			<-task.done
		}
		close(allDone)
	}()
	if grace > 0 {
		timer := time.NewTimer(grace)
		select {
		case <-allDone:
			timer.Stop()
			return len(running)
		case <-timer.C:
		}
	}
	for _, task := range running {
		if !task.isDone() {
			killTaskGroup(task, syscall.SIGKILL)
		}
	}
	<-allDone
	return len(running)
}

func (manager *taskManager) nameError(name string) string {
	if name == "" {
		return "'name' must not be empty"
	}
	if len(name) > 32 {
		return "'name' too long (max 32 characters)"
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return "'name' may contain only letters, digits, '-' and '_'"
		}
	}
	if len(name) > 1 && name[0] == 't' {
		allDigits := true
		for _, char := range name[1:] {
			if char < '0' || char > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return "'name' must not look like an automatic task id (t<number>)"
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if existing := manager.tasks[name]; existing != nil && !existing.isCollected() {
		return fmt.Sprintf("task name '%s' is already in use", name)
	}
	return ""
}

func (manager *taskManager) adopt(task *commandTask, name string) (string, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.runningCountLocked() >= manager.maxRunning {
		return "", false
	}
	id := name
	if id == "" {
		id = fmt.Sprintf("t%d", manager.nextID)
	}
	manager.nextID++
	if existing := manager.tasks[id]; existing != nil && !existing.isCollected() {
		return "", false
	}
	task.id = id
	task.name = name
	manager.tasks[id] = task
	manager.order = append(manager.order, task)
	return id, true
}

func (manager *taskManager) setMaxRunning(maxRunning int) {
	if manager == nil || maxRunning <= 0 {
		return
	}
	manager.mu.Lock()
	manager.maxRunning = maxRunning
	manager.mu.Unlock()
}

func (manager *taskManager) runningCountLocked() int {
	count := 0
	for _, task := range manager.tasks {
		if !task.isDone() {
			count++
		}
	}
	return count
}

func (manager *taskManager) find(id string) *commandTask {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.tasks[id]
}

func (manager *taskManager) collect(task *commandTask) {
	task.mu.Lock()
	task.collected = true
	task.mu.Unlock()
	task.cleanup()
}

func (manager *taskManager) Close() {
	manager.mu.Lock()
	manager.closed = true
	tasks := append([]*commandTask(nil), manager.order...)
	manager.mu.Unlock()
	for _, task := range tasks {
		if !task.isDone() {
			killTaskGroup(task, syscall.SIGTERM)
		}
	}
	for _, task := range tasks {
		if !task.isDone() {
			select {
			case <-task.done:
			case <-time.After(250 * time.Millisecond):
				killTaskGroup(task, syscall.SIGKILL)
				<-task.done
			}
		}
		manager.collect(task)
	}
}

func (registry *Registry) runBash(arguments string, _ int, display DisplaySink) Result {
	return registry.runBashContext(context.Background(), arguments, -1, display)
}

func (registry *Registry) runBashContext(ctx context.Context, arguments string, _ int, display DisplaySink) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	registry.tasks.setMaxRunning(registry.taskMaxRunning())
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	command, ok := object.string("command")
	if !ok || command == "" {
		return Result{Text: "missing 'command' argument"}
	}
	background, hasBackground, boolError := object.boolValue("background")
	if boolError != "" {
		return Result{Text: boolError}
	}
	if !hasBackground {
		background = false
	}
	if background && registry.noTasks() {
		return Result{Text: "background tasks are disabled; run the command synchronously"}
	}

	name := ""
	nameRaw, namePresent := object["name"]
	if namePresent && !registry.noTasks() && string(nameRaw) != "null" {
		name, ok = object.string("name")
		if !ok {
			return Result{Text: "'name' must be a string"}
		}
		if name != "" {
			if message := registry.tasks.nameError(name); message != "" {
				return Result{Text: message}
			}
			if maxRunning := registry.taskMaxRunning(); maxRunning > 0 && background && registry.tasks.runningCount() >= maxRunning {
				return Result{Text: fmt.Sprintf("too many running tasks (max %d): wait on or kill one first", maxRunning)}
			}
		}
	}
	if maxRunning := registry.taskMaxRunning(); maxRunning > 0 && background && registry.tasks.runningCount() >= maxRunning {
		return Result{Text: fmt.Sprintf("too many running tasks (max %d): wait on or kill one first", maxRunning)}
	}
	if ctx.Err() != nil {
		return Result{Text: "[interrupted]"}
	}

	timeout := registry.bashTimeout()
	if _, provided, err := object.integer("timeout_seconds"); provided {
		if err != "" {
			return Result{Text: err}
		}
		seconds, _, _ := object.integer("timeout_seconds")
		if int64(time.Duration(seconds)) > int64((time.Duration(1<<63-1))/time.Second) {
			timeout = time.Duration(1<<63 - 1)
		} else {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	if timeoutMax := registry.bashTimeoutMax(); timeoutMax > 0 && timeout > timeoutMax {
		timeout = timeoutMax
	}

	task, err := registry.startCommand(command, display)
	if err != nil {
		return Result{Text: "bash: " + err.Error()}
	}
	defer task.capture.setDisplay(nil, 0)
	window := timeout
	if background {
		window = registry.backgroundYield()
	}
	waitStop := registry.tasks.waitContext(ctx, task, window, background)
	if waitStop == waitCancelled {
		terminateTask(task, registry.bashTimeoutGrace())
		result := formatCommandResult(registry, task, registry.outputLimit(), "interrupted", window)
		displayBashOutcome(task, "interrupted", window)
		return Result{Text: result}
	}
	if waitStop == waitTargetDone {
		result := formatCommandResult(registry, task, registry.outputLimit(), "", 0)
		displayBashOutcome(task, "", 0)
		if background {
			footer := "\n[finished during launch; no task created]"
			if name != "" {
				footer = fmt.Sprintf("\n[finished during launch; task %s not created]", name)
			}
			result += footer
			task.capture.appendDisplayMessage(strings.TrimPrefix(footer, "\n"), true)
		}
		task.cleanup()
		return Result{Text: result}
	}

	if registry.noTasks() {
		terminateTask(task, registry.bashTimeoutGrace())
		result := formatCommandResult(registry, task, registry.outputLimit(), "timeout", window)
		displayBashOutcome(task, "timeout", window)
		return Result{Text: result}
	}
	if id, adopted := registry.tasks.adopt(task, name); adopted {
		task.capture.setTaskMode()
		_, total, stored, _, _, _ := task.capture.snapshot(0)
		task.mu.Lock()
		task.delivered = total
		task.mu.Unlock()
		initial, _, _, binary, overflow, writeFailed := task.capture.snapshot(0)
		body := ""
		if binary {
			body = fmt.Sprintf("[binary output suppressed: %s]", formatByteSize(total))
		} else if writeFailed {
			body = "[output unavailable: spool write failed]"
		} else {
			initial, compressed := registry.compressCommandOutput(task.capture, initial, command, commandExitCode(task))
			body = commandOutputPreview(task.capture, initial, registry.outputLimit(), compressed)
		}
		if overflow && total > stored {
			body += fmt.Sprintf("\n[output limit reached: %d further bytes discarded, task stopped]", total-stored)
		}
		footer := fmt.Sprintf("[detached as task %s]", id)
		if !background {
			footer = fmt.Sprintf("[detached as task %s after %s timeout]", id, formatDuration(window))
		}
		if body != "" {
			body = strings.TrimRight(body, "\n") + "\n"
		}
		task.capture.appendDisplayMessage(footer, true)
		return Result{Text: body + footer}
	}

	terminateTask(task, registry.bashTimeoutGrace())
	result := formatCommandResult(registry, task, registry.outputLimit(), "timeout", window)
	displayBashOutcome(task, "timeout", window)
	return Result{Text: result}
}

func (registry *Registry) startCommand(command string, display DisplaySink) (*commandTask, error) {
	shell, err := registry.resolveShell()
	if err != nil {
		return nil, err
	}
	capture, err := newCommandCapture(registry.outputLimit())
	if err != nil {
		return nil, fmt.Errorf("create output log: %w", err)
	}
	capture.onPreserve = registry.preserveOutputLog
	capture.setDisplay(display, 0)
	reader, writer, err := os.Pipe()
	if err != nil {
		capture.closeRemove()
		return nil, fmt.Errorf("create output pipe: %w", err)
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Dir = registry.options.WorkDir
	cmd.Env = registry.childEnvironment()
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = bashProcessAttributes()
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		capture.closeRemove()
		return nil, err
	}
	_ = writer.Close()
	task := &commandTask{command: command, cmd: cmd, capture: capture, started: time.Now(), done: make(chan struct{}), manager: registry.tasks}
	registry.trackChild(task)
	capture.setOverflowHandler(func() { killTaskGroup(task, syscall.SIGKILL) })
	go func() {
		waitResult := make(chan error, 1)
		go func() { waitResult <- cmd.Wait() }()
		pipeDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(capture, reader)
			_ = reader.Close()
			close(pipeDone)
		}()
		var waitErr error
		select {
		case waitErr = <-waitResult:
			select {
			case <-pipeDone:
			case <-time.After(200 * time.Millisecond):
				task.mu.Lock()
				task.orphansKilled = true
				task.mu.Unlock()
				killTaskGroup(task, syscall.SIGKILL)
				<-pipeDone
			}
			if killTaskGroup(task, syscall.SIGKILL) {
				task.mu.Lock()
				task.orphansKilled = true
				task.mu.Unlock()
			}
		case <-pipeDone:
			waitErr = <-waitResult
			if killTaskGroup(task, syscall.SIGKILL) {
				task.mu.Lock()
				task.orphansKilled = true
				task.mu.Unlock()
			}
		}
		task.mu.Lock()
		task.waitErr = waitErr
		task.mu.Unlock()
		registry.untrackChild(task)
		task.finish()
	}()
	return task, nil
}

var fixedChildEnvironment = []string{
	"PAGER=cat", "GIT_PAGER=cat", "MANPAGER=cat", "SYSTEMD_PAGER=cat", "GH_PAGER=cat",
	"GIT_EDITOR=false", "GIT_SEQUENCE_EDITOR=false", "VISUAL=false", "EDITOR=false",
	"TERM=dumb", "COLORTERM=", "GIT_TERMINAL_PROMPT=0", "AI_AGENT=gax",
	"PYTHONUNBUFFERED=1", "TQDM_DISABLE=1", "GAX_TRACE=", "GAX_TRANSCRIPT=",
}

func (registry *Registry) childEnvironment() []string {
	selection := childSelection{}
	if registry != nil {
		registry.selectionMu.RLock()
		selection = registry.selection
		registry.selectionMu.RUnlock()
	}
	return buildChildEnvironment(os.Environ(), os.Getenv("GAX_SUBAGENT_DEPTH"), selection)
}

func buildChildEnvironment(inherited []string, depthValue string, selection childSelection) []string {
	dynamic := make([]string, 0, 5)
	if selection.active {
		dynamic = append(dynamic,
			"GAX_PROVIDER="+selection.provider,
			"GAX_MODEL="+selection.model,
			"GAX_EFFORT="+selection.effort,
			"GAX_PRESET=",
		)
	}
	dynamic = append(dynamic, "GAX_SUBAGENT_DEPTH="+strconv.Itoa(subagent.NextChildDepth(depthValue)))

	overridden := make(map[string]struct{}, len(fixedChildEnvironment)+len(dynamic))
	for _, assignment := range fixedChildEnvironment {
		name, _, _ := strings.Cut(assignment, "=")
		overridden[name] = struct{}{}
	}
	for _, assignment := range dynamic {
		name, _, _ := strings.Cut(assignment, "=")
		overridden[name] = struct{}{}
	}

	environment := make([]string, 0, len(inherited)+len(fixedChildEnvironment)+len(dynamic))
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overridden[name]; !replaced {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, fixedChildEnvironment...)
	environment = append(environment, dynamic...)
	return environment
}

type taskWaitStop uint8

const (
	waitTargetDone taskWaitStop = iota
	waitOtherDone
	waitTimedOut
	waitCancelled
)

func (manager *taskManager) wait(task *commandTask, timeout time.Duration, immediateAtZero bool) taskWaitStop {
	return manager.waitContext(context.Background(), task, timeout, immediateAtZero)
}

func (manager *taskManager) waitContext(ctx context.Context, task *commandTask, timeout time.Duration,
	immediateAtZero bool) taskWaitStop {
	if ctx == nil {
		ctx = context.Background()
	}
	if task.isDone() {
		return waitTargetDone
	}
	if timeout <= 0 {
		if immediateAtZero {
			return waitTimedOut
		}
		select {
		case <-task.done:
			return waitTargetDone
		case <-ctx.Done():
			if task.isDone() {
				return waitTargetDone
			}
			return waitCancelled
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if task.isDone() {
			return waitTargetDone
		}
		if manager.takeOtherCompletion(task) {
			return waitOtherDone
		}
		select {
		case <-task.done:
			return waitTargetDone
		case <-manager.events:
			if task.isDone() {
				return waitTargetDone
			}
			if manager.takeOtherCompletion(task) {
				return waitOtherDone
			}
		case <-timer.C:
			if task.isDone() {
				return waitTargetDone
			}
			return waitTimedOut
		case <-ctx.Done():
			if task.isDone() {
				return waitTargetDone
			}
			return waitCancelled
		}
	}
}

func (manager *taskManager) takeOtherCompletion(target *commandTask) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, task := range manager.order {
		if task == target || !task.isDone() {
			continue
		}
		task.mu.Lock()
		if task.collected || task.notified || task.waitObserved {
			task.mu.Unlock()
			continue
		}
		task.waitObserved = true
		task.mu.Unlock()
		return true
	}
	return false
}

func (manager *taskManager) collectNotes() string {
	manager.mu.Lock()
	tasks := append([]*commandTask(nil), manager.order...)
	manager.mu.Unlock()

	var notes []string
	for _, task := range tasks {
		if !task.isDone() {
			continue
		}
		task.mu.Lock()
		if task.collected || task.notified {
			task.mu.Unlock()
			continue
		}
		from := task.delivered
		task.mu.Unlock()

		_, total, _, binary, _, _ := task.capture.snapshot(from)
		pending := total - from
		if pending < 0 {
			pending = 0
		}
		clause := "; "
		if total == 0 {
			clause += "no output; nothing to collect"
		} else if pending == 0 {
			clause += "no new output; nothing to collect"
		} else {
			clause += formatByteSize(pending)
			if binary {
				clause += " binary"
			}
			clause += " output pending"
		}
		notes = append(notes, "[task "+task.id+" "+taskStatus(task)+clause+"]")

		task.mu.Lock()
		task.notified = true
		collect := pending == 0
		task.mu.Unlock()
		if collect {
			manager.collect(task)
		}
	}
	return strings.Join(notes, "\n")
}

func (manager *taskManager) exitNotes() string {
	manager.mu.Lock()
	tasks := append([]*commandTask(nil), manager.order...)
	manager.mu.Unlock()

	var notes []string
	for _, task := range tasks {
		task.mu.Lock()
		collected := task.collected
		from := task.delivered
		task.mu.Unlock()
		if collected {
			continue
		}
		if !task.isDone() {
			notes = append(notes, "[task "+task.id+" killed at exit]")
			continue
		}
		_, total, _, _, _, _ := task.capture.snapshot(from)
		line := "[task " + task.id + " " + taskStatus(task)
		if pending := total - from; pending > 0 {
			line += "; " + formatByteSize(pending) + " output discarded"
		}
		notes = append(notes, line+"]")
	}
	return strings.Join(notes, "\n")
}

func terminateTask(task *commandTask, grace time.Duration) {
	if task.isDone() {
		return
	}
	killTaskGroup(task, syscall.SIGTERM)
	if grace > 0 {
		select {
		case <-task.done:
			return
		case <-time.After(grace):
		}
	}
	killTaskGroup(task, syscall.SIGKILL)
	select {
	case <-task.done:
	case <-time.After(3 * time.Second):
	}
}

func killTaskGroup(task *commandTask, signal syscall.Signal) bool {
	if task == nil || task.cmd == nil || task.cmd.Process == nil {
		return false
	}
	if err := syscall.Kill(-task.cmd.Process.Pid, signal); err == nil {
		return true
	} else if !errors.Is(err, syscall.ESRCH) {
		_ = task.cmd.Process.Kill()
	}
	return false
}

func (registry *Registry) runTaskWait(arguments string, _ int, display DisplaySink) Result {
	return registry.runTaskWaitContext(context.Background(), arguments, -1, display)
}

func (registry *Registry) runTaskWaitContext(ctx context.Context, arguments string, _ int, display DisplaySink) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if registry.noTasks() {
		return Result{Text: "background tasks are disabled"}
	}
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	id, ok := object.string("id")
	if !ok || id == "" {
		return Result{Text: "missing 'id': name the task to wait on, e.g. \"t1\""}
	}
	kill, hasKill, boolError := object.boolValue("kill")
	if boolError != "" {
		return Result{Text: boolError}
	}
	if !hasKill {
		kill = false
	}
	timeout := registry.taskWaitTimeout()
	if kill {
		timeout = 0
	}
	if raw, present := object["timeout_seconds"]; present {
		seconds, err := parseIntegerRaw(raw)
		if err != nil || seconds < 0 {
			return Result{Text: "'timeout_seconds' must be an integer >= 0"}
		}
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			timeout = time.Duration(1<<63 - 1)
		} else {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	task := registry.tasks.find(id)
	if task == nil {
		return Result{Text: fmt.Sprintf("no such task: %s", id)}
	}
	if display != nil {
		task.mu.Lock()
		from := task.delivered
		task.mu.Unlock()
		task.capture.setDisplay(display, from)
		defer task.capture.setDisplay(nil, 0)
	}
	waitStop := registry.tasks.waitContext(ctx, task, timeout, true)
	if waitStop == waitCancelled {
		terminateTask(task, registry.bashTimeoutGrace())
		result := collectTaskOutput(registry, task, registry.outputLimit())
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n"
		}
		footer := "[" + id + " interrupted]"
		task.capture.displayTaskWait(result, footer)
		return Result{Text: result + footer}
	}
	finished := waitStop == waitTargetDone
	killed := false
	if waitStop == waitOtherDone {
		task.mu.Lock()
		from := task.delivered
		task.mu.Unlock()
		_, total, stored, _, overflow, _ := task.capture.snapshot(from)
		settled := stored
		if overflow {
			settled = total
		}
		result := collectTaskOutput(registry, task, registry.outputLimit())
		footer := fmt.Sprintf("[%s %s", id, taskStatus(task))
		if result == "" {
			if total == 0 {
				footer += "; no output"
			} else if settled <= from {
				footer += "; no new output"
			}
		}
		if kill {
			footer += "; not killed"
		}
		footer += "; another task finished]"
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n"
		}
		task.capture.displayTaskWait(result, footer)
		return Result{Text: result + footer}
	}
	if waitStop == waitTimedOut && kill {
		killed = true
		terminateTask(task, registry.bashTimeoutGrace())
		finished = task.isDone()
	}
	if finished {
		task.mu.Lock()
		from := task.delivered
		task.mu.Unlock()
		_, total, stored, _, overflow, _ := task.capture.snapshot(from)
		settled := stored
		if overflow {
			settled = total
		}
		result := collectTaskOutput(registry, task, registry.outputLimit())
		footer := "[" + task.id + " " + taskStatus(task)
		if result == "" {
			if total == 0 {
				footer += "; no output"
			} else if settled <= from {
				footer += "; no new output"
			}
		}
		footer += "]"
		registry.tasks.collect(task)
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n"
		}
		task.capture.displayTaskWait(result, footer)
		return Result{Text: result + footer}
	}
	task.mu.Lock()
	from := task.delivered
	task.mu.Unlock()
	_, total, stored, _, overflow, _ := task.capture.snapshot(from)
	settled := stored
	if overflow {
		settled = total
	}
	result := collectTaskOutput(registry, task, registry.outputLimit())
	footer := fmt.Sprintf("[%s %s", id, taskStatus(task))
	if result == "" {
		if total == 0 {
			footer += "; no output"
		} else if settled <= from {
			footer += "; no new output"
		}
	}
	footer += "; wait timed out]"
	if killed {
		footer = fmt.Sprintf("[%s %s; did not exit after SIGKILL]", id, taskStatus(task))
	}
	if result != "" {
		result = strings.TrimRight(result, "\n") + "\n"
	}
	task.capture.displayTaskWait(result, footer)
	return Result{Text: result + footer}
}

// commandExitCode returns the command's exit status, or -1 when it is still running or did not
// exit normally. A running task's chunk is partial, so filters that depend on a complete,
// successful output leave it alone.
func commandExitCode(task *commandTask) int {
	if !task.isDone() {
		return -1
	}
	task.mu.Lock()
	err := task.waitErr
	task.mu.Unlock()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func collectTaskOutput(registry *Registry, task *commandTask, outputCap int) string {
	task.mu.Lock()
	from := task.delivered
	task.mu.Unlock()
	data, total, stored, binary, overflow, writeFailed := task.capture.snapshot(from)
	settled := stored
	if overflow {
		settled = total
	}
	task.mu.Lock()
	if settled > task.delivered {
		task.delivered = settled
	}
	task.mu.Unlock()
	if binary {
		if settled <= from {
			return ""
		}
		return fmt.Sprintf("[binary output suppressed: %s]", formatByteSize(total))
	}
	if writeFailed {
		if settled <= from {
			return ""
		}
		return "[output unavailable: spool write failed]"
	}
	data, compressed := registry.compressCommandOutput(task.capture, data, task.command, commandExitCode(task))
	result := taskOutputPreview(task.capture, data, outputCap, compressed)
	if overflow && total > stored {
		result += fmt.Sprintf("\n[output limit reached: %d further bytes discarded, task stopped]", total-stored)
	}
	return result
}

func formatCommandResult(registry *Registry, task *commandTask, outputCap int, stop string, timeout time.Duration) string {
	data, total, stored, binary, overflow, writeFailed := task.capture.snapshot(0)
	result := ""
	if binary {
		result = fmt.Sprintf("[binary output suppressed: %s]", formatByteSize(total))
	} else if writeFailed {
		result = "[output unavailable: spool write failed]"
	} else {
		data, compressed := registry.compressCommandOutput(task.capture, data, task.command, commandExitCode(task))
		result = commandOutputPreview(task.capture, data, outputCap, compressed)
	}
	if overflow && total > stored {
		result += fmt.Sprintf("\n[output limit reached: %d further bytes discarded, task stopped]", total-stored)
	}
	status := commandExitSuffix(task)
	switch stop {
	case "timeout":
		status = "timed out after " + formatDuration(timeout)
	case "interrupted":
		status = "interrupted"
	}
	if status != "" {
		if result != "" {
			result = strings.TrimRight(result, "\n") + "\n"
		}
		result += "[" + status + "]"
	} else if result == "" {
		result = "(no output)"
	}
	return result
}

func displayBashOutcome(task *commandTask, stop string, timeout time.Duration) {
	if task == nil || task.capture == nil {
		return
	}
	_, total, stored, binary, overflow, writeFailed := task.capture.snapshot(0)
	bodyDisplayed, _ := task.capture.displayState()
	var suffix strings.Builder
	if binary {
		if bodyDisplayed {
			suffix.WriteByte('\n')
		}
		suffix.WriteString(fmt.Sprintf("[binary output suppressed: %s]", formatByteSize(total)))
	} else if writeFailed {
		suffix.WriteString("[output unavailable: spool write failed]")
	}
	if overflow && total > stored {
		if suffix.Len() > 0 || bodyDisplayed {
			suffix.WriteByte('\n')
		}
		suffix.WriteString(fmt.Sprintf("[output limit reached: %d further bytes discarded, task stopped]", total-stored))
	}
	status := commandExitSuffix(task)
	if stop == "timeout" {
		status = "timed out after " + displayDuration(timeout)
	} else if stop == "interrupted" {
		status = "interrupted"
	}
	if status != "" {
		if suffix.Len() > 0 || bodyDisplayed {
			suffix.WriteByte('\n')
		} else {
			suffix.WriteByte('\n')
		}
		suffix.WriteByte('[')
		suffix.WriteString(status)
		suffix.WriteByte(']')
	} else if !bodyDisplayed && suffix.Len() == 0 {
		suffix.WriteString("(no output)")
	}
	if suffix.Len() > 0 {
		task.capture.appendDisplayMessage(suffix.String(), false)
	}
}

// compressCommandOutput replaces a recognized command's output with its compact form. The full
// text is stored and named when the filter dropped lines, so the compact form never hides output
// the model cannot get back.
func (registry *Registry) compressCommandOutput(capture *commandCapture, data []byte, command string, exitCode int) ([]byte, bool) {
	if registry == nil || !registry.bashCompress() || command == "" || len(data) == 0 {
		return data, false
	}
	compressed, ok := bashcompress.Compress(command, string(data), exitCode)
	if !ok {
		return data, false
	}
	if !compressed.Omitted {
		return []byte(compressed.Text), false
	}
	path := capture.preserveFull(data)
	if path == "" {
		// Without a recovery path the compact form would hide the rest of the output.
		return data, false
	}
	text := strings.TrimRight(compressed.Text, "\n") + "\n[full output saved to " + path + "]"
	return []byte(text), true
}

func commandOutputPreview(capture *commandCapture, data []byte, outputCap int, compressed bool) string {
	result, truncated := shapeCommandOutput(data, outputCap, capture.savedPath())
	if truncated && !compressed {
		capture.preserve()
		result, _ = shapeCommandOutput(data, outputCap, capture.savedPath())
	}
	return result
}

func taskOutputPreview(capture *commandCapture, data []byte, outputCap int, compressed bool) string {
	result, truncated := shapeTaskOutput(data, outputCap, capture.savedPath())
	if truncated && !compressed {
		capture.preserve()
		result, _ = shapeTaskOutput(data, outputCap, capture.savedPath())
	}
	return result
}

func shapeTaskOutput(data []byte, outputCap int, savedPath string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	if outputCap < 2 {
		return "[output truncated]", true
	}
	if savedPath == "" {
		return sanitizeUTF8(capLineLengths(string(data), maxLineWidth)), len(data) > outputCap || countOutputLines(data) > maxReadLines
	}
	headBudget := outputCap / 8
	tailBudget := outputCap - headBudget
	headLineLimit := maxReadLines / 8
	tailLineLimit := maxReadLines - headLineLimit
	tail, tailStart, _ := commandTailSlice(data, tailBudget, tailLineLimit)
	head := make([]byte, 0, headBudget)
	headLines := 0
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if len(head)+len(line) > headBudget || headLines >= headLineLimit || len(head)+len(line) > tailStart {
			break
		}
		head = append(head, line...)
		headLines++
	}
	gapBytes := tailStart - len(head)
	var result strings.Builder
	if headLines > 0 && gapBytes > 0 {
		result.WriteString(sanitizeUTF8(capLineLengths(string(head), maxLineWidth)))
		fmt.Fprintf(&result, "... [output truncated: omitted %s; full output: %s] ...\n", formatByteSize(int64(gapBytes)), savedPath)
		result.WriteString(sanitizeUTF8(capLineLengths(string(tail), maxLineWidth)))
		return result.String(), true
	}
	if len(tail) < len(data) {
		tail, _, _ = commandTailSlice(data, outputCap, maxReadLines)
		if len(tail) < len(data) {
			fmt.Fprintf(&result, "... [output truncated: omitted %s; full output: %s] ...\n", formatByteSize(int64(len(data)-len(tail))), savedPath)
		}
	}
	result.WriteString(sanitizeUTF8(capLineLengths(string(tail), maxLineWidth)))
	return result.String(), len(tail) < len(data)
}

func shapeCommandOutput(data []byte, outputCap int, savedPath string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	lineCount := countOutputLines(data)
	if len(data) <= outputCap && lineCount <= maxReadLines {
		return sanitizeUTF8(capLineLengths(string(data), maxLineWidth)), false
	}
	if outputCap < 2 || savedPath == "" {
		return "[output truncated]", true
	}
	headBudget := outputCap / 8
	tailBudget := outputCap - headBudget
	headLineLimit := maxReadLines / 8
	tailLineLimit := maxReadLines - headLineLimit

	tail, tailStart, tailLines := commandTailSlice(data, tailBudget, tailLineLimit)
	var head []byte
	var headLines int
	for _, line := range lines {
		if len(head)+len(line) > headBudget || headLines >= headLineLimit || len(head)+len(line) > tailStart {
			break
		}
		head = append(head, line...)
		headLines++
	}
	var result strings.Builder
	if headLines > 0 && tailStart > len(head) {
		result.WriteString(sanitizeUTF8(capLineLengths(string(head), maxLineWidth)))
		omittedLines := lineCount - headLines - tailLines
		if omittedLines < 0 {
			omittedLines = 0
		}
		gapBytes := tailStart - len(head)
		if omittedLines > 0 {
			fmt.Fprintf(&result, "... [output truncated: omitted %d of %d lines (kept first %d, last %d); full output saved to %s] ...\n", omittedLines, lineCount, headLines, tailLines, savedPath)
		} else {
			fmt.Fprintf(&result, "... [output truncated: omitted %s mid-line; full output saved to %s] ...\n", formatByteSize(int64(gapBytes)), savedPath)
		}
		result.WriteString(sanitizeUTF8(capLineLengths(string(tail), maxLineWidth)))
		return result.String(), true
	}

	// If the head has no complete line before the tail, match the upstream tail-only fallback and
	// spend the full byte and line budget on the ending of the command output.
	tail, tailStart, tailLines = commandTailSlice(data, outputCap, maxReadLines)
	keptSize := formatByteSize(int64(len(tail)))
	totalSize := formatByteSize(int64(len(data)))
	result.WriteString(sanitizeUTF8(capLineLengths(string(tail), maxLineWidth)))
	fmt.Fprintf(&result, "\n[output truncated: last %d of %d lines, %s of %s; full output saved to %s]", tailLines, lineCount, keptSize, totalSize, savedPath)
	return result.String(), true
}

func countOutputLines(data []byte) int {
	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}

func commandTailSlice(data []byte, byteLimit, lineLimit int) ([]byte, int, int) {
	if byteLimit <= 0 || len(data) == 0 {
		return nil, len(data), 0
	}
	start := len(data) - byteLimit
	if start < 0 {
		start = 0
	}
	if start > 0 && data[start-1] != '\n' {
		if newline := bytes.IndexByte(data[start:], '\n'); newline >= 0 && start+newline+1 < len(data) {
			start += newline + 1
		}
	}
	tail := data[start:]
	if lineLimit > 0 && countOutputLines(tail) > lineLimit {
		skip := countOutputLines(tail) - lineLimit
		for index, value := range tail {
			if value == '\n' {
				skip--
				if skip == 0 {
					start += index + 1
					tail = data[start:]
					break
				}
			}
		}
	}
	return tail, start, countOutputLines(tail)
}

func commandStatus(task *commandTask) string {
	task.mu.Lock()
	err := task.waitErr
	task.mu.Unlock()
	if err == nil {
		return "finished (exit 0)"
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Sprintf("killed (signal %d)", status.Signal())
		}
		return fmt.Sprintf("finished (exit %d)", exitError.ExitCode())
	}
	return "finished (exit -1)"
}

func commandExitSuffix(task *commandTask) string {
	task.mu.Lock()
	err := task.waitErr
	task.mu.Unlock()
	if err == nil {
		return ""
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Sprintf("signal %d", status.Signal())
		}
		return fmt.Sprintf("exit %d", exitError.ExitCode())
	}
	return "exit -1"
}

func taskStatus(task *commandTask) string {
	status := commandStatus(task)
	if !task.isDone() {
		status = "still running"
	}
	end := time.Now()
	if task.isDone() {
		task.mu.Lock()
		if !task.completed.IsZero() {
			end = task.completed
		}
		task.mu.Unlock()
	}
	phrase := fmt.Sprintf("%s after %s", status, formatDuration(end.Sub(task.started)))
	task.mu.Lock()
	orphansKilled := task.orphansKilled
	task.mu.Unlock()
	if orphansKilled {
		phrase += "; orphaned processes killed"
	}
	return phrase
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	minutes := int(duration / time.Minute)
	seconds := int(duration/time.Second) % 60
	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func formatByteSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	if size < 10*1024 {
		return fmt.Sprintf("%.1fK", float64(size)/1024)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%dK", (size+512)/1024)
	}
	if size < 10*1024*1024 {
		return fmt.Sprintf("%.1fM", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%dM", (size+512*1024)/(1024*1024))
}

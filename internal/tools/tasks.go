package tools

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
)

type bgTask struct {
	id      string
	command string
	pg      platform.ProcessGroup
	done    chan struct{}

	mu       sync.Mutex
	buf      *ringBuffer
	err      error
	finished bool
	reported bool
	raw      bool
}

type taskManager struct {
	mu         sync.Mutex
	maxRunning int
	seq        int
	tasks      map[string]*bgTask
	order      []string
}

func newTaskManager(maxRunning int) *taskManager {
	if maxRunning <= 0 {
		maxRunning = 32
	}
	if maxRunning > 64 {
		maxRunning = 64
	}
	return &taskManager{
		maxRunning: maxRunning,
		tasks:      make(map[string]*bgTask),
	}
}

func (m *taskManager) nextID(preferred string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	running := 0
	for _, t := range m.tasks {
		t.mu.Lock()
		if !t.finished {
			running++
		}
		t.mu.Unlock()
	}
	if running >= m.maxRunning {
		return "", fmt.Errorf("too many running background tasks (%d/%d)", running, m.maxRunning)
	}
	if preferred != "" {
		if _, exists := m.tasks[preferred]; !exists {
			return preferred, nil
		}
	}
	for {
		m.seq++
		id := fmt.Sprintf("t%d", m.seq)
		if _, exists := m.tasks[id]; !exists {
			return id, nil
		}
	}
}

func (m *taskManager) add(t *bgTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.id] = t
	m.order = append(m.order, t.id)
}

func (m *taskManager) get(id string) (*bgTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	return t, ok
}

// CollectNotes returns a summary of newly finished background tasks.
func (m *taskManager) CollectNotes() string {
	m.mu.Lock()
	ids := append([]string(nil), m.order...)
	m.mu.Unlock()

	var notes []string
	for _, id := range ids {
		t, ok := m.get(id)
		if !ok {
			continue
		}
		t.mu.Lock()
		if t.finished && !t.reported {
			t.reported = true
			status := "exited 0"
			if t.err != nil {
				status = t.err.Error()
			}
			notes = append(notes, fmt.Sprintf("[background task %s (%s) finished: %s — call task_wait to collect output]", t.id, t.command, status))
		}
		t.mu.Unlock()
	}
	return strings.Join(notes, "\n")
}

func (m *taskManager) stopAll() {
	m.mu.Lock()
	tasks := make([]*bgTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.Unlock()

	for _, t := range tasks {
		_ = t.pg.KillTree()
		<-t.done
		_ = t.pg.Close()
	}
}

// CollectTaskNotes returns completion notifications for newly finished tasks.
func (r *Registry) CollectTaskNotes() string {
	if r == nil || r.tasks == nil {
		return ""
	}
	return r.tasks.CollectNotes()
}

type taskWaitTool struct{ reg *Registry }

func (t *taskWaitTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "task_wait",
		Description: "Wait for a background bash task to finish and collect its output, or stop it with kill=true.",
		Params: []core.ToolParam{
			{Name: "id", Type: "string", Required: true, Description: "Task id to wait on (e.g. \"t1\")."},
			{Name: "timeout_seconds", Type: "integer", Description: "How long to wait; 0 returns immediately. Defaults to task.wait_timeout (or 0 when kill=true)."},
			{Name: "kill", Type: "boolean", Description: "Stop the task and return its final output."},
		},
	}
}

func (t *taskWaitTool) Run(ctx context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		ID             string `json:"id"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
		Kill           bool   `json:"kill"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	task, ok := t.reg.tasks.get(args.ID)
	if !ok {
		return Result{Text: fmt.Sprintf("error: no such task %q", args.ID), IsError: true}
	}

	waitDur := t.reg.store.Duration("task.wait_timeout")
	if args.Kill && args.TimeoutSeconds == nil {
		waitDur = 0
	} else if args.TimeoutSeconds != nil {
		waitDur = time.Duration(max(0, *args.TimeoutSeconds)) * time.Second
	}

	if waitDur > 0 {
		timer := time.NewTimer(waitDur)
		select {
		case <-task.done:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return Result{Text: fmt.Sprintf("interrupted while waiting for task %s", args.ID), IsError: true}
		}
		timer.Stop()
	}

	if args.Kill {
		select {
		case <-task.done:
		default:
			_ = task.pg.SignalGraceful()
			grace := t.reg.store.Duration("bash.timeout_grace")
			if grace <= 0 {
				grace = 2 * time.Second
			}
			timer := time.NewTimer(grace)
			select {
			case <-task.done:
			case <-timer.C:
				_ = task.pg.KillTree()
				<-task.done
			}
			timer.Stop()
		}
	}

	task.mu.Lock()
	finished := task.finished
	taskErr := task.err
	out := task.buf.String()
	if finished {
		task.reported = true
	}
	task.mu.Unlock()

	if !finished {
		return Result{Text: fmt.Sprintf("task %s is still running\n%s", args.ID, capOutput(out, t.reg.outputCap(), "gax-task"))}
	}
	status := "exit 0"
	if taskErr != nil {
		status = taskErr.Error()
	}
	return Result{
		Text:    fmt.Sprintf("task %s finished (%s)\n%s", args.ID, status, capOutput(out, t.reg.outputCap(), "gax-task")),
		IsError: taskErr != nil,
	}
}

// ringBuffer keeps at most maxBytes of the most recent output, bounded in memory.
type ringBuffer struct {
	mu       sync.Mutex
	maxBytes int
	buf      bytes.Buffer
}

func newRingBuffer(maxBytes int) *ringBuffer {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	return &ringBuffer{maxBytes: maxBytes}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= r.maxBytes {
		r.buf.Reset()
		r.buf.Write(p[len(p)-r.maxBytes:])
		return len(p), nil
	}
	if r.buf.Len()+len(p) > r.maxBytes {
		excess := r.buf.Len() + len(p) - r.maxBytes
		data := r.buf.Bytes()
		r.buf.Reset()
		r.buf.Write(data[excess:])
	}
	return r.buf.Write(p)
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

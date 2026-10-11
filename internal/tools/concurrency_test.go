package tools

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luank3/gax/internal/conversation"
)

// TestRegistryConcurrentBashAndTaskCollection exercises the task manager, the command capture
// buffers, and the active-child map at once. The registry is shared across concurrent tool calls
// in the REPL, so every one of these paths must be race-free.
func TestRegistryConcurrentBashAndTaskCollection(t *testing.T) {
	registry := New(Options{UseExactValues: true, BashTimeout: 10 * time.Second, TaskMaxRunning: 64})
	defer registry.Close()

	const workers = 8
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 4; iteration++ {
				call := conversation.Item{
					Kind: conversation.KindToolCall, CallID: conversation.String("c"),
					ToolName: conversation.String("bash"),
					Arguments: conversation.String(
						`{"command":"printf 'worker output\n'","timeout_seconds":5}`),
				}
				result := registry.Execute(call, -1)
				if result.Output == nil || !strings.Contains(*result.Output, "worker output") {
					t.Errorf("concurrent bash output = %v", result.Output)
					return
				}
			}
		}()
	}
	// Collect notes, list tasks, and query the working directory while tools run.
	wait.Add(1)
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			registry.CollectTaskNotes()
			registry.ListTasks()
			registry.StopTasks([]string{"missing"}, false)
			registry.bashWorkingDirectory()
		}
	}()
	wait.Wait()

	if snapshot := registry.ListTasks(); len(snapshot) != 0 {
		t.Fatalf("synchronous commands should leave no uncollected tasks: %+v", snapshot)
	}
}

// TestRegistryConcurrentBackgroundTasksAndStop covers the detach path: tasks that outlive their
// call are registered, collected, and signalled from other goroutines.
func TestRegistryConcurrentBackgroundTasksAndStop(t *testing.T) {
	registry := New(Options{
		UseExactValues: true, BashTimeout: 10 * time.Second, BashTimeoutGrace: 200 * time.Millisecond,
		BackgroundYield: 150 * time.Millisecond, TaskWaitTimeout: 5 * time.Second, TaskMaxRunning: 32,
	})
	defer registry.Close()

	const workers = 6
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			call := conversation.Item{
				Kind: conversation.KindToolCall, CallID: conversation.String("c"),
				ToolName:  conversation.String("bash"),
				Arguments: conversation.String(`{"command":"sleep 30","background":true}`),
			}
			_ = registry.Execute(call, -1)
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for index := 0; index < 20; index++ {
			registry.StopTasks(nil, true)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	wait.Wait()
	registry.StopTasks(nil, true)

	// A stopped task stays listable until task_wait collects its output, so the invariant is that
	// every task has been signalled rather than that the registry is empty.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running := 0
		for _, snapshot := range registry.ListTasks() {
			if snapshot.Running {
				running++
			}
		}
		if running == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stopped tasks still report as running: %+v", registry.ListTasks())
}

// TestRegistryConcurrentDisplaySinksAndOutputCaps runs the display sink and the output cap
// together, which is how the live renderer consumes a streaming command.
func TestRegistryConcurrentDisplaySinksAndOutputCaps(t *testing.T) {
	registry := New(Options{UseExactValues: true, BashTimeout: 10 * time.Second})
	defer registry.Close()

	var wait sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var mu sync.Mutex
			var displayed int
			call := conversation.Item{
				Kind: conversation.KindToolCall, CallID: conversation.String("c"),
				ToolName: conversation.String("bash"),
				Arguments: conversation.String(
					`{"command":"for i in 1 2 3 4 5; do echo line-$i; done","timeout_seconds":5}`),
			}
			execution := registry.ExecuteWithDisplay(call, -1, func(data []byte) error {
				mu.Lock()
				displayed += len(data)
				mu.Unlock()
				return nil
			})
			if execution.Result.Output == nil || !strings.Contains(*execution.Result.Output, "line-5") {
				t.Errorf("canonical result lost output: %v", execution.Result.Output)
			}
		}()
	}
	wait.Wait()
}

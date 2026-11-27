package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/bashcompress"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/subagent"
)

type bashTool struct{ reg *Registry }

func (t *bashTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "bash",
		Description: "Execute a shell command in the workspace. Long-running commands detach into a background task unless no_tasks is enabled.",
		Params: []core.ToolParam{
			{Name: "command", Type: "string", Required: true, Description: "Shell command to run."},
			{Name: "timeout_seconds", Type: "integer", Minimum: intPtr(1), Description: "Optional timeout override in seconds."},
			{Name: "background", Type: "boolean", Description: "Run in background after a brief initial output window."},
			{Name: "name", Type: "string", Description: "Optional short task name if detached."},
			{Name: "raw", Type: "boolean", Description: "Return output verbatim without compression."},
		},
	}
}

func (t *bashTool) Run(ctx context.Context, arguments string, sink OutputSink) Result {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Background     bool   `json:"background"`
		Name           string `json:"name"`
		Raw            bool   `json:"raw"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{Text: "error: 'command' is required", IsError: true}
	}

	shellSpec, err := platform.ResolveShell(t.reg.store.String("bash.shell"))
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err), IsError: true}
	}

	cmdArgs := append(append([]string{}, shellSpec.Command[1:]...), args.Command)
	cmd := exec.Command(shellSpec.Command[0], cmdArgs...)
	if t.reg.cwd != "" {
		cmd.Dir = t.reg.cwd
	}
	overrides := map[string]string{}
	if t.reg.store != nil {
		if p := t.reg.store.String("provider"); p != "" && p != "auto" {
			overrides["GAX_PROVIDER"] = p
		}
		if m := t.reg.store.String("model"); m != "" {
			overrides["GAX_MODEL"] = m
		}
		if e := t.reg.store.String("effort"); e != "" {
			overrides["GAX_EFFORT"] = e
		}
		if t.reg.store.Bool("no_web") {
			overrides["GAX_NO_WEB"] = "on"
		}
		if t.reg.store.Bool("no_skills") {
			overrides["GAX_NO_SKILLS"] = "on"
		}
		if t.reg.store.Bool("no_tasks") {
			overrides["GAX_NO_TASKS"] = "on"
		}
		if t.reg.store.Bool("no_subagents") {
			overrides["GAX_NO_SUBAGENTS"] = "on"
		}
		if ns := t.reg.store.String("no_session"); ns != "" && ns != "auto" {
			overrides["GAX_NO_SESSION"] = ns
		}
	}
	cmd.Env = subagent.ChildEnv(os.Environ(), os.Getenv("GAX_SUBAGENT_DEPTH"), overrides)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{Text: fmt.Sprintf("error: stdout pipe: %v", err), IsError: true}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{Text: fmt.Sprintf("error: stderr pipe: %v", err), IsError: true}
	}

	pg, err := platform.StartCommand(cmd)
	if err != nil {
		return Result{Text: fmt.Sprintf("error starting command: %v", err), IsError: true}
	}

	ring := newRingBuffer(max(256*1024, t.reg.outputCap()*4))
	var ioWG sync.WaitGroup
	pump := func(stream string, r io.Reader) {
		defer ioWG.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				_, _ = ring.Write(buf[:n])
				if sink != nil {
					sink(stream, string(buf[:n]))
				}
			}
			if rerr != nil {
				return
			}
		}
	}
	ioWG.Add(2)
	go pump("stdout", stdoutPipe)
	go pump("stderr", stderrPipe)

	done := make(chan struct{})
	var waitErr error
	var taskRef *bgTask

	go func() {
		ioWG.Wait()
		waitErr = pg.Wait()
		if taskRef != nil {
			taskRef.mu.Lock()
			taskRef.err = waitErr
			taskRef.finished = true
			taskRef.mu.Unlock()
			_ = pg.Close()
		}
		close(done)
	}()

	timeout := t.reg.store.Duration("bash.timeout")
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
		if maxT := t.reg.store.Duration("bash.timeout_max"); maxT > 0 && timeout > maxT {
			timeout = maxT
		}
	}
	if args.Background && !t.reg.store.Bool("no_tasks") {
		timeout = t.reg.store.Duration("bash.background_yield")
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
	}

	var timer *time.Timer
	var timerCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerCh = timer.C
		defer timer.Stop()
	}

	select {
	case <-done:
		_ = pg.Close()
		rawOut := ring.String()
		out := formatBashOutput(args.Command, rawOut, waitErr, args.Raw, t.reg)
		if waitErr != nil {
			return Result{Text: fmt.Sprintf("exit error: %v\n%s", waitErr, out), IsError: true}
		}
		return Result{Text: out}

	case <-ctx.Done():
		_ = pg.SignalGraceful()
		grace := t.reg.store.Duration("bash.timeout_grace")
		if grace <= 0 {
			grace = 2 * time.Second
		}
		gTimer := time.NewTimer(grace)
		select {
		case <-done:
		case <-gTimer.C:
			_ = pg.KillTree()
			<-done
		}
		gTimer.Stop()
		_ = pg.Close()
		return Result{Text: fmt.Sprintf("command interrupted\n%s", capOutput(ring.String(), t.reg.outputCap(), "gax-bash")), IsError: true}

	case <-timerCh:
		if t.reg.store.Bool("no_tasks") {
			_ = pg.SignalGraceful()
			_ = pg.KillTree()
			<-done
			_ = pg.Close()
			return Result{Text: fmt.Sprintf("command timed out after %s\n%s", timeout, capOutput(ring.String(), t.reg.outputCap(), "gax-bash")), IsError: true}
		}
		id, idErr := t.reg.tasks.nextID(args.Name)
		if idErr != nil {
			_ = pg.KillTree()
			<-done
			_ = pg.Close()
			return Result{Text: fmt.Sprintf("error detaching task: %v", idErr), IsError: true}
		}
		taskRef = &bgTask{
			id:      id,
			command: args.Command,
			pg:      pg,
			done:    done,
			buf:     ring,
			raw:     args.Raw,
		}
		t.reg.tasks.add(taskRef)
		return Result{
			Text: fmt.Sprintf("command detached as background task %s after %s (use task_wait to collect output)\n%s",
				id, timeout, capOutput(ring.String(), t.reg.outputCap(), "gax-bash")),
		}
	}
}

func formatBashOutput(command, rawOut string, waitErr error, rawFlag bool, reg *Registry) string {
	if !rawFlag && reg.store.Bool("bash.compress") {
		exitCode := 0
		if waitErr != nil {
			exitCode = 1
		}
		if res, ok := bashcompress.Compress(command, rawOut, exitCode); ok {
			if res.Omitted {
				if f, err := os.CreateTemp("", "gax-bash-full-*.log"); err == nil {
					_, _ = f.WriteString(rawOut)
					fullPath := f.Name()
					_ = f.Close()
					return res.Text + fmt.Sprintf("\n[full output saved to %s; pass raw: true to bypass compression]", fullPath)
				}
			}
			return res.Text
		}
	}
	return capOutput(rawOut, reg.outputCap(), "gax-bash")
}

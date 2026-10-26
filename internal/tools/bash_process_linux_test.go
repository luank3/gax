//go:build linux

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBashProcessStartsInItsOwnSession(t *testing.T) {
	registry := New(Options{})
	defer registry.Close()

	task, err := registry.startCommand("exec sleep 30", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	pid := task.cmd.Process.Pid
	session, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	if errno != 0 {
		t.Fatalf("getsid(%d): %v", pid, errno)
	}
	if int(session) != pid {
		t.Fatalf("Bash process session ID = %d, want its own PID %d", session, pid)
	}
}

func TestBashProcessDiesWhenItsParentIsKilled(t *testing.T) {
	const markerEnv = "GAX_BASH_PARENT_DEATH_MARKER"
	if marker := os.Getenv(markerEnv); marker != "" {
		registry := New(Options{})
		task, err := registry.startCommand("exec sleep 30", nil, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		contents := fmt.Sprintf("%d\n%s\n", task.cmd.Process.Pid, task.capture.path)
		if err := os.WriteFile(marker, []byte(contents), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		select {}
	}

	marker := filepath.Join(t.TempDir(), "bash-child")
	parent := exec.Command(os.Args[0], "-test.run=^TestBashProcessDiesWhenItsParentIsKilled$")
	parent.Env = append(os.Environ(), markerEnv+"="+marker)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if parent.ProcessState == nil {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	}()

	var fields []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			fields = strings.Fields(string(data))
			if len(fields) >= 1 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(fields) < 1 {
		t.Fatal("helper did not report its Bash child")
	}
	childPID, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("invalid child PID %q: %v", fields[0], err)
	}
	if len(fields) > 1 {
		defer os.Remove(fields[1])
	}

	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill helper parent: %v", err)
	}
	_ = parent.Wait()
	exited := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if linuxProcessExited(childPID) {
			exited = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !exited {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("Bash child %d survived its parent", childPID)
	}
}

func linuxProcessExited(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH)
	}
	closing := strings.LastIndex(string(data), ") ")
	if closing < 0 {
		return false
	}
	fields := strings.Fields(string(data[closing+2:]))
	if len(fields) == 0 {
		return false
	}
	state := fields[0]
	return state == "Z" || state == "X" || state == "x"
}

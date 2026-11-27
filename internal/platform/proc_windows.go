//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ntdll            = syscall.NewLazyDLL("ntdll.dll")
	procNtResumeProc = ntdll.NewProc("NtResumeProcess")
)

type windowsProcessGroup struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	job  windows.Handle
	root windows.Handle
	done bool
}

func startCommand(cmd *exec.Cmd) (ProcessGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}

	access := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_SUSPEND_RESUME | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	root, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err == nil {
		if err = windows.AssignProcessToJobObject(job, root); err == nil {
			status, _, _ := procNtResumeProc.Call(uintptr(root))
			if status == 0 {
				return &windowsProcessGroup{cmd: cmd, job: job, root: root}, nil
			}
			err = fmt.Errorf("resume process: NTSTATUS 0x%x", status)
		}
		_ = windows.CloseHandle(root)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = windows.CloseHandle(job)
	return nil, fmt.Errorf("assign process to job: %w", err)
}

func (g *windowsProcessGroup) Wait() error {
	err := g.cmd.Wait()
	g.mu.Lock()
	g.done = true
	g.mu.Unlock()
	return err
}

func (g *windowsProcessGroup) SignalGraceful() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.cmd.Process == nil {
		return nil
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(g.cmd.Process.Pid)); err == nil {
		return nil
	}
	if g.job != 0 {
		return windows.TerminateJobObject(g.job, 1)
	}
	return nil
}

func (g *windowsProcessGroup) KillTree() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(g.job, 1)
}

func (g *windowsProcessGroup) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.root != 0 {
		_ = windows.CloseHandle(g.root)
		g.root = 0
	}
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
	return nil
}

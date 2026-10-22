//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var (
	processKernel32      = syscall.NewLazyDLL("kernel32.dll")
	createJobObject      = processKernel32.NewProc("CreateJobObjectW")
	setJobInformation    = processKernel32.NewProc("SetInformationJobObject")
	queryJobInformation  = processKernel32.NewProc("QueryInformationJobObject")
	assignJobProcess     = processKernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject   = processKernel32.NewProc("TerminateJobObject")
	generateConsoleEvent = processKernel32.NewProc("GenerateConsoleCtrlEvent")
	resumeProcess        = syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess")
)

type jobBasicLimits struct {
	ProcessTime, JobTime                 int64
	Flags                                uint32
	MinimumWorkingSet, MaximumWorkingSet uintptr
	ActiveProcessLimit                   uint32
	Affinity                             uintptr
	PriorityClass, SchedulingClass       uint32
}
type jobExtendedLimits struct {
	Basic                                                      jobBasicLimits
	IO                                                         [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}
type jobAccounting struct {
	UserTime, KernelTime, PeriodUserTime, PeriodKernelTime           int64
	PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

type commandProcess struct {
	mu   sync.Mutex
	job  syscall.Handle
	root syscall.Handle
	term syscall.Signal
}

func startCommandProcess(cmd *exec.Cmd) (*commandProcess, error) {
	job, _, err := createJobObject.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("create process job: %w", err)
	}
	process := &commandProcess{job: syscall.Handle(job)}
	limits := jobExtendedLimits{Basic: jobBasicLimits{Flags: 0x2000}} // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	result, _, err := setJobInformation.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if result == 0 {
		process.close()
		return nil, fmt.Errorf("configure process job: %w", err)
	}
	// Assign before any user code runs, otherwise a fast shell can spawn children outside the job.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x4} // CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		process.close()
		return nil, err
	}
	handle, err := syscall.OpenProcess(0x0100|0x0001|0x0800|0x1000, false, uint32(cmd.Process.Pid))
	if err == nil {
		result, _, err = assignJobProcess.Call(job, uintptr(handle))
		if result != 0 {
			// os/exec closes the initial thread handle, so resume the process after job assignment.
			status, _, _ := resumeProcess.Call(uintptr(handle))
			if status == 0 {
				process.root = handle
				return process, nil
			}
			err = fmt.Errorf("resume command: NTSTATUS 0x%x", status)
		}
		syscall.CloseHandle(handle)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	process.close()
	return nil, fmt.Errorf("assign command to process job: %w", err)
}

func (process *commandProcess) close() {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.root != 0 {
		syscall.CloseHandle(process.root)
		process.root = 0
	}
	if process.job != 0 {
		syscall.CloseHandle(process.job)
		process.job = 0
	}
}

func (process *commandProcess) kill(cmd *exec.Cmd, signal syscall.Signal) bool {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.job == 0 {
		return false
	}
	var accounting jobAccounting
	result, _, _ := queryJobInformation.Call(uintptr(process.job), 1, uintptr(unsafe.Pointer(&accounting)), unsafe.Sizeof(accounting), 0)
	if result != 0 && accounting.ActiveProcesses == 0 {
		return false
	}
	var code uint32
	rootRunning := process.root != 0 && syscall.GetExitCodeProcess(process.root, &code) == nil && code == 259
	if signal != syscall.SIGKILL {
		result, _, _ = generateConsoleEvent.Call(1, uintptr(cmd.Process.Pid)) // CTRL_BREAK_EVENT
		if result != 0 {
			if rootRunning {
				process.term = signal
			}
			return true
		}
	}
	result, _, _ = terminateJobObject.Call(uintptr(process.job), 1)
	if result != 0 && rootRunning {
		process.term = signal
	}
	return result != 0
}

func commandSignal(task *commandTask, _ *exec.ExitError) syscall.Signal {
	if task.process == nil {
		return 0
	}
	task.process.mu.Lock()
	defer task.process.mu.Unlock()
	return task.process.term
}

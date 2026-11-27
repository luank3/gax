//go:build windows

package platform

import "syscall"

var (
	kernel32Power               = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32Power.NewProc("SetThreadExecutionState")
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

type winKeepAwake struct{}

func inhibitSleep(_ string) KeepAwake {
	_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
	return &winKeepAwake{}
}

func (w *winKeepAwake) Stop() {
	if w != nil {
		_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous))
	}
}

//go:build linux

package input

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// tcflushRequest is the Linux TCFLSH ioctl request. syscall exposes it only on some
// architectures, although the request number is part of the Linux tty ioctl ABI.
const tcflushRequest = 0x540B

// InterruptWatch owns stdin in noncanonical, no-echo mode for the duration of a running turn.
// Its state is polled by the agent loop; a second bare Escape invokes onAbort immediately.
type InterruptWatch struct {
	fd      int
	saved   syscall.Termios
	stop    chan struct{}
	resolve chan struct{}
	done    chan struct{}
	stopped atomic.Bool
	pause   atomic.Bool
	abort   atomic.Bool
	onPause func()
	onAbort func()
}

// StartInterruptWatch arms Escape handling while a provider turn or tool is running. ISIG remains
// enabled, so Ctrl-C continues through the process signal handler.
func StartInterruptWatch(stdin *os.File, onPause, onAbort func()) (*InterruptWatch, error) {
	if stdin == nil {
		return nil, errors.New("terminal input is unavailable")
	}
	fd := int(stdin.Fd())
	saved, err := getTermios(fd)
	if err != nil {
		return nil, err
	}
	raw := saved
	raw.Lflag &^= syscall.ECHO | syscall.ICANON
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 0
	if err := setTermios(fd, &raw); err != nil {
		return nil, err
	}
	watch := &InterruptWatch{fd: fd, saved: saved, stop: make(chan struct{}), resolve: make(chan struct{}), done: make(chan struct{}),
		onPause: onPause, onAbort: onAbort}
	go watch.run()
	return watch, nil
}

func (watch *InterruptWatch) PauseRequested() bool { return watch != nil && watch.pause.Load() }
func (watch *InterruptWatch) AbortRequested() bool { return watch != nil && watch.abort.Load() }

// Stop releases stdin, restores its exact terminal mode, and discards bytes queued during the
// run so they cannot become an accidental prompt.
func (watch *InterruptWatch) Stop() {
	watch.stopWatch(false)
}

// StopResolvePending confirms a bare Escape already read by the watcher before restoring stdin.
// This lets short operations preserve an Escape that arrived as the provider request completed.
func (watch *InterruptWatch) StopResolvePending() {
	watch.stopWatch(true)
}

func (watch *InterruptWatch) stopWatch(resolvePending bool) {
	if watch == nil || !watch.stopped.CompareAndSwap(false, true) {
		return
	}
	if resolvePending {
		close(watch.resolve)
	} else {
		close(watch.stop)
	}
	<-watch.done
	_ = setTermios(watch.fd, &watch.saved)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(watch.fd), uintptr(tcflushRequest), uintptr(syscall.TCIFLUSH))
}

func (watch *InterruptWatch) confirmEscape() {
	if watch.pause.CompareAndSwap(false, true) {
		if watch.onPause != nil {
			watch.onPause()
		}
		return
	}
	if watch.abort.CompareAndSwap(false, true) && watch.onAbort != nil {
		watch.onAbort()
	}
}

func (watch *InterruptWatch) run() {
	defer close(watch.done)
	var classifier escapeClassifier
	var escapeDeadline time.Time
	buffer := make([]byte, 64)
	for {
		select {
		case <-watch.resolve:
			for {
				count, err := syscall.Read(watch.fd, buffer)
				if err == syscall.EINTR {
					continue
				}
				if err != nil || count == 0 {
					break
				}
				for _, value := range buffer[:count] {
					if classifier.Feed(value) > 0 {
						watch.confirmEscape()
					}
				}
			}
			if classifier.Timeout() {
				watch.confirmEscape()
			}
			return
		case <-watch.stop:
			return
		default:
		}

		timeout := 10 * time.Millisecond
		if classifier.Pending() {
			remaining := time.Until(escapeDeadline)
			if remaining <= 0 {
				if classifier.Timeout() {
					watch.confirmEscape()
				}
				continue
			}
			if remaining < timeout {
				timeout = remaining
			}
		}
		ts := syscall.NsecToTimespec(timeout.Nanoseconds())
		descriptor := pollDescriptor{fd: int32(watch.fd), events: pollIn}
		result, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&descriptor)), 1,
			uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return
		}
		if result == 0 {
			continue
		}
		select {
		case <-watch.stop:
			return
		default:
		}
		if descriptor.revents&pollIn == 0 {
			continue
		}
		count, readErr := syscall.Read(watch.fd, buffer)
		if readErr == syscall.EINTR || readErr == syscall.EAGAIN {
			continue
		}
		if readErr != nil || count == 0 {
			return
		}
		for _, value := range buffer[:count] {
			if classifier.Feed(value) > 0 {
				watch.confirmEscape()
			}
			if classifier.Pending() {
				escapeDeadline = time.Now().Add(escapeSequenceTimeout)
			} else {
				escapeDeadline = time.Time{}
			}
		}
	}
}

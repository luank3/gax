//go:build windows

package platform

import (
	"os"
	"os/signal"
)

// WatchSignals forwards os.Interrupt to onAbort on Windows.
func WatchSignals(_, onAbort func()) func() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				if onAbort != nil {
					onAbort()
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// WatchResize is a no-op on Windows console builds.
func WatchResize(func()) func() {
	return func() {}
}

//go:build !windows

package platform

import (
	"os"
	"os/signal"
	"syscall"
)

// WatchSignals forwards SIGUSR1 to onPause and SIGINT/SIGTERM to onAbort.
// Returns a stop function.
func WatchSignals(onPause, onAbort func()) func() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				if sig == syscall.SIGUSR1 {
					if onPause != nil {
						onPause()
					}
				} else if onAbort != nil {
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

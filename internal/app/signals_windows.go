//go:build windows

package app

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/luank3/gax/internal/system/console"
	"github.com/luank3/gax/internal/system/spawn"
)

func watchTerminationEscalation(stopContext func(), killChildren func(), restore func()) func() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	clear := spawn.SetParentInterruptHandlers(func() { signal.Ignore(os.Interrupt) }, func() { signal.Notify(signals, os.Interrupt) })
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		seenStop := false
		for {
			select {
			case <-signals:
				if !seenStop {
					seenStop = true
					stopContext()
					continue
				}
				if killChildren != nil {
					killChildren()
				}
				stopContext()
				restore()
				console.RestoreAll()
				os.Exit(130)
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { signal.Stop(signals); close(done); <-stopped; clear() }) }
}

// Windows has no SIGUSR1; interactive Escape remains the pause gesture.
func notifyPauseSignals(chan os.Signal) {}

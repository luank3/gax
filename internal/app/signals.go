package app

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/luank3/gax/internal/system/spawn"
)

func watchTerminationEscalation(stopContext func(), killChildren func()) func() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	clearInterruptHandlers := spawn.SetParentInterruptHandlers(
		func() { signal.Ignore(os.Interrupt, syscall.SIGQUIT) },
		func() { signal.Notify(signals, os.Interrupt, syscall.SIGQUIT) },
	)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		seenStop := false
		for {
			select {
			case received := <-signals:
				if received == syscall.SIGQUIT {
					reraiseTerminationSignal(signals, received)
					return
				}
				if !seenStop {
					seenStop = true
					stopContext()
					continue
				}
				if killChildren != nil {
					killChildren()
				}
				stopContext()
				reraiseTerminationSignal(signals, received)
				return
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		signal.Stop(signals)
		<-stopped
		clearInterruptHandlers()
	}
}

func reraiseTerminationSignal(signals chan os.Signal, received os.Signal) {
	signal.Stop(signals)
	signal.Reset(received)
	if number, ok := received.(syscall.Signal); ok {
		if err := syscall.Kill(syscall.Getpid(), number); err == nil {
			return
		}
		os.Exit(128 + int(number))
	}
	os.Exit(128 + int(syscall.SIGINT))
}

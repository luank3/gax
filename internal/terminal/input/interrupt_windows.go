//go:build windows

package input

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luank3/gax/internal/system/console"
)

// InterruptWatch owns raw console input while a provider turn is active.
type InterruptWatch struct {
	stop, done            chan struct{}
	once                  sync.Once
	resolve, pause, abort atomic.Bool
	onPause, onAbort      func()
}

// StartInterruptWatch arms Escape handling without disabling console Ctrl+C delivery.
func StartInterruptWatch(stdin *os.File, onPause, onAbort func()) (*InterruptWatch, error) {
	mode, err := console.Raw(stdin, nil, true)
	if err != nil {
		return nil, err
	}
	watch := &InterruptWatch{stop: make(chan struct{}), done: make(chan struct{}), onPause: onPause, onAbort: onAbort}
	go func() {
		defer close(watch.done)
		defer mode.Restore()
		defer console.Flush(stdin)
		watch.run(&console.Source{File: stdin})
	}()
	return watch, nil
}
func (watch *InterruptWatch) PauseRequested() bool { return watch != nil && watch.pause.Load() }
func (watch *InterruptWatch) AbortRequested() bool { return watch != nil && watch.abort.Load() }
func (watch *InterruptWatch) Stop()                { watch.stopWatch(false) }
func (watch *InterruptWatch) StopResolvePending()  { watch.stopWatch(true) }
func (watch *InterruptWatch) stopWatch(resolve bool) {
	if watch == nil {
		return
	}
	watch.once.Do(func() { watch.resolve.Store(resolve); close(watch.stop) })
	<-watch.done
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
func (watch *InterruptWatch) run(source *console.Source) {
	var classifier escapeClassifier
	var deadline time.Time
	for {
		select {
		case <-watch.stop:
			if watch.resolve.Load() {
				// Bound draining so continuously arriving input cannot delay shutdown indefinitely.
				for i := 0; i < 4096; i++ {
					value, available, err := source.ReadByteTimeout(time.Millisecond)
					if err != nil || !available {
						break
					}
					if classifier.Feed(value) > 0 {
						watch.confirmEscape()
					}
				}
				if classifier.Timeout() {
					watch.confirmEscape()
				}
			}
			return
		default:
		}
		value, available, err := source.ReadByteTimeout(10 * time.Millisecond)
		if err != nil {
			return
		}
		if !available {
			if classifier.Pending() && time.Now().After(deadline) && classifier.Timeout() {
				watch.confirmEscape()
			}
			continue
		}
		if classifier.Feed(value) > 0 {
			watch.confirmEscape()
		}
		if classifier.Pending() {
			deadline = time.Now().Add(escapeSequenceTimeout)
		}
	}
}

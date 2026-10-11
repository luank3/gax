package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"

	"github.com/luank3/gax/internal/transport"
)

const (
	defaultCacheMaxBytes = 32 * 1024 * 1024
	defaultFetchTimeout  = 30 * time.Second
	staleWarningAge      = 30 * 24 * time.Hour
)

// FetchOptions makes cache and transport dependencies replaceable for embedders and tests.
// Empty values select the upstream-compatible defaults.
type FetchOptions struct {
	Client    *transport.Client
	CachePath string
	Now       func() time.Time
	MaxBytes  int64
	Timeout   time.Duration
}

type fetchState struct {
	cancel            context.CancelFunc
	done              chan struct{}
	started           time.Time
	generationAtStart uint64
}

// LoadCache reads a bounded snapshot file and installs it only after validation succeeds.
func (catalog *Catalog) LoadCache(path string, maxBytes int64) error {
	if catalog == nil {
		return errors.New("catalog is nil")
	}
	if path == "" {
		return errors.New("catalog cache path is empty")
	}
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	data, truncated, err := systemfs.ReadFileCapped(path, maxBytes)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("catalog cache exceeds %d bytes", maxBytes)
	}
	return catalog.LoadSnapshot(data)
}

// Prefetch starts the one-shot refresh using the default XDG cache path and HTTP client.
func (catalog *Catalog) Prefetch() { catalog.PrefetchWithOptions(FetchOptions{}) }

// PrefetchWithOptions loads the last cached snapshot and starts an asynchronous refresh only
// when it is older than catalog.refresh. The first call consumes the one-shot attempt even when
// fetching is disabled or no cache path is available, matching the process-level upstream API.
func (catalog *Catalog) PrefetchWithOptions(options FetchOptions) {
	if catalog == nil {
		return
	}
	catalog.fetchMu.Lock()
	defer catalog.fetchMu.Unlock()
	if catalog.fetchAttempted {
		return
	}
	catalog.fetchAttempted = true

	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultCacheMaxBytes
	}
	if options.Timeout <= 0 {
		options.Timeout = defaultFetchTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.CachePath == "" {
		options.CachePath = defaultCachePath()
	}
	if options.CachePath == "" {
		return
	}
	_ = catalog.LoadCache(options.CachePath, options.MaxBytes)

	url := ""
	refresh := time.Duration(0)
	if catalog.store != nil {
		if value := catalog.store.String("catalog.url"); value != nil {
			url = *value
		}
		refresh = catalog.store.Duration("catalog.refresh")
	}
	if url == "" || refresh <= 0 {
		return
	}

	if info, err := os.Stat(options.CachePath); err == nil {
		age := options.Now().Sub(info.ModTime())
		if age < refresh {
			return
		}
		if age > staleWarningAge {
			catalog.staleDaysUnreported = int64(age / (24 * time.Hour))
		}
	}

	client := options.Client
	if client == nil {
		client = transport.DefaultClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	state := &fetchState{
		cancel:            cancel,
		done:              make(chan struct{}),
		started:           time.Now(),
		generationAtStart: catalog.cacheGeneration,
	}
	catalog.fetch = state
	go catalog.fetchSnapshot(ctx, state, client, url, options.CachePath, options.MaxBytes, options.Timeout)
}

func (catalog *Catalog) fetchSnapshot(ctx context.Context, state *fetchState, client *transport.Client,
	url, path string, maxBytes int64, timeout time.Duration) {
	defer func() {
		state.cancel()
		close(state.done)
	}()
	data, status, err := client.Get(ctx, url, nil, timeout, maxBytes, nil)
	if err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices {
		return
	}
	providers, err := parseSnapshot(data)
	if err != nil {
		return
	}
	if err := writeCacheAtomic(path, data); err != nil {
		return
	}
	catalog.snapshotMu.Lock()
	catalog.snapshot = providers
	catalog.snapshotMu.Unlock()
	catalog.fetchMu.Lock()
	catalog.cacheGeneration++
	catalog.fetchMu.Unlock()
}

// StaleDays returns the age of the stale snapshot observed when refresh began, once. A
// successful replacement before the call suppresses the warning.
func (catalog *Catalog) StaleDays() int64 {
	if catalog == nil {
		return 0
	}
	catalog.fetchMu.Lock()
	defer catalog.fetchMu.Unlock()
	days := catalog.staleDaysUnreported
	catalog.staleDaysUnreported = 0
	if catalog.fetch != nil && catalog.cacheGeneration != catalog.fetch.generationAtStart {
		return 0
	}
	return days
}

// Wait waits for the current refresh up to a budget anchored at fetch start. A tick callback
// that returns true abandons the wait but leaves the refresh running. The result is true once no
// refresh is running or it has completed.
func (catalog *Catalog) Wait(maxWait time.Duration, tick func() bool) bool {
	state := catalog.currentFetch()
	if state == nil || isDone(state.done) {
		return true
	}
	return catalog.waitState(state, maxWait, tick, true)
}

// Drain gives a refresh a call-relative grace period, then cancels and joins it.
func (catalog *Catalog) Drain(maxWait time.Duration) {
	state := catalog.currentFetch()
	if state == nil {
		return
	}
	if !isDone(state.done) && !catalog.waitState(state, maxWait, nil, false) {
		state.cancel()
	}
	<-state.done
}

// Shutdown cancels and joins an active refresh. Catalog snapshots remain available to readers.
func (catalog *Catalog) Shutdown() {
	if catalog == nil {
		return
	}
	catalog.Drain(0)
}

func (catalog *Catalog) currentFetch() *fetchState {
	if catalog == nil {
		return nil
	}
	catalog.fetchMu.Lock()
	defer catalog.fetchMu.Unlock()
	return catalog.fetch
}

func (catalog *Catalog) waitState(state *fetchState, maxWait time.Duration, tick func() bool,
	anchored bool) bool {
	budget := maxWait
	if anchored {
		budget -= time.Since(state.started)
	}
	if budget <= 0 {
		return isDone(state.done)
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	if tick != nil && tick() {
		return false
	}
	for {
		select {
		case <-state.done:
			return true
		case <-timer.C:
			return isDone(state.done)
		case <-ticker.C:
			if tick != nil && tick() {
				return false
			}
		}
	}
}

func isDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func defaultCachePath() string {
	path, ok := systempath.XDGGaxPath(os.Getenv("XDG_CACHE_HOME"), os.Getenv("HOME"), ".cache", "catalog.json")
	if !ok {
		return ""
	}
	return path
}

func writeCacheAtomic(path string, data []byte) error {
	directory := systempath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".catalog-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

package catalog_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/transport"
)

const oldCatalog = `{"vendor":{"models":{"m":{"cost":{"input":1,"output":2}}}}}`
const freshCatalog = `{"vendor":{"models":{"m":{"cost":{"input":3,"output":4}}}}}`

func lifecycleStore(url string, refresh string) *config.Store {
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	store.SetOverride("catalog.url", &url)
	store.SetOverride("catalog.refresh", &refresh)
	return store
}

func writeCatalog(t *testing.T, path, body string, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCacheIsBoundedAndPreservesLastSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	writeCatalog(t, path, oldCatalog, time.Now())
	modelCatalog := catalog.New(nil)
	if err := modelCatalog.LoadCache(path, 1024); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 20)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := modelCatalog.LoadCache(path, 10); err == nil {
		t.Fatal("expected oversized cache to be rejected")
	}
	entry, found := modelCatalog.Lookup("", "vendor", "m")
	if !found || entry.CostInput != 1 {
		t.Fatalf("bad cache load replaced the last good snapshot: %+v found=%t", entry, found)
	}
}

func TestPrefetchLoadsFreshCacheWithoutFetching(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "catalog.json")
	writeCatalog(t, path, oldCatalog, time.Now())
	modelCatalog := catalog.New(lifecycleStore(server.URL, "24h"))
	modelCatalog.PrefetchWithOptions(catalog.FetchOptions{CachePath: path, Client: &transport.Client{HTTP: server.Client()}})
	if !modelCatalog.Wait(time.Second, nil) {
		t.Fatal("fresh cache should not start a background fetch")
	}
	entry, found := modelCatalog.Lookup("", "vendor", "m")
	if !found || entry.CostInput != 1 || requests.Load() != 0 {
		t.Fatalf("fresh cache should be loaded without a network request: %+v found=%t requests=%d", entry, found, requests.Load())
	}
}

func TestPrefetchRefreshesStaleCacheAsynchronously(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = response.Write([]byte(freshCatalog))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "catalog.json")
	writeCatalog(t, path, oldCatalog, time.Now().Add(-40*24*time.Hour))
	modelCatalog := catalog.New(lifecycleStore(server.URL, "24h"))
	modelCatalog.PrefetchWithOptions(catalog.FetchOptions{CachePath: path, Client: &transport.Client{HTTP: server.Client()}})
	if !modelCatalog.Wait(3*time.Second, nil) {
		t.Fatal("refresh did not finish within the wait budget")
	}
	if days := modelCatalog.StaleDays(); days != 0 {
		t.Fatalf("a completed refresh should suppress the stale warning, got %d days", days)
	}
	entry, found := modelCatalog.Lookup("", "vendor", "m")
	cached, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || entry.CostInput != 3 || !strings.Contains(string(cached), `"input":3`) || requests.Load() != 1 {
		t.Fatalf("refresh did not atomically replace snapshot and cache: entry=%+v found=%t requests=%d cache=%s", entry, found, requests.Load(), cached)
	}
}

func TestFailedRefreshKeepsCacheAndReportsStaleAgeOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"error":"invalid snapshot"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "catalog.json")
	staleTime := time.Now().Add(-40 * 24 * time.Hour)
	writeCatalog(t, path, oldCatalog, staleTime)
	modelCatalog := catalog.New(lifecycleStore(server.URL, "24h"))
	modelCatalog.PrefetchWithOptions(catalog.FetchOptions{CachePath: path, Client: &transport.Client{HTTP: server.Client()}})
	if !modelCatalog.Wait(3*time.Second, nil) {
		t.Fatal("invalid response request did not finish")
	}
	if days := modelCatalog.StaleDays(); days < 30 || days > 40 {
		t.Fatalf("expected stale cache age once, got %d days", days)
	}
	if days := modelCatalog.StaleDays(); days != 0 {
		t.Fatalf("stale age must be reported only once, got %d", days)
	}
	cached, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := modelCatalog.Lookup("", "vendor", "m")
	if string(cached) != oldCatalog || !found || entry.CostInput != 1 {
		t.Fatalf("failed refresh damaged last good state: cache=%s entry=%+v found=%t", cached, entry, found)
	}
}

func TestWaitTickAbandonsWaitWithoutCancellingFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = response.Write([]byte(freshCatalog))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "catalog.json")
	writeCatalog(t, path, oldCatalog, time.Now().Add(-2*24*time.Hour))
	modelCatalog := catalog.New(lifecycleStore(server.URL, "24h"))
	modelCatalog.PrefetchWithOptions(catalog.FetchOptions{CachePath: path, Client: &transport.Client{HTTP: server.Client()}})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fetch did not start")
	}
	if modelCatalog.Wait(time.Second, func() bool { return true }) {
		t.Fatal("tick should abandon the wait while refresh is still blocked")
	}
	close(release)
	if !modelCatalog.Wait(3*time.Second, nil) {
		t.Fatal("abandoned wait should leave fetch running")
	}
	entry, found := modelCatalog.Lookup("", "vendor", "m")
	if !found || entry.CostInput != 3 {
		t.Fatalf("background fetch did not finish after an early wait return: %+v found=%t", entry, found)
	}
}

func TestDrainCancelsFetchAndShutdownIsSafe(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "catalog.json")
	writeCatalog(t, path, oldCatalog, time.Now().Add(-2*24*time.Hour))
	modelCatalog := catalog.New(lifecycleStore(server.URL, "24h"))
	modelCatalog.PrefetchWithOptions(catalog.FetchOptions{CachePath: path, Client: &transport.Client{HTTP: server.Client()}})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fetch did not start")
	}
	modelCatalog.Drain(0)
	modelCatalog.Shutdown()
	if modelCatalog.Wait(0, nil) != true {
		t.Fatal("drained fetch should no longer be running")
	}
}

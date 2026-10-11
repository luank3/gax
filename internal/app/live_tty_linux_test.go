//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/registry"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	"github.com/luank3/gax/internal/transport"
)

func TestTTYBusyOperationShowsStatusAndDoubleEscapeCancelsFetch(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	busy := startBusyOperation(context.Background(), slave, true, slave, "fetching usage...")

	result := make(chan error, 1)
	go func() {
		_, _, err := (&transport.Client{HTTP: server.Client()}).Get(
			busy.Context(), server.URL, nil, 10*time.Second, 1024, busy.Tick)
		result <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not start")
	}
	waitPTYOutput(t, capture, "fetching usage...", 2*time.Second)
	if !strings.Contains(capture.string(), "\x1b[2m") {
		t.Fatalf("busy spinner was not dimmed: %q", capture.string())
	}
	if _, err := master.Write([]byte{0x1b, 0x1b}); err != nil {
		t.Fatal(err)
	}
	var requestErr error
	select {
	case requestErr = <-result:
	case <-time.After(2 * time.Second):
		t.Fatalf("double Escape did not cancel provider request: %q", capture.string())
	}
	if !errors.Is(requestErr, transport.ErrCancelled) {
		t.Fatalf("cancelled provider request error = %v, want %v", requestErr, transport.ErrCancelled)
	}
	cancelled, err := busy.End()
	if err != nil || !cancelled {
		t.Fatalf("busy operation end = cancelled %v, error %v", cancelled, err)
	}
	waitPTYOutput(t, capture, "[interrupted]", time.Second)
	got := capture.string()
	if !strings.Contains(got, "\r\x1b[2K") || !strings.Contains(got, "[interrupted]") {
		t.Fatalf("busy indicator was not cleared with an interrupt marker: %q", got)
	}
}

func TestTTYBusyEndResolvesDoubleEscapeAlreadyQueuedOnInput(t *testing.T) {
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	busy := startBusyOperation(context.Background(), slave, true, slave, "fetching models...")
	if _, err := master.Write([]byte{0x1b, 0x1b}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := busy.End()
	if err != nil || !cancelled {
		t.Fatalf("busy end did not resolve the queued double Escape: cancelled=%v error=%v", cancelled, err)
	}
	waitPTYOutput(t, capture, "[interrupted]", time.Second)
}

func TestTTYUsageFetchUsesBusyCancellationWithoutReportingProviderError(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/key" {
			t.Errorf("usage request path = %q, want /key", request.URL.Path)
		}
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()

	store := config.New(config.DefaultSettings())
	store.SetEnvironment(func(name string) (string, bool) {
		if name == "OPENROUTER_API_KEY" {
			return "test-key", true
		}
		return "", false
	})
	provider, err := runtime.NewResolver(store, registry.New(store)).Resolve("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = server.URL

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	var stderr bytes.Buffer
	finished := make(chan struct{})
	go func() {
		runProviderUsage(context.Background(), provider, &transport.Client{HTTP: server.Client()}, 80,
			slave, true, slave, &stderr)
		close(finished)
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenRouter usage request did not start")
	}
	waitPTYOutput(t, capture, "fetching usage...", 2*time.Second)
	if _, err := master.Write([]byte{0x1b, 0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("/usage did not return after double Escape: %q", capture.string())
	}
	waitPTYOutput(t, capture, "[interrupted]", time.Second)
	if stderr.Len() != 0 {
		t.Fatalf("user-cancelled /usage printed a provider error: %q", stderr.String())
	}
}

func TestRunTTYREPLStreamsAnswerBeforeProviderCompletes(t *testing.T) {
	firstChunk := make(chan struct{})
	firstResponse := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		if requests.Add(1) > 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"live-final\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		<-firstResponse
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"old-visible \"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-release
	}))
	defer server.Close()
	defer func() {
		select {
		case <-firstResponse:
		default:
			close(firstResponse)
		}
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "working...", 2*time.Second)
	close(firstResponse)
	select {
	case <-firstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not send its first event")
	}
	seenBeforeCompletion := waitPTYOutputMaybe(capture, "old-visible", 300*time.Millisecond)
	close(release)
	waitPTYOutput(t, capture, "live-final", 5*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 2*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	got := capture.string()
	if !seenBeforeCompletion {
		t.Fatalf("first answer text was not visible while the provider stream was paused: %q", got)
	}
	if !strings.Contains(got, "▌ hello") {
		t.Fatalf("submitted prompt was not rendered as a committed user message: %q", got)
	}
	answerAt := strings.Index(got, "live-final")
	if answerAt < 0 {
		t.Fatalf("final answer missing from terminal output: %q", got)
	}
	firstAnswerAt := strings.Index(got, "old-visible")
	if firstAnswerAt < 0 {
		t.Fatalf("first streamed answer is missing from terminal output: %q", got)
	}
	statusClearAt := strings.LastIndex(got[:firstAnswerAt], "\r\x1b[2K")
	if statusClearAt < 0 || got[statusClearAt:firstAnswerAt] != "\r\x1b[2K\r\n" {
		t.Fatalf("first answer did not start one blank line after the cleared status: %q", got)
	}
	statsAt := strings.Index(got[answerAt:], "\x1b[2m")
	if statsAt < 0 || !strings.Contains(got[answerAt:answerAt+statsAt], "\r\n\r\n") {
		t.Fatalf("turn stats were not separated from the answer by a blank line: %q", got)
	}
	if strings.Count(got, "old-visible") != 1 || strings.Count(got, "live-final") != 1 || strings.Count(got, "[unexpected end]") != 1 {
		t.Fatalf("retry output was missing, duplicated, or unmarked: %q", got)
	}
}

func TestRunTTYEditorSignalDoesNotCancelTheREPL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if serveTTYModelProbe(w, request) {
			return
		}
		t.Errorf("unexpected provider request during editor signal test: %s", request.URL.Path)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	editorPath := filepath.Join(t.TempDir(), "editor")
	editorScript := "#!/bin/sh\nkill -INT \"$GAX_TEST_APP_PID\"\nprintf 'edited draft' > \"$1\"\n"
	if err := os.WriteFile(editorPath, []byte(editorScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editorPath)
	t.Setenv("GAX_TEST_APP_PID", strconv.Itoa(os.Getpid()))

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte{0x07}); err != nil {
		t.Fatal(err)
	}
	if !waitPTYOutputMaybe(capture, "edited draft", time.Second) {
		t.Fatalf("editor result was not restored to the prompt: %q", capture.string())
	}
	if _, err := master.Write([]byte{0x15, 0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d after editor signal", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D: %q", capture.string())
	}
}

func TestRunTTYUndoWithoutArgumentPicksLastUserTurn(t *testing.T) {
	requestBody := make(chan string, 1)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requestNumber := requests.Add(1)
		if requestNumber == 2 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read provider request: %v", err)
			}
			requestBody <- string(body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		answer := "first answer"
		if requestNumber == 2 {
			answer = "answer after undo"
		}
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", answer)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("first prompt\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "first answer", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("/undo\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "revert to before which message", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYEditorReadyCount(t, capture, 3, 3*time.Second)
	if _, err := master.Write([]byte("after undo\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "answer after undo", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 4, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	select {
	case body := <-requestBody:
		if !strings.Contains(body, "after undo") || strings.Contains(body, "first prompt") || strings.Contains(body, "first answer") {
			t.Fatalf("selected history turn was not undone before the next request: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive the request after undo")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider request count = %d, want the original and post-undo requests", got)
	}
}

func TestRunTTYForkWithoutArgumentPicksLastUserTurn(t *testing.T) {
	requestBody := make(chan string, 1)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		if requestNumber == 3 {
			requestBody <- string(body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		answer := fmt.Sprintf("answer %d", requestNumber)
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", answer)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_NO_SESSION", "off")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("first prompt\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "answer 1", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("second prompt\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "answer 2", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 3, 3*time.Second)
	if _, err := master.Write([]byte("/fork\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "fork before which message", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "forked · ctrl-o for full history", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 4, 3*time.Second)
	if _, err := master.Write([]byte("continue fork\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "answer 3", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 5, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	select {
	case body := <-requestBody:
		if !strings.Contains(body, "first prompt") || !strings.Contains(body, "answer 1") ||
			strings.Contains(body, "second prompt") || strings.Contains(body, "answer 2") ||
			!strings.Contains(body, "continue fork") {
			t.Fatalf("fork did not branch before the selected latest user turn: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not receive the fork continuation")
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("provider request count = %d, want the two source turns and one fork turn", got)
	}
}

func TestRunTTYFirstEscapePausesAtProviderTurnSeam(t *testing.T) {
	firstChunk := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-release
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\" finished\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("pause me\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not send its first event")
	}
	waitPTYOutput(t, capture, "partial answer", 2*time.Second)
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(stripPTYANSI(capture.string()), "paused — enter to continue") {
		t.Fatal("first Escape ended the turn before the provider reached its seam")
	}
	close(release)
	waitPTYOutput(t, capture, "paused — enter to continue", 5*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	if got := stripPTYANSI(capture.string()); !strings.Contains(got, "partial answer finished") || strings.Contains(got, "[interrupted]") {
		t.Fatalf("soft pause did not preserve the completed response: %q", got)
	}
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
}

func TestInterruptWatchEscapeCallbacks(t *testing.T) {
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	paused := make(chan struct{})
	aborted := make(chan struct{})
	watch, err := terminalinput.StartInterruptWatch(slave, func() { close(paused) }, func() { close(aborted) })
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Stop()
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("bare Escape did not invoke the pause callback")
	}
	if !watch.PauseRequested() || watch.AbortRequested() {
		t.Fatalf("first Escape state: pause=%t abort=%t", watch.PauseRequested(), watch.AbortRequested())
	}
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("second bare Escape did not invoke the abort callback")
	}
	if !watch.AbortRequested() {
		t.Fatal("second Escape did not latch abort")
	}
}

func TestRunTTYFirstEscapeCancelsEmptyRequestAndCanResume(t *testing.T) {
	firstRequest := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		if requests.Add(1) == 1 {
			close(firstRequest)
			<-release
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"resumed answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("pause before answer\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstRequest:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the first request")
	}
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "paused — enter to continue", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	close(release)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "resumed answer", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider request count = %d, want interrupted and resumed requests", got)
	}
}

func TestRunTTYFirstEscapePausesAutoCompactionAndDefersIt(t *testing.T) {
	compactionStarted := make(chan struct{})
	compactionCanceled := make(chan struct{})
	var requests atomic.Int32
	var resumedWithSummary atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestNumber {
		case 1:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ordinary answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
		case 2:
			close(compactionStarted)
			<-r.Context().Done()
			close(compactionCanceled)
		case 3:
			if strings.Contains(string(body), "Summarize the conversation so far") && strings.Contains(string(body), "ordinary answer") {
				resumedWithSummary.Store(true)
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary seed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case 4:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"resumed answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected provider request %d", requestNumber)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configBody := `{"catalog":{"models":{"openai-compatible":{"test-model":{"limit":{"context":1000}}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("GAX_COMPACT_AUTO", "on")
	t.Setenv("GAX_COMPACT_THRESHOLD", "1")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("compact me\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic compaction did not start")
	}
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Escape did not cancel the automatic compaction request")
	}
	waitPTYOutput(t, capture, "paused — enter to continue", 5*time.Second)
	if strings.Contains(stripPTYANSI(capture.string()), "[interrupted]") {
		t.Fatalf("single Escape aborted instead of pausing compaction: %q", capture.string())
	}
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "resumed answer", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 4 || !resumedWithSummary.Load() {
		t.Fatalf("deferred compaction was not resumed before the next turn: requests=%d summary=%t output=%q",
			got, resumedWithSummary.Load(), capture.string())
	}
}

func TestRunTTYFirstEscapeCancelsManualCompaction(t *testing.T) {
	compactionStarted := make(chan struct{})
	compactionCanceled := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer before compaction\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
			return
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("read compaction request: %v", err)
		}
		close(compactionStarted)
		select {
		case <-r.Context().Done():
			close(compactionCanceled)
		case <-release:
		}
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	prepareTTYAppEnv(t, server.URL)
	configRoot := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configBody := `{"catalog":{"models":{"openai-compatible":{"test-model":{"limit":{"context":1000}}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("GAX_COMPACT_AUTO", "on")
	t.Setenv("GAX_COMPACT_THRESHOLD", "85")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("compact manually\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "answer before compaction", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("/compact keep the key details\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("manual compaction did not start")
	}
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Escape did not cancel manual compaction")
	}
	waitPTYOutput(t, capture, "compaction cancelled", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 3, 3*time.Second)
	if got := stripPTYANSI(capture.string()); strings.Contains(got, "[interrupted]") {
		t.Fatalf("canceling manual compaction marked the conversation interrupted: %q", got)
	}
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider request count = %d, want initial answer and canceled compaction", got)
	}
}

func TestRunTTYFirstEscapeCancelsDeferredCompactionBeforeSendingPrompt(t *testing.T) {
	compactionStarted := make(chan struct{})
	compactionCanceled := make(chan struct{})
	release := make(chan struct{})
	requestBodies := make(chan string, 8)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requestNumber := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		requestBodies <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestNumber {
		case 1:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"echo seam\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n")
		case 2:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case 3:
			close(compactionStarted)
			select {
			case <-r.Context().Done():
				close(compactionCanceled)
			case <-release:
			}
		case 4:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary seed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case 5:
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"retried answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected provider request %d", requestNumber)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	defer close(release)
	prepareTTYAppEnv(t, server.URL)
	configRoot := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configBody := `{"catalog":{"models":{"openai-compatible":{"test-model":{"limit":{"context":1000}}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("GAX_COMPACT_AUTO", "on")
	t.Setenv("GAX_COMPACT_THRESHOLD", "1")
	t.Setenv("GAX_MAX_TURNS", "1")
	t.Setenv("GAX_CATALOG_URL", "")
	t.Setenv("GAX_CATALOG_REFRESH", "0")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("first turn\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "enter to continue", 5*time.Second)
	if _, err := master.Write([]byte("must not be sent\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("deferred compaction did not start before sending the next prompt")
	}
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compactionCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Escape did not cancel deferred compaction")
	}
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 3, 3*time.Second)
	if _, err := master.Write([]byte("retry after compaction\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "retried answer", 5*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 4, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 5 {
		t.Fatalf("provider request count = %d, want the original turn, two compaction attempts, and retried turn", got)
	}
	bodies := make([]string, 5)
	for index := range bodies {
		select {
		case bodies[index] = <-requestBodies:
		case <-time.After(time.Second):
			t.Fatal("provider request body was not recorded")
		}
	}
	if !strings.Contains(bodies[2], "Summarize the conversation so far") ||
		strings.Contains(bodies[2], "must not be sent") ||
		!strings.Contains(bodies[3], "Summarize the conversation so far") ||
		strings.Contains(bodies[3], "must not be sent") ||
		!strings.Contains(bodies[4], "retry after compaction") {
		t.Fatalf("deferred compaction cancellation did not preserve debt and discard only the canceled prompt: bodies=%q", bodies)
	}
}

func TestRunTTYDoubleEscapeAbortsProviderRequest(t *testing.T) {
	firstChunk := make(chan struct{})
	requestCanceled := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("abort me\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not send its first event")
	}
	waitPTYOutput(t, capture, "partial answer", 2*time.Second)
	if _, err := master.Write([]byte{0x1b, 0x1b}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("double Escape did not cancel the in-flight provider request")
	}
	waitPTYOutput(t, capture, "[interrupted]", 5*time.Second)
	waitPTYOutput(t, capture, "enter to continue", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider request count = %d, want the interrupted request only", got)
	}
}

func TestRunTTYDoubleEscapeStopsRunningBashTool(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"bash-call\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"sleep 30\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("stop tool\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "[bash] sleep 30", 5*time.Second)
	if _, err := master.Write([]byte{0x1b, 0x1b}); err != nil {
		t.Fatal(err)
	}
	waitPTYOutputCount(t, capture, "[interrupted]", 2, 5*time.Second)
	waitPTYOutput(t, capture, "enter to continue", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider request count = %d, want one tool request", got)
	}
}

func TestRunTTYCtrlTShowsAndMirrorsModelFacingTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"transcript reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("PAGER", "cat")
	transcriptPath := filepath.Join(t.TempDir(), "model-transcript.txt")
	t.Setenv("GAX_TRANSCRIPT", transcriptPath)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("hello transcript\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "transcript reply", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte{0x14}); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "TRANSCRIPT", 3*time.Second)
	waitPTYOutput(t, capture, "hello transcript", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 3, 3*time.Second)
	if _, err := master.Write([]byte{0x0f}); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "conversation · 1 prompt", 3*time.Second)
	waitPTYOutput(t, capture, "transcript reply", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 4, 3*time.Second)
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after transcript view; output: %q", capture.string())
	}
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\x1b[") || !strings.Contains(string(data), "hello transcript") ||
		!strings.Contains(string(data), "transcript reply") || !strings.Contains(string(data), "system prompt") {
		t.Fatalf("plain transcript mirror omitted context or included terminal controls: %q", data)
	}
}

func TestRunTTYResumeWithoutIDOpensPickerAndRestoresSelectedContext(t *testing.T) {
	var requestBody = make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		requestBody <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	provider, model := "openai-compatible", "test-model"
	log, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append([]conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("earlier resumable request")}}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run([]string{"--resume"}, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "type to search 1 item", 2*time.Second)
	if _, err := master.Write([]byte("earlier\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("continue\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "continued", 5*time.Second)
	waitPTYEditorReadyCount(t, capture, 1, 3*time.Second)
	var request string
	select {
	case request = <-requestBody:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the resumed turn")
	}
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY resume exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY resume did not exit; output: %q", capture.string())
	}
	if !strings.Contains(request, "earlier resumable request") || !strings.Contains(request, "continue") {
		t.Fatalf("selected session context was not restored: %s", request)
	}
}

func TestRunTTYSlashResumeSwitchesConversationAndExcludesCurrent(t *testing.T) {
	var requestBodies = make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		requestBodies <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_NO_SESSION", "off")
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	provider, model := "openai-compatible", "test-model"
	log, err := session.NewLog(cwd, session.Selection{Provider: &provider, Model: &model})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append([]conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("selected-history sentinel")}}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("current-only sentinel\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := receiveRequestBody(t, requestBodies); err != nil {
		t.Fatal(err)
	}
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("/resume\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "type to search 1 item", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "resumed · ctrl-o for full history", 3*time.Second)
	waitPTYOutput(t, capture, "selected-history sentinel", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 3, 3*time.Second)
	if _, err := master.Write([]byte("continue selected history\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "reply", 5*time.Second)
	request, err := receiveRequestBody(t, requestBodies)
	if err != nil {
		t.Fatal(err)
	}
	waitPTYEditorReadyCount(t, capture, 3, 5*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY /resume exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY /resume did not exit; output: %q", capture.string())
	}
	if !strings.Contains(request, "selected-history sentinel") || !strings.Contains(request, "continue selected history") ||
		strings.Contains(request, "current-only sentinel") {
		t.Fatalf("/resume did not replace current context with selected history: %s", request)
	}
}

func TestRunTTYSlashEffortPickerAppliesSelectedLevel(t *testing.T) {
	var requestBody = make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		requestBody <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"catalog":{"models":{"openai-compatible":{"test-model":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/effort\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select reasoning effort", 3*time.Second)
	waitPTYOutput(t, capture, "high", 3*time.Second)
	if _, err := master.Write([]byte("\x1b[B\x1b[B\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("use selected level\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "reply", 5*time.Second)
	var body string
	select {
	case body = <-requestBody:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the request")
	}
	waitPTYEditorReadyCount(t, capture, 3, 5*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY /effort exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY /effort did not exit; output: %q", capture.string())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	var effort string
	_ = json.Unmarshal(decoded["reasoning_effort"], &effort)
	if effort != "high" {
		t.Fatalf("picker selected reasoning effort %q, want high; request=%s", effort, body)
	}
}

func TestRunTTYREPLNotifiesWhenTurnCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ready\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_NOTIFY", "bel")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "ready", 5*time.Second)
	waitPTYOutput(t, capture, "\a", 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY notification exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY notification run did not exit; output: %q", capture.string())
	}
}

func TestRunTTYSlashModelPickerSwitchesModelForNextRequest(t *testing.T) {
	var requestModel = make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"alpha-model"},{"id":"beta-model"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestModel <- body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_MODEL", "")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/model\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a model", 3*time.Second)
	waitPTYOutput(t, capture, "beta-model", 3*time.Second)
	if _, err := master.Write([]byte("beta\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select reasoning effort", 3*time.Second)
	waitPTYOutput(t, capture, "Let the provider choose the reasoning effort", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "openai-compatible · beta-model", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	if _, err := master.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "reply", 5*time.Second)
	select {
	case got := <-requestModel:
		if got != "beta-model" {
			t.Fatalf("request model = %q, want beta-model after picker selection", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the request")
	}
	waitPTYEditorReadyCount(t, capture, 3, 5*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY /model picker exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY /model picker did not exit; output: %q", capture.string())
	}
}

func TestRunTTYSlashModelPickerSortsVersionedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5-mini"},{"id":"gpt-5"},{"id":"gpt-5.6"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_SORT_MODELS", "auto")

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/model\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a model", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select reasoning effort", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "openai-compatible · gpt-5.6", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY model sorting exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY model sorting run did not exit; output: %q", capture.string())
	}
}

func TestRunTTYSlashProviderPickerSwitchesProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"default_generation_settings":{"n_ctx":65536},"modalities":{"vision":true}}`)
			return
		}
		if r.URL.Path != "/models" {
			t.Errorf("provider probe path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"local-model-a"},{"id":"local-model-b"}]}`)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, "http://127.0.0.1:1")
	t.Setenv("GAX_LLAMACPP_BASE_URL", server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/provider\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a provider", 3*time.Second)
	if _, err := master.Write([]byte("llama\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a model", 3*time.Second)
	if _, err := master.Write([]byte("local-model-b\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "llama.cpp · local-model-b", 3*time.Second)
	waitPTYEditorReadyCount(t, capture, 2, 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY /provider picker exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY /provider picker did not exit; output: %q", capture.string())
	}
}

func TestRunTTYSlashProviderPickerChainsModelAndEffortSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("unexpected request during selection: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, "http://127.0.0.1:1")
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"providers":{"test":{"base_url":"` + server.URL + `","api_key":"test-key"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	var stderr strings.Builder
	go func() { finished <- Run(nil, slave, true, slave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/provider\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a provider", 3*time.Second)
	if _, err := master.Write([]byte("test\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select reasoning effort", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "test · test-model", 3*time.Second)
	if _, err := master.Write([]byte("/session\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "test · test-model", 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY provider selection chain exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY provider selection chain did not exit; output: %q", capture.string())
	}
	if stderr.Len() != 0 {
		t.Fatalf("TTY provider selection chain stderr = %q", stderr.String())
	}
}

func TestRunTTYSlashPresetPickerAppliesSelectedPreset(t *testing.T) {
	requestBody := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		requestBody <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"review":{"description":"Review changes","provider":"openai-compatible","model":"review-model","system_prompt":"review-only system directive"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/preset\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "select a preset", 3*time.Second)
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	if _, err := master.Write([]byte("review this\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "reply", 5*time.Second)
	var body string
	select {
	case body = <-requestBody:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not receive the preset request")
	}
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 3, 5*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY preset picker exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY preset picker did not exit; output: %q", capture.string())
	}
	if !strings.Contains(body, `"model":"review-model"`) || !strings.Contains(body, "review-only system directive") {
		t.Fatalf("picker-applied preset did not reach the provider request: %s", body)
	}
}

func TestRunTTYPresetSavePreseedsNameAndActivatesSavedPreset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		t.Error("preset save unexpectedly called the provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(`{"presets":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	var stderr strings.Builder
	go func() { finished <- Run(nil, slave, true, slave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/preset-save\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "name it: /preset-save <name> [tint]", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	if _, err := master.Write([]byte("review rose\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "saved preset 'review'", 3*time.Second)
	waitPTYOutput(t, capture, "[review]", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 3, 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY preset save exit code = %d; output=%q stderr=%q", code, capture.string(), stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY preset save did not exit; output=%q stderr=%q", capture.string(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(configRoot, "gax", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Presets map[string]map[string]string `json:"presets"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if preset := decoded.Presets["review"]; preset["provider"] != "openai-compatible" ||
		preset["model"] != "test-model" || preset["tint"] != "rose" {
		t.Fatalf("preseeded name/tint were not saved: %s", data)
	}
	if stderr.Len() != 0 {
		t.Fatalf("TTY preset save stderr = %q", stderr.String())
	}
}

func TestRunTTYPresetSaveConfirmsOverwriteAndCanClearTint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		t.Error("preset save unexpectedly called the provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "gax"), 0700); err != nil {
		t.Fatal(err)
	}
	configText := `{"presets":{"review":{"description":"Keep this description","provider":"openai-compatible","model":"old-model","tint":"sage"}}}`
	if err := os.WriteFile(filepath.Join(configRoot, "gax", "config.json"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	var stderr strings.Builder
	go func() { finished <- Run(nil, slave, true, slave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/preset-save review\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "already exists", 3*time.Second)
	if _, err := master.Write([]byte("overwrite\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "tint for this preset", 3*time.Second)
	if _, err := master.Write([]byte("none\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "updated preset 'review'", 3*time.Second)
	waitPTYOutput(t, capture, "[review]", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY preset overwrite exit code = %d; output=%q stderr=%q", code, capture.string(), stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY preset overwrite did not exit; output=%q stderr=%q", capture.string(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(configRoot, "gax", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Presets map[string]map[string]string `json:"presets"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	preset := decoded.Presets["review"]
	if preset["description"] != "Keep this description" || preset["provider"] != "openai-compatible" ||
		preset["model"] != "test-model" || preset["tint"] != "" {
		t.Fatalf("overwrite did not preserve description and clear tint: %s", data)
	}
	if _, exists := preset["tint"]; exists {
		t.Fatalf("cleared tint was persisted instead of omitted: %s", data)
	}
	if stderr.Len() != 0 {
		t.Fatalf("TTY preset overwrite stderr = %q", stderr.String())
	}
}

func TestRunTTYConfigPickerSelectsAndAppliesChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		t.Error("configuration picker unexpectedly called the provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	var stderr strings.Builder
	go func() { finished <- Run(nil, slave, true, slave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/config\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "configuration", 3*time.Second)
	if _, err := master.Write([]byte("theme\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "theme — Color theme", 3*time.Second)
	if _, err := master.Write([]byte("dark\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "theme = dark (run)", 3*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY config picker exit code = %d; output: %q stderr=%q", code, capture.string(), stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY config picker did not exit; output: %q stderr=%q", capture.string(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("configuration picker emitted diagnostics: %q", stderr.String())
	}
}

func TestRunTTYConfigPickerSeedsExactTokenValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		t.Error("configuration picker unexpectedly called the provider")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)

	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	var stderr strings.Builder
	go func() { finished <- Run(nil, slave, true, slave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("/config\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "configuration", 3*time.Second)
	if _, err := master.Write([]byte("context_limit\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "/config context_limit ", 3*time.Second)
	if _, err := master.Write([]byte("200k\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "context_limit = 200k (run)", 3*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("TTY config exact value exit code = %d; output: %q stderr=%q", code, capture.string(), stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("TTY config exact value did not exit; output: %q stderr=%q", capture.string(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("configuration exact value emitted diagnostics: %q", stderr.String())
	}
}

func receiveRequestBody(t *testing.T, bodies <-chan string) (string, error) {
	t.Helper()
	select {
	case body := <-bodies:
		return body, nil
	case <-time.After(3 * time.Second):
		return "", fmt.Errorf("provider did not receive a request")
	}
}

func TestRunTTYREPLStreamsToolOutputAndPreservesCanonicalHistory(t *testing.T) {
	var requests atomic.Int32
	var canonicalToolResult atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		if requests.Add(1) == 1 {
			arguments, _ := json.Marshal(map[string]string{"command": "printf DISPLAY_SENTINEL; exit 7"})
			call := map[string]any{
				"index": 0, "id": "bash-call", "type": "function",
				"function": map[string]string{"name": "bash", "arguments": string(arguments)},
			}
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls",
			}}})
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read replay request: %v", err)
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode replay request: %v", err)
		} else {
			for _, message := range request.Messages {
				if message.Role == "tool" {
					canonicalToolResult.Store(message.Content)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_NO_SESSION", "off")
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	capture := &ptyCapture{}
	go capture.read(master)
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, slave, true, slave, io.Discard) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := master.Write([]byte("run tool\r")); err != nil {
		t.Fatal(err)
	}
	waitPTYOutput(t, capture, "done", 5*time.Second)
	waitPTYOutputCount(t, capture, "\x1b[?2004h", 2, 2*time.Second)
	_, _ = master.Write([]byte{0x04})
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("REPL exit code = %d; output: %q", code, capture.string())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("REPL did not exit after Ctrl-D; output: %q", capture.string())
	}
	if got, ok := canonicalToolResult.Load().(string); !ok || got != "DISPLAY_SENTINEL\n[exit 7]" {
		t.Fatalf("canonical tool result sent back to the provider = %q, want complete result", got)
	}
	if got := capture.string(); strings.Count(got, "\x1b[2mDISPLAY_SENTINEL") != 1 || !strings.Contains(got, "[exit 7]") {
		t.Fatalf("tool output was not displayed once with its live status: %q", got)
	}
	cwd, _ := os.Getwd()
	directory, err := session.Directory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read persisted session directory: %v", err)
	}
	var foundToolResult bool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		items, _, loadErr := session.Load(filepath.Join(directory, entry.Name()))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, item := range items {
			if item.Kind == conversation.KindToolResult && item.Output != nil && *item.Output == "DISPLAY_SENTINEL\n[exit 7]" {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Fatal("persisted session did not keep the complete canonical tool result")
	}
}

func TestRunTTYREPLStopsOnRendererWriterFailure(t *testing.T) {
	firstChunk := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTTYModelProbe(w, r) {
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"visible text \"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(firstChunk)
		<-release
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"trigger failure\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	prepareTTYAppEnv(t, server.URL)
	t.Setenv("GAX_NO_SESSION", "off")

	inputMaster, inputSlave := openTestPTY(t)
	defer inputMaster.Close()
	defer inputSlave.Close()
	outputMaster, outputSlave := openTestPTY(t)
	defer outputSlave.Close()
	capture := &ptyCapture{}
	go capture.read(outputMaster)
	var stderr bytes.Buffer
	finished := make(chan int, 1)
	go func() { finished <- Run(nil, inputSlave, true, outputSlave, &stderr) }()
	waitPTYOutput(t, capture, "> ", 2*time.Second)
	if _, err := inputMaster.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not send its first event")
	}
	waitPTYOutput(t, capture, "visible text", 2*time.Second)
	_ = outputSlave.Close()
	close(release)
	select {
	case code := <-finished:
		if code == 0 {
			t.Fatalf("REPL succeeded after terminal output failed: stdout=%q stderr=%q", capture.string(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("REPL did not stop after terminal output failed: stdout=%q stderr=%q", capture.string(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write output:") || requests.Load() != 1 {
		t.Fatalf("writer failure was not reported or a later request started: stderr=%q requests=%d", stderr.String(), requests.Load())
	}
	cwd, _ := os.Getwd()
	directory, err := session.Directory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read persisted session directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		items, _, loadErr := session.Load(filepath.Join(directory, entry.Name()))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, item := range items {
			if item.Text != nil && strings.Contains(*item.Text, "write output:") ||
				item.Output != nil && strings.Contains(*item.Output, "write output:") {
				t.Fatalf("terminal error leaked into session history: %+v", item)
			}
		}
	}
}

func prepareTTYAppEnv(t *testing.T, baseURL string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("GAX_OPENAI_BASE_URL", baseURL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_PROVIDER", "openai-compatible")
	t.Setenv("GAX_MODEL", "test-model")
	t.Setenv("GAX_NO_SESSION", "on")
}

func serveTTYModelProbe(w http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path != "/models" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
	return true
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open pseudo-terminal master: %v", err)
	}
	unlocked := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(masterFD), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlocked))); errno != 0 {
		_ = syscall.Close(masterFD)
		t.Fatalf("unlock pseudo-terminal: %v", errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(masterFD), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&number))); errno != 0 {
		_ = syscall.Close(masterFD)
		t.Fatalf("query pseudo-terminal number: %v", errno)
	}
	slaveFD, err := syscall.Open(fmt.Sprintf("/dev/pts/%d", number), syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = syscall.Close(masterFD)
		t.Fatalf("open pseudo-terminal slave: %v", err)
	}
	size := struct {
		Rows, Columns, XPixel, YPixel uint16
	}{Rows: 24, Columns: 80}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(slaveFD), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&size))); errno != 0 {
		_ = syscall.Close(masterFD)
		_ = syscall.Close(slaveFD)
		t.Fatalf("set pseudo-terminal size: %v", errno)
	}
	return os.NewFile(uintptr(masterFD), "pty-master"), os.NewFile(uintptr(slaveFD), "pty-slave")
}

type ptyCapture struct {
	mu     sync.Mutex
	output bytes.Buffer
}

func (capture *ptyCapture) read(reader io.Reader) {
	buffer := make([]byte, 4096)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			capture.mu.Lock()
			_, _ = capture.output.Write(buffer[:count])
			capture.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (capture *ptyCapture) string() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.output.String()
}

func waitPTYOutput(t *testing.T, capture *ptyCapture, text string, timeout time.Duration) {
	t.Helper()
	if text == "> " && terminalocale.HaveUTF8() {
		text = "❯ "
	}
	if !waitPTYOutputMaybe(capture, text, timeout) {
		t.Fatalf("PTY output did not contain %q within %s: %q", text, timeout, capture.string())
	}
}

func waitPTYOutputCount(t *testing.T, capture *ptyCapture, text string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output := capture.string()
		if strings.Count(output, text) >= count || strings.Count(stripPTYANSI(output), text) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("PTY output contained %q fewer than %d times: %q", text, count, capture.string())
}

func waitPTYEditorReadyCount(t *testing.T, capture *ptyCapture, count int, timeout time.Duration) {
	t.Helper()
	glyph := ">"
	if terminalocale.HaveUTF8() {
		glyph = "❯"
	}
	waitPTYOutputCount(t, capture, "\x1b[1m"+glyph+"\x1b[22m \x1b[J\r\x1b[2C\x1b[?2026l", count, timeout)
}

func waitPTYOutputMaybe(capture *ptyCapture, text string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output := capture.string()
		if strings.Contains(output, text) || strings.Contains(stripPTYANSI(output), text) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	output := capture.string()
	return strings.Contains(output, text) || strings.Contains(stripPTYANSI(output), text)
}

func stripPTYANSI(value string) string {
	var visible strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) {
				current := value[index]
				index++
				if current >= 0x40 && current <= 0x7e {
					break
				}
			}
			continue
		}
		visible.WriteByte(value[index])
		index++
	}
	return visible.String()
}

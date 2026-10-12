package live

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/conversation"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalvt "github.com/luank3/gax/internal/terminal/vtresolve"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

func TestTextAfterClearedStatusKeepsOneBlankLineAfterExternalPrompt(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	renderer.SyncExternalLine()
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(time.Now().Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"}); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	statusClear := strings.LastIndex(got, ansiEraseLine)
	if statusClear < 0 {
		t.Fatalf("status was not cleared before answer: %q", got)
	}
	if got, want := got[statusClear:], ansiEraseLine+"\nanswer"; got != want {
		t.Fatalf("answer after cleared status = %q, want %q", got, want)
	}
}

func TestRendererStreamsTextAndDropsInitialLineEndings(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "\r\n\n"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || renderer.HasRenderedAnswer() {
		t.Fatalf("leading line endings rendered: %q", output.String())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "hello" || !renderer.HasRenderedAnswer() {
		t.Fatalf("text was not rendered eagerly: output=%q answer=%v", output.String(), renderer.HasRenderedAnswer())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventDone}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "hello" {
		t.Fatalf("completion duplicated text: %q", output.String())
	}
}

func TestRendererInterruptSeparatesMarkerFromAnswer(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "partial answer"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "partial answer\n\n[interrupted]\n"; got != want {
		t.Fatalf("interrupted answer display = %q, want %q", got, want)
	}
}

func TestRendererDisplaysTaskNotesOutsideAssistantAnswer(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, true, false)
	if err := renderer.TaskNote("[task t1 finished (exit 0) after 53ms; 9B output pending]"); err != nil {
		t.Fatal(err)
	}
	want := ansiDim + "[task t1 finished (exit 0) after 53ms; 9B output pending]" + ansiReset + "\n"
	if got := output.String(); got != want {
		t.Fatalf("task note display = %q, want %q", got, want)
	}
	if renderer.HasRenderedAnswer() {
		t.Fatal("task note was counted as an assistant answer")
	}
}

func TestRendererLeavesMarkdownLiteralWhenDisabled(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	renderer.SetMarkdownEnabled(false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "**raw**\n## heading"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "**raw**\n## heading"; got != want {
		t.Fatalf("Markdown-disabled live output = %q, want literal %q", got, want)
	}
}

func TestRendererReasoningVisibilityAndBoundaries(t *testing.T) {
	t.Run("hidden", func(t *testing.T) {
		var output bytes.Buffer
		renderer := NewRenderer(&output, 0, false, false)
		_ = renderer.Begin()
		if err := renderer.Event(conversation.Event{Kind: conversation.EventReasoningDelta, Text: "secret"}); err != nil {
			t.Fatal(err)
		}
		if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "answer") {
			t.Fatalf("hidden reasoning leaked or answer missing: %q", output.String())
		}
	})
	t.Run("shown", func(t *testing.T) {
		var output bytes.Buffer
		renderer := NewRenderer(&output, 0, true, true)
		_ = renderer.Begin()
		if err := renderer.Event(conversation.Event{Kind: conversation.EventReasoningDelta, Text: "thought"}); err != nil {
			t.Fatal(err)
		}
		if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "\x1b[2m\x1b[3mthought") || !strings.Contains(output.String(), "answer") {
			t.Fatalf("reasoning style or answer boundary missing: %q", output.String())
		}
	})
}

func TestRendererRetryMarksOnlyVisibleAbandonedAttempt(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	_ = renderer.Begin()
	_ = renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "discard me"})
	if err := renderer.Event(conversation.Event{Kind: conversation.EventRetry, Attempt: 1, MaxAttempts: 3, DelayMS: 1200}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "keep me"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "[unexpected end]") != 1 || !strings.Contains(output.String(), "discard me") || !strings.Contains(output.String(), "keep me") {
		t.Fatalf("retry display state incorrect: %q", output.String())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventRetry}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "[unexpected end]") != 2 {
		t.Fatalf("second visible attempt was not marked: %q", output.String())
	}
	if got, want := output.String(), "discard me\n\n[unexpected end]\n\nkeep me\n\n[unexpected end]\n"; got != want {
		t.Fatalf("retry output ordering = %q, want %q", got, want)
	}
}

func TestRendererRetryWithoutVisibleAttemptAddsNoMarker(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	_ = renderer.Begin()
	if err := renderer.Event(conversation.Event{Kind: conversation.EventRetry}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[unexpected end]") {
		t.Fatalf("silent retry added an unexpected-end marker: %q", output.String())
	}
}

func TestRendererDelaysTableStatusUntilTick(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "| A |\n|---|\n| x |\n"}); err != nil {
		t.Fatal(err)
	}
	start := renderer.tableStarted
	if err := renderer.Tick(start.Add(1799 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "composing...") {
		t.Fatalf("table status appeared before its delay: %q", output.String())
	}
	if err := renderer.Tick(start.Add(1800 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "⠸ composing...") {
		t.Fatalf("animated table status did not appear after its delay: %q", output.String())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "| B |\n"}); err != nil {
		t.Fatal(err)
	}
	if !renderer.markdown.InTable() {
		t.Fatalf("table ended before the next streamed row; output=%q", output.String())
	}
	firstStatusCount := strings.Count(output.String(), "composing...")
	chunkAt := time.Now()
	renderer.tableStarted = chunkAt.Add(-tableStatusDelay)
	if err := renderer.Tick(chunkAt.Add(299 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(output.String(), "composing..."); got != firstStatusCount {
		t.Fatalf("table status reappeared before its show grace: count=%d output=%q", got, output.String())
	}
	if err := renderer.Tick(chunkAt.Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(output.String(), "composing..."); got != firstStatusCount+1 {
		t.Fatalf("table status did not return after its show grace: count=%d output=%q", got, output.String())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "| x |\nafter\n"}); err != nil {
		t.Fatal(err)
	}
	statusIndex := strings.Index(output.String(), "composing...")
	if statusIndex < 0 || !strings.Contains(output.String()[statusIndex:], "\r\x1b[2K") {
		t.Fatalf("resolved table did not clear its transient status row: %q", output.String())
	}
}

func TestRendererShowsAndAnimatesDelayedWaitingSpinner(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := renderer.statusStarted
	if err := renderer.Tick(start.Add(299 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "working...") {
		t.Fatalf("waiting indicator appeared before its grace period: %q", output.String())
	}
	if err := renderer.Tick(start.Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "⠸ working...") {
		t.Fatalf("waiting indicator did not show the expected spinner frame: %q", output.String())
	}
	if err := renderer.Tick(start.Add(380 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "⠼ working...") || strings.Count(output.String(), "working...") != 2 {
		t.Fatalf("waiting spinner did not repaint on the next frame: %q", output.String())
	}
}

func TestRendererAddsElapsedTimeToLongRunningStatus(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := renderer.statusStarted
	if err := renderer.Tick(start.Add(29999 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "30s ·") {
		t.Fatalf("elapsed time appeared before thirty seconds: %q", output.String())
	}
	if err := renderer.Tick(start.Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "30s · working...") {
		t.Fatalf("long-running status omitted its elapsed time: %q", output.String())
	}
}

func TestRendererKeepsStatusWithinTerminalWidth(t *testing.T) {
	var output bytes.Buffer
	const width = 14
	renderer := NewRenderer(&output, width, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := renderer.statusStarted
	if err := renderer.PausePending(start); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(start.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	// The row's tail is erased after the paint and occupies no cells on screen.
	shown := string(terminalvt.Resolve(output.Bytes()))
	if cells := terminalwidth.Cells(strings.TrimSuffix(shown, "\n")); cells > width-1 {
		t.Fatalf("status occupies %d terminal cells at width %d: %q", cells, width, output.String())
	}
}

// A frame rewrites the row from column zero and erases only what follows the label; erasing the
// row first would show a blank indicator for the length of a write. A label that replaces a
// visible one repaints the same way, so swapping labels never blinks the row off and on.
func TestRendererRepaintsStatusInPlaceWithoutErasingFirst(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	start := renderer.statusStarted
	if err := renderer.Tick(start.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(start.Add(statusShowGrace + spinnerInterval)); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventReasoningDelta, Text: "cot"}); err != nil {
		t.Fatal(err)
	}
	swapped := renderer.statusStarted
	if err := renderer.Tick(swapped.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	want := spinnerFrames[3] + " working..." + ansiEraseTail +
		"\r" + spinnerFrames[4] + " working..." + ansiEraseTail +
		"\r" + spinnerFrames[3] + " thinking..." + ansiEraseTail
	if got != want {
		t.Fatalf("status frames = %q, want %q", got, want)
	}
	if strings.Contains(got, ansiEraseLine) {
		t.Fatalf("status frame erased the row before painting it: %q", got)
	}
}

func TestRendererDisplaysAndClearsCallerProvidedBusyLabel(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	renderer.SetStatusLabel("fetching models...")
	start := renderer.statusStarted
	if err := renderer.Tick(start.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "fetching models...") {
		t.Fatalf("custom busy label was not shown: %q", output.String())
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	statusIndex := strings.Index(output.String(), "fetching models...")
	if statusIndex < 0 || !strings.Contains(output.String()[statusIndex:], "\r\x1b[2K") {
		t.Fatalf("closing renderer did not erase the busy row: %q", output.String())
	}
}

func TestRendererDoesNotReviveWaitingStatusDuringTextOutput(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := renderer.statusStarted
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, "working...") {
		t.Fatalf("waiting status was drawn over streamed text: %q", got)
	}
}

func TestRendererDoesNotReviveWaitingStatusAfterStreamError(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := renderer.statusStarted
	if err := renderer.Event(conversation.Event{Kind: conversation.EventError, ErrorMessage: "request failed"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, "working...") {
		t.Fatalf("waiting status was drawn after the stream error: %q", got)
	}
}

func TestRendererShowsDelayedPauseStatusAndClearsItAtBoundary(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := renderer.PausePending(start); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(start.Add(statusShowGrace - time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "pausing...") {
		t.Fatalf("pause status appeared before its delay: %q", output.String())
	}
	if err := renderer.Tick(start.Add(statusShowGrace + time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "pausing... (esc again to interrupt)") {
		t.Fatalf("pause status was not shown: %q", output.String())
	}
	if err := renderer.Boundary(); err != nil {
		t.Fatal(err)
	}
	statusIndex := strings.Index(output.String(), "pausing...")
	if statusIndex < 0 || !strings.Contains(output.String()[statusIndex:], "\r\x1b[2K") {
		t.Fatalf("turn boundary did not clear pause status: %q", output.String())
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRendererTracksTableStatusAfterEagerProse(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "Intro\n| A |\n|---|\n"}); err != nil {
		t.Fatal(err)
	}
	start := renderer.tableStarted
	if got := output.String(); got != "Intro\n" {
		t.Fatalf("prose before buffered table = %q, want eager prose", got)
	}
	if err := renderer.Tick(start.Add(tableStatusDelay + statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "composing...") {
		t.Fatalf("table after eager prose did not start composition status: %q", output.String())
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "| x |\nAfter\n"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\r\x1b[2K") || !strings.Contains(output.String(), "After") {
		t.Fatalf("completed table did not clear status and render its following block: %q", output.String())
	}
}

func TestRendererToolHeaderUsesSuffixAwareReflow(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 20, false, false)
	presentation := coretools.ToolPresentation{
		Argument:          strings.Repeat("a", 30),
		Extra:             ":10000-10019",
		PreviewMode:       coretools.PreviewCollapsed,
		CollapsedArgument: strings.Repeat("a", 30),
	}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, presentation); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(output.String(), "\n")
	if !strings.HasSuffix(line, "...:1000...") || terminalwidth.Cells(line) > 19 {
		t.Fatalf("collapsed tool header did not preserve its capped suffix: %q cells=%d", line, terminalwidth.Cells(line))
	}
}

func TestRendererToolHeaderUsesConfiguredChromeRole(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())

	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, true, false)
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, coretools.ToolPresentation{Argument: "file.go"}); err != nil {
		t.Fatal(err)
	}
	want := terminaltheme.Open(terminaltheme.Chrome) + "[read]" + ansiReset
	if !strings.Contains(output.String(), want) {
		t.Fatalf("tool header did not use configured chrome role %q: %q", want, output.String())
	}
}

func TestRendererErrorsUseConfiguredErrorRole(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())

	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, true, false)
	if err := renderer.Event(conversation.Event{Kind: conversation.EventError, ErrorMessage: "bad request"}); err != nil {
		t.Fatal(err)
	}
	want := terminaltheme.Open(terminaltheme.Error) + "[error: bad request]" + ansiReset + "\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("error output did not use configured error role %q: %q", want, output.String())
	}
}

func TestRendererRawToolArgumentsStayOneDimTruncatedRow(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 14, true, false)
	if err := renderer.ToolStart(conversation.Item{
		ToolName:  conversation.String("custom"),
		Arguments: conversation.String(`{"long":"argument payload"}`),
	}, coretools.ToolPresentation{}); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, ansiDim) || !strings.Contains(got, "...") {
		t.Fatalf("raw JSON argument was wrapped or not dim-truncated: %q", got)
	}
}

func TestRendererCoalescesAdjacentCollapsedReadCalls(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	first := coretools.ToolPresentation{Argument: "/repo/src/a.txt", HasArgument: true, PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: "a.txt"}
	second := coretools.ToolPresentation{Argument: "/repo/src/b.txt", HasArgument: true, Extra: ":2-3", PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: "b.txt"}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, first); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: first}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, second); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: second}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[read] a.txt, b.txt:2-3\n"; got != want {
		t.Fatalf("collapsed read cluster = %q, want %q", got, want)
	}
}

func TestRendererKeepsCollapsedReadClusterAcrossProviderBoundaries(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{Argument: "a.txt", HasArgument: true, PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: "a.txt"}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, presentation); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: presentation}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventDone}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventToolCallStart}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, coretools.ToolPresentation{
		Argument: "b.txt", HasArgument: true, PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: "b.txt",
	}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[read] a.txt, b.txt\n"; got != want {
		t.Fatalf("provider boundary split the collapsed cluster: %q, want %q", got, want)
	}
}

func TestRendererParksStatusBelowOpenCollapsedReadRow(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	read := func(name string) {
		t.Helper()
		presentation := coretools.ToolPresentation{Argument: name, HasArgument: true, PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: name}
		if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, presentation); err != nil {
			t.Fatal(err)
		}
		if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: presentation}); err != nil {
			t.Fatal(err)
		}
	}
	read("a.txt")
	// The provider round trip between two reads paints the delayed working indicator.
	if err := renderer.SetStatusLabel("working..."); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(renderer.statusStarted.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	read("b.txt")
	if err := renderer.SetStatusLabel("working..."); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Tick(renderer.statusStarted.Add(statusShowGrace)); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	for _, frame := range spinnerFrames {
		got = strings.ReplaceAll(got, frame, "X")
	}
	// A parked indicator sits below the open row and clearing it climbs back to the row's last
	// column, so the next read still coalesces onto that row instead of a fresh line. Each frame
	// erases the row's tail after painting it, never before.
	want := "[read] a.txt\n\nX working..." + ansiEraseTail +
		"\r\x1b[2K\x1b[2A\x1b[13G, b.txt" +
		"\n\nX working..." + ansiEraseTail +
		"\r\x1b[2K\x1b[2A\x1b[20G\n"
	if got != want {
		t.Fatalf("parked status output = %q, want %q", got, want)
	}
}

func TestRendererStartsNewCollapsedReadRowWhenClusterWouldOverflow(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 20, false, false)
	_ = renderer.Begin()
	for range 2 {
		presentation := coretools.ToolPresentation{
			Argument:          strings.Repeat("a", 30),
			HasArgument:       true,
			PreviewMode:       coretools.PreviewCollapsed,
			CollapsedArgument: strings.Repeat("a", 30),
		}
		if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("read")}, presentation); err != nil {
			t.Fatal(err)
		}
		if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: presentation}); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Count(got, "[read]") != 2 || strings.Contains(got, ", ") || strings.Count(got, "\n") != 2 {
		t.Fatalf("overflowing collapsed calls were not split into bounded rows: %q", got)
	}
}

func TestRendererKeepsCollapsedNonReadCallsInTheSameCluster(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	for _, call := range []struct {
		name string
		arg  string
	}{
		{name: "bash", arg: "find . -type f"},
		{name: "read", arg: "a.txt"},
	} {
		presentation := coretools.ToolPresentation{Argument: call.arg, HasArgument: true, PreviewMode: coretools.PreviewCollapsed, CollapsedArgument: call.arg}
		if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String(call.name)}, presentation); err != nil {
			t.Fatal(err)
		}
		if err := renderer.ToolResult(coretools.ToolExecution{Result: conversation.Item{Kind: conversation.KindToolResult}, Presentation: presentation}); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[bash] find . -type f\n[read] a.txt\n"; got != want {
		t.Fatalf("collapsed tool cluster inserted a separator: %q, want %q", got, want)
	}
}

func TestRendererToolDisplayDoesNotDuplicateStreamedSummary(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	call := conversation.Item{ToolName: conversation.String("bash"), Arguments: conversation.String(`{"command":"printf live"}`)}
	presentation := coretools.ToolPresentation{Argument: "printf live", PreviewMode: coretools.PreviewHeadTail, HeaderRows: 3}
	if err := renderer.ToolStart(call, presentation); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolOutput([]byte("live output\n")); err != nil {
		t.Fatal(err)
	}
	canonical := conversation.String("live output\n[exit 0]")
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: canonical},
		Presentation: presentation, OutputSummarizesDisplay: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "live output") != 1 || strings.Contains(output.String(), "[exit 0]") {
		t.Fatalf("streamed tool output was duplicated with canonical summary: %q", output.String())
	}
	if got, want := output.String(), "[bash] printf live\n┌ live output\r›\n"; got != want {
		t.Fatalf("tool output ordering = %q, want %q", got, want)
	}
}

func TestRendererShowsSummarizedDiffOutputWhenStreamHasNoVisibleRows(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	presentation := coretools.ToolPresentation{
		Argument: "notes.txt", OutputStyle: coretools.OutputUnifiedDiff, PreviewMode: coretools.PreviewHead,
	}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("write")}, presentation); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolOutput([]byte("   \n")); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("created notes.txt (0 lines, 0 bytes)")},
		Presentation: presentation, OutputSummarizesDisplay: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "created notes.txt") {
		t.Fatalf("empty streamed preview hid its canonical summary: %q", output.String())
	}
}

func TestRendererKeepsStreamedWriteContentInPlainPreviewMode(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{Argument: "notes.txt", OutputStyle: coretools.OutputUnifiedDiff, PreviewMode: coretools.PreviewHead}
	if err := renderer.ToolStart(conversation.Item{ToolName: conversation.String("write")}, presentation); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolOutput([]byte("--- user content\n+++ still content\n")); err != nil {
		t.Fatal(err)
	}
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("created notes.txt")},
		Presentation: presentation, OutputSummarizesDisplay: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "--- user content") || !strings.Contains(output.String(), "+++ still content") {
		t.Fatalf("streamed file content was misclassified as a unified diff: %q", output.String())
	}
}

func TestRendererToolResultUsesCanonicalOutputWhenNoLiveBytes(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{Argument: "file.go", OutputStyle: coretools.OutputUnifiedDiff, PreviewMode: coretools.PreviewHead}
	_ = renderer.ToolStart(conversation.Item{ToolName: conversation.String("write")}, presentation)
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("--- a/file.go\n+++ b/file.go\n@@ -0,0 +1 @@\n+new\n")},
		Presentation: presentation,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "+ new") && !strings.Contains(output.String(), "+new") {
		t.Fatalf("non-streamed tool result missing from display: %q", output.String())
	}
	if strings.Contains(output.String(), "a/file.go") || strings.Contains(output.String(), "b/file.go") {
		t.Fatalf("diff file headers repeated after the tool header: %q", output.String())
	}
	if got, want := output.String(), "[write] file.go\n┌ @@ -0,0 +1 @@\n│ +new\r└\n"; got != want {
		t.Fatalf("tool preview ordering = %q, want %q", got, want)
	}
}

func TestRendererToolResultShowsNoChangesMarkerForEmptyDiff(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{Argument: "file.go", OutputStyle: coretools.OutputUnifiedDiff, PreviewMode: coretools.PreviewHead}
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("")},
		Presentation: presentation,
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[?] file.go\n› (no changes)\n"; got != want {
		t.Fatalf("empty diff marker = %q, want %q", got, want)
	}
}

func TestRendererHidesOnlyAValidCanonicalTailLength(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{PreviewMode: coretools.PreviewHead}
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("keep")},
		Presentation: presentation,
		HiddenTail:   99,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "keep") {
		t.Fatalf("oversized hidden-tail count removed visible output: %q", output.String())
	}
}

func TestRendererDoesNotMistakeAHiddenDiffTailForNoChanges(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 80, false, false)
	_ = renderer.Begin()
	presentation := coretools.ToolPresentation{OutputStyle: coretools.OutputUnifiedDiff, PreviewMode: coretools.PreviewHead}
	if err := renderer.ToolResult(coretools.ToolExecution{
		Result:       conversation.Item{Kind: conversation.KindToolResult, Output: conversation.String("model-only")},
		Presentation: presentation,
		HiddenTail:   len("model-only"),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "no changes") {
		t.Fatalf("hidden diff tail produced a false no-changes marker: %q", output.String())
	}
}

func TestRendererLatchesWriterFailure(t *testing.T) {
	want := errors.New("terminal failed")
	writer := &errorWriter{err: want}
	renderer := NewRenderer(writer, 0, false, false)
	if err := renderer.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "answer"}); !errors.Is(err, want) {
		t.Fatalf("first writer error = %v, want %v", err, want)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "later"}); !errors.Is(err, want) {
		t.Fatalf("latched writer error = %v, want %v", err, want)
	}
	if writer.calls != 1 {
		t.Fatalf("writer was called %d times after its first failure", writer.calls)
	}
}

func TestRendererStreamErrorAndClose(t *testing.T) {
	var output bytes.Buffer
	renderer := NewRenderer(&output, 0, false, false)
	_ = renderer.Begin()
	if err := renderer.Event(conversation.Event{Kind: conversation.EventTextDelta, Text: "partial"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Event(conversation.Event{Kind: conversation.EventError, ErrorMessage: "provider failed"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "partial") || !strings.Contains(output.String(), "[error: provider failed]") {
		t.Fatalf("stream error did not close the partial block and report its error: %q", output.String())
	}
	if got, want := output.String(), "partial\n\n[error: provider failed]\n"; got != want {
		t.Fatalf("stream error output ordering = %q, want %q", got, want)
	}
}

type errorWriter struct {
	err   error
	calls int
}

func (writer *errorWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, writer.err
}

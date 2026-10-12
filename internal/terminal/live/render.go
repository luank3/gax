// Package live renders provider events and tool output as they arrive in the interactive terminal.
package live

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/luank3/gax/internal/conversation"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	"github.com/luank3/gax/internal/terminal/markdown"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

const (
	statusShowGrace  = 300 * time.Millisecond
	tableStatusDelay = 1500 * time.Millisecond
	spinnerInterval  = 80 * time.Millisecond
	ansiDim          = "\x1b[2m"
	ansiItalic       = "\x1b[3m"
	ansiBold         = "\x1b[1m"
	ansiBoldOff      = "\x1b[22m"
	ansiReset        = "\x1b[0m"
	ansiEraseLine    = "\r\x1b[2K"
	ansiEraseTail    = "\x1b[K"
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type renderMode uint8

const (
	modeIdle renderMode = iota
	modeText
	modeReasoning
	modeTool
)

// Renderer owns one live answer stream and its display-only tool presentation state.
type Renderer struct {
	writer        io.Writer
	width         int
	styled        bool
	markdownOn    bool
	showReasoning bool
	markdown      *markdown.Stream

	mode               renderMode
	err                error
	closed             bool
	outputStarted      bool
	lineNewlines       int
	answerStarted      bool
	answerRendered     bool
	attemptRendered    bool
	tableStarted       time.Time
	tableStatusReset   time.Time
	statusLabel        string
	statusStarted      time.Time
	statusVisible      bool
	statusLineNewlines int
	statusFrame        int
	statusPainted      string
	statusParkedRows   int
	statusParkedColumn int
	turnStarted        time.Time
	retryDeadline      time.Time
	retryAttempt       int
	retryMaxAttempts   int
	toolPresentation   coretools.ToolPresentation
	toolName           string
	toolOutputBytes    int
	toolPreview        *toolPreviewRenderer
	collapsedCluster   bool
	collapsedLineOpen  bool
	collapsedLastTool  string
	collapsedCells     int
}

// NewRenderer creates a terminal renderer. Widths <= 0 disable Markdown wrapping.
func NewRenderer(writer io.Writer, width int, styled, showReasoning bool) *Renderer {
	renderer := &Renderer{writer: writer, width: width, styled: styled, markdownOn: true, showReasoning: showReasoning}
	renderer.markdown = markdown.NewStream(width, styled, renderer.write)
	return renderer
}

// SetMarkdownEnabled keeps raw-mode TTY output literal while preserving the same live event and
// tool lifecycle. The default is enabled; callers should set this before the first turn.
func (renderer *Renderer) SetMarkdownEnabled(enabled bool) {
	renderer.markdownOn = enabled
}

// SetStatusLabel changes the delayed animated status shown by Tick. An empty label disables it.
func (renderer *Renderer) SetStatusLabel(label string) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	renderer.setStatus(label)
	return renderer.failure()
}

// SyncExternalLine records a complete line already written to the same terminal. The REPL input
// editor commits submitted prompts before the provider renderer is created for that turn.
func (renderer *Renderer) SyncExternalLine() {
	if renderer == nil || renderer.closed || renderer.failure() != nil {
		return
	}
	renderer.outputStarted = true
	renderer.lineNewlines = 1
}

// TaskNote displays a completed-task notice as dim terminal text outside the assistant answer.
func (renderer *Renderer) TaskNote(note string) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if note == "" {
		return nil
	}
	if err := renderer.Boundary(); err != nil {
		return err
	}
	if err := renderer.openBlock(); err != nil {
		return err
	}
	if renderer.styled {
		if err := renderer.write(ansiDim); err != nil {
			return err
		}
	}
	if err := renderer.write(note); err != nil {
		return err
	}
	if renderer.styled {
		if err := renderer.write(ansiReset); err != nil {
			return err
		}
	}
	if err := renderer.write("\n"); err != nil {
		return err
	}
	renderer.mode = modeIdle
	return nil
}

// UserMessage renders a previously submitted prompt with the editor's safe glyph and wrapping
// rules. It keeps the renderer's block accounting in sync with the rows it writes.
func (renderer *Renderer) UserMessage(text string) error {
	if err := renderer.Boundary(); err != nil {
		return err
	}
	if err := renderer.openBlock(); err != nil {
		return err
	}
	accentOpen, accentClose := "", ""
	if renderer.styled {
		accentOpen = terminaltheme.Open(terminaltheme.Accent)
		accentClose = terminaltheme.Close(terminaltheme.Accent)
	}
	if err := renderer.write(accentOpen + "▌ "); err != nil {
		return err
	}
	core := terminalinput.NewCore()
	core.SetBuffer(text)
	columns := renderer.width
	if columns <= 0 {
		columns = 80
	}
	core.Render(2, 2, columns, func(event terminalinput.RenderEvent) {
		if renderer.err != nil {
			return
		}
		if event.Kind == terminalinput.RenderGlyph {
			renderer.err = renderer.write(event.Bytes)
			return
		}
		renderer.err = renderer.write(accentClose + "\x1b[K\r\n" + accentOpen + "▌ ")
	})
	if err := renderer.failure(); err != nil {
		return err
	}
	if err := renderer.write(accentClose + "\x1b[K\r\n"); err != nil {
		return err
	}
	renderer.mode = modeIdle
	return nil
}

// StoredText writes a complete assistant text item without applying live-stream first-chunk
// trimming. Adjacent text items stay in one Markdown block, as they did in the original stream.
func (renderer *Renderer) StoredText(text string) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if text == "" {
		return nil
	}
	if renderer.mode == modeReasoning {
		if err := renderer.endReasoning(); err != nil {
			return err
		}
	}
	if renderer.mode != modeText {
		if err := renderer.openBlock(); err != nil {
			return err
		}
		renderer.mode = modeText
	}
	renderer.answerStarted = true
	renderer.answerRendered = true
	renderer.attemptRendered = true
	if renderer.markdownOn {
		return renderer.markdown.Feed(text)
	}
	return renderer.write(text)
}

// StoredReasoning adds a visible or hidden history seam while preserving collapsed tool clusters.
func (renderer *Renderer) StoredReasoning(text string) error {
	if err := renderer.Event(conversation.Event{Kind: conversation.EventReasoningItem}); err != nil {
		return err
	}
	if text == "" {
		return nil
	}
	return renderer.Event(conversation.Event{Kind: conversation.EventReasoningDelta, Text: text})
}

// Begin starts a new provider turn while retaining the writer and display configuration.
func (renderer *Renderer) Begin() error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if renderer.mode == modeReasoning {
		if err := renderer.endReasoning(); err != nil {
			return err
		}
	}
	if err := renderer.markdown.Reset(); err != nil {
		return renderer.fail(err)
	}
	renderer.mode = modeIdle
	renderer.answerStarted = false
	renderer.answerRendered = false
	renderer.attemptRendered = false
	renderer.tableStarted = time.Time{}
	renderer.tableStatusReset = time.Time{}
	renderer.statusLabel = "working..."
	renderer.statusStarted = time.Now()
	renderer.turnStarted = renderer.statusStarted
	renderer.statusFrame = -1
	renderer.statusPainted = ""
	renderer.retryDeadline = time.Time{}
	renderer.retryAttempt = 0
	renderer.retryMaxAttempts = 0
	return nil
}

// Event consumes one provider-independent stream event.
func (renderer *Renderer) Event(event conversation.Event) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if event.Kind != conversation.EventRetry && !renderer.retryDeadline.IsZero() {
		renderer.retryDeadline = time.Time{}
		renderer.retryAttempt = 0
		renderer.retryMaxAttempts = 0
		if renderer.statusLabel == "" || strings.HasPrefix(renderer.statusLabel, "retrying") {
			renderer.setStatus("working...")
		}
	}
	switch event.Kind {
	case conversation.EventTextDelta:
		text := event.Text
		if !renderer.answerStarted {
			text = strings.TrimLeft(text, "\r\n")
		}
		if text == "" {
			return nil
		}
		tableStatusWasVisible := renderer.statusVisible && renderer.statusPainted == "composing..."
		if err := renderer.clearStatus(); err != nil {
			return err
		}
		if renderer.mode == modeReasoning {
			if err := renderer.endReasoning(); err != nil {
				return err
			}
		}
		if renderer.mode != modeText {
			if err := renderer.openBlock(); err != nil {
				return err
			}
			renderer.mode = modeText
		}
		renderer.answerStarted = true
		renderer.answerRendered = true
		renderer.attemptRendered = true
		if renderer.markdownOn {
			if err := renderer.markdown.Feed(text); err != nil {
				return renderer.fail(err)
			}
		} else if err := renderer.write(text); err != nil {
			return err
		}
		if renderer.markdownOn && renderer.markdown.InTable() {
			if renderer.tableStarted.IsZero() {
				renderer.tableStarted = time.Now()
			}
			if tableStatusWasVisible {
				renderer.tableStatusReset = time.Now()
			}
		} else {
			renderer.tableStarted = time.Time{}
			renderer.tableStatusReset = time.Time{}
		}
		return nil
	case conversation.EventReasoningDelta:
		if !renderer.showReasoning || event.Text == "" {
			renderer.setStatus("thinking...")
			return nil
		}
		if err := renderer.clearStatus(); err != nil {
			return err
		}
		if renderer.mode == modeText {
			if err := renderer.markdown.Boundary(); err != nil {
				return renderer.fail(err)
			}
			renderer.mode = modeIdle
		}
		if renderer.mode != modeReasoning {
			if err := renderer.openBlock(); err != nil {
				return err
			}
			if renderer.styled {
				if err := renderer.write(ansiDim + ansiItalic); err != nil {
					return err
				}
			}
			renderer.mode = modeReasoning
		}
		renderer.attemptRendered = true
		if err := renderer.write(event.Text); err != nil {
			return err
		}
		return nil
	case conversation.EventToolCallStart:
		return renderer.streamBoundary()
	case conversation.EventToolCallEnd:
		renderer.setStatus("working...")
		return nil
	case conversation.EventToolCallDelta:
		return nil
	case conversation.EventReasoningItem:
		renderer.setStatus("working...")
		return renderer.streamBoundary()
	case conversation.EventRetry:
		if err := renderer.Retry(); err != nil {
			return err
		}
		delay := time.Duration(event.DelayMS) * time.Millisecond
		renderer.retryDeadline = time.Now().Add(delay)
		renderer.retryAttempt = event.Attempt + 1
		renderer.retryMaxAttempts = event.MaxAttempts
		renderer.setStatus(renderer.retryLabel(time.Now()))
		return nil
	case conversation.EventProgress:
		uncachedTotal := event.Total - event.Cache
		if uncachedTotal <= 0 {
			return nil
		}
		uncachedDone := event.Processed - event.Cache
		if uncachedDone < 0 {
			uncachedDone = 0
		}
		percent := uncachedDone * 100 / uncachedTotal
		if percent > 100 {
			percent = 100
		}
		renderer.setStatus(fmt.Sprintf("processing... %d%%", percent))
		return nil
	case conversation.EventDone:
		renderer.tableStarted = time.Time{}
		renderer.tableStatusReset = time.Time{}
		renderer.setStatus("")
		return renderer.streamBoundary()
	case conversation.EventError:
		renderer.tableStarted = time.Time{}
		renderer.tableStatusReset = time.Time{}
		renderer.setStatus("")
		if err := renderer.Boundary(); err != nil {
			return err
		}
		if event.ErrorMessage == "" {
			return nil
		}
		if err := renderer.openBlock(); err != nil {
			return err
		}
		if renderer.styled {
			if err := renderer.write(terminaltheme.Open(terminaltheme.Error)); err != nil {
				return err
			}
		}
		if err := renderer.write("[error: " + event.ErrorMessage + "]"); err != nil {
			return err
		}
		if renderer.styled {
			if err := renderer.write(ansiReset); err != nil {
				return err
			}
		}
		return renderer.write("\n")
	default:
		return nil
	}
}

// Boundary ends the current Markdown or reasoning block and resets its parser state.
func (renderer *Renderer) Boundary() error {
	return renderer.boundary(false)
}

// Separate closes the current display block and leaves one blank line after committed output.
func (renderer *Renderer) Separate() error {
	if err := renderer.Boundary(); err != nil {
		return err
	}
	return renderer.openBlock()
}

// Interrupt closes the active answer and draws the marker for a hard-interrupted user turn.
func (renderer *Renderer) Interrupt() error {
	if err := renderer.Boundary(); err != nil {
		return err
	}
	if err := renderer.openBlock(); err != nil {
		return err
	}
	if renderer.styled {
		if err := renderer.write(ansiDim); err != nil {
			return err
		}
	}
	if err := renderer.write("[interrupted]"); err != nil {
		return err
	}
	if renderer.styled {
		if err := renderer.write(ansiReset); err != nil {
			return err
		}
	}
	return renderer.write("\n")
}

func (renderer *Renderer) streamBoundary() error {
	return renderer.boundary(true)
}

func (renderer *Renderer) boundary(preserveCollapsed bool) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if !preserveCollapsed {
		if err := renderer.closeCollapsedCluster(); err != nil {
			return err
		}
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	renderer.tableStarted = time.Time{}
	renderer.tableStatusReset = time.Time{}
	if renderer.mode == modeReasoning {
		return renderer.endReasoning()
	}
	if renderer.mode == modeText {
		if err := renderer.markdown.Boundary(); err != nil {
			return renderer.fail(err)
		}
		renderer.mode = modeIdle
	}
	return nil
}

// Retry settles the visible attempt, marks it if necessary, and discards parser state.
func (renderer *Renderer) Retry() error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	if renderer.mode == modeReasoning {
		if err := renderer.endReasoning(); err != nil {
			return err
		}
	} else if renderer.mode == modeText {
		if err := renderer.markdown.Boundary(); err != nil {
			return renderer.fail(err)
		}
		renderer.mode = modeIdle
	}
	if renderer.attemptRendered {
		if err := renderer.openBlock(); err != nil {
			return err
		}
		marker := "[unexpected end]"
		if renderer.styled {
			marker = ansiDim + marker + ansiReset
		}
		if err := renderer.write(marker + "\n"); err != nil {
			return err
		}
	}
	if err := renderer.markdown.Reset(); err != nil {
		return renderer.fail(err)
	}
	renderer.answerStarted = false
	renderer.answerRendered = false
	renderer.attemptRendered = false
	renderer.tableStarted = time.Time{}
	renderer.tableStatusReset = time.Time{}
	renderer.mode = modeIdle
	return nil
}

// ToolStart writes the display-only header for an assembled tool call.
func (renderer *Renderer) ToolStart(call conversation.Item, presentation coretools.ToolPresentation) error {
	name := "?"
	if call.ToolName != nil && *call.ToolName != "" {
		name = *call.ToolName
	}
	if presentation.PreviewMode == coretools.PreviewCollapsed {
		return renderer.startCollapsedTool(call, presentation, name)
	}
	if err := renderer.Boundary(); err != nil {
		return err
	}
	if err := renderer.openBlock(); err != nil {
		return err
	}
	argument := presentation.Argument
	rawArgument := presentation.RawArgument
	hasArgument := presentation.HasArgument || argument != "" || rawArgument
	if !hasArgument && call.Arguments != nil && *call.Arguments != "" {
		argument = *call.Arguments
		rawArgument = true
		hasArgument = true
	}
	argument = terminalwidth.FlattenForDisplay(argument)
	extra := terminalwidth.FlattenForDisplay(presentation.Extra)
	width := renderer.width
	if width <= 0 {
		width = 100
	}
	firstRowCells := max(8, width-(terminalwidth.Cells(name)+3))
	otherRowCells := max(8, width)
	extraCells := 0
	if hasArgument && !rawArgument && extra != "" {
		maxExtra := min(20, firstRowCells-4)
		extra = terminalwidth.TruncateForDisplay(extra, maxExtra)
		extraCells = terminalwidth.Cells(extra)
	} else {
		extra = ""
	}
	rows := 1
	if !rawArgument && presentation.HeaderRows > 0 {
		rows = presentation.HeaderRows
	}
	layout := ""
	if hasArgument {
		layout = terminalwidth.ReflowForDisplay(argument, firstRowCells, otherRowCells, rows, extraCells)
	}
	var line strings.Builder
	if renderer.styled {
		line.WriteString(terminaltheme.Open(terminaltheme.Chrome))
	}
	line.WriteByte('[')
	line.WriteString(name)
	line.WriteByte(']')
	if renderer.styled {
		line.WriteString(ansiReset)
	}
	if hasArgument {
		line.WriteByte(' ')
		if renderer.styled {
			if rawArgument {
				line.WriteString(ansiDim)
			} else {
				line.WriteString(ansiBold)
			}
		}
		line.WriteString(layout)
		if renderer.styled {
			if rawArgument {
				line.WriteString(ansiReset)
			} else {
				line.WriteString(ansiBoldOff)
			}
		}
	}
	if extra != "" {
		if renderer.styled {
			line.WriteString(ansiDim)
		}
		line.WriteString(extra)
		if renderer.styled {
			line.WriteString(ansiReset)
		}
	}
	if err := renderer.write(line.String() + "\n"); err != nil {
		return err
	}
	renderer.setToolHeaderState(presentation, name, newToolPreviewRenderer(presentation.PreviewMode, presentation.OutputStyle, renderer.width, renderer.styled))
	return nil
}

func (renderer *Renderer) startCollapsedTool(call conversation.Item, presentation coretools.ToolPresentation, name string) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	if !renderer.collapsedCluster {
		if err := renderer.Boundary(); err != nil {
			return err
		}
		if err := renderer.openBlock(); err != nil {
			return err
		}
		renderer.collapsedCluster = true
	}

	argument := presentation.Argument
	rawArgument := presentation.RawArgument
	hasArgument := presentation.HasArgument || argument != "" || rawArgument
	if presentation.CollapsedArgument != "" && !rawArgument {
		argument = presentation.CollapsedArgument
	}
	if !hasArgument && call.Arguments != nil && *call.Arguments != "" {
		argument = *call.Arguments
		rawArgument = true
		hasArgument = true
	}
	argument = terminalwidth.FlattenForDisplay(argument)
	extra := ""
	if hasArgument && !rawArgument {
		extra = terminalwidth.FlattenForDisplay(presentation.Extra)
	}
	width := renderer.width
	if width <= 0 {
		width = 100
	}
	tagCells := terminalwidth.Cells(name) + 3
	rowCells := max(8, width-tagCells-1)
	extraCells := 0
	if extra != "" {
		extra = terminalwidth.TruncateForDisplay(extra, min(20, rowCells-4))
		extraCells = terminalwidth.Cells(extra)
	}
	layout := ""
	if hasArgument {
		layout = terminalwidth.ReflowForDisplay(argument, rowCells, rowCells, 1, extraCells)
	}
	var header strings.Builder
	if renderer.styled {
		header.WriteString(terminaltheme.Open(terminaltheme.ChromeDim))
	}
	header.WriteByte('[')
	header.WriteString(name)
	header.WriteByte(']')
	if renderer.styled {
		header.WriteString(terminaltheme.Close(terminaltheme.ChromeDim) + ansiDim)
	}
	header.WriteByte(' ')
	header.WriteString(layout)
	if extra != "" {
		header.WriteString(extra)
	}
	if renderer.styled {
		header.WriteString(ansiReset)
	}
	headerCells := tagCells + terminalwidth.Cells(layout) + extraCells
	appendCells := 2 + terminalwidth.Cells(argument) + extraCells

	if renderer.collapsedLineOpen {
		canCoalesce := renderer.collapsedLastTool == "read" && name == "read"
		if canCoalesce && renderer.collapsedCells+appendCells <= width-1 {
			var suffix strings.Builder
			suffix.WriteString(", ")
			if hasArgument {
				suffix.WriteString(argument)
			}
			if extra != "" {
				suffix.WriteString(extra)
			}
			if renderer.styled {
				if err := renderer.write(ansiDim + suffix.String() + ansiReset); err != nil {
					return err
				}
			} else if err := renderer.write(suffix.String()); err != nil {
				return err
			}
			renderer.collapsedCells += appendCells
			renderer.setToolHeaderState(presentation, name, nil)
			return nil
		}
		if err := renderer.write("\n"); err != nil {
			return err
		}
		renderer.collapsedLineOpen = false
	}
	if err := renderer.write(header.String()); err != nil {
		return err
	}
	renderer.collapsedCells = headerCells
	renderer.collapsedLastTool = name
	renderer.collapsedLineOpen = name == "read"
	if !renderer.collapsedLineOpen {
		if err := renderer.write("\n"); err != nil {
			return err
		}
	}
	renderer.setToolHeaderState(presentation, name, nil)
	return nil
}

func (renderer *Renderer) setToolHeaderState(presentation coretools.ToolPresentation, name string, preview *toolPreviewRenderer) {
	renderer.mode = modeTool
	renderer.toolName = name
	renderer.toolPresentation = presentation
	renderer.toolOutputBytes = 0
	renderer.toolPreview = preview
}

// ToolOutput writes bytes sent by a tool's optional display sink.
func (renderer *Renderer) ToolOutput(data []byte) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if len(data) == 0 || renderer.toolPresentation.PreviewMode == coretools.PreviewCollapsed {
		return nil
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	firstOutput := renderer.toolOutputBytes == 0
	renderer.toolOutputBytes += len(data)
	if renderer.toolPreview != nil {
		// Upstream selects diff rendering only for a completed, non-streamed result. A stream may
		// contain ordinary file content beginning with diff-looking prefixes.
		if firstOutput {
			renderer.toolPreview.style = coretools.OutputPlain
		}
		return renderer.write(renderer.toolPreview.Feed(data))
	}
	return nil
}

// ToolResult renders any final preview not already represented by streamed display bytes.
func (renderer *Renderer) ToolResult(execution coretools.ToolExecution) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	if renderer.mode != modeTool {
		if err := renderer.ToolStart(execution.Result, execution.Presentation); err != nil {
			return err
		}
	}
	if renderer.toolPresentation.PreviewMode != coretools.PreviewCollapsed {
		if renderer.toolOutputBytes == 0 && !execution.OutputSummarizesDisplay && execution.Result.Output != nil && renderer.toolPreview != nil {
			visible := *execution.Result.Output
			if execution.HiddenTail > 0 && execution.HiddenTail <= len(visible) {
				visible = visible[:len(visible)-execution.HiddenTail]
			}
			if *execution.Result.Output == "" && renderer.toolPresentation.OutputStyle == coretools.OutputUnifiedDiff && execution.FallbackOutput == nil {
				if err := renderer.write(renderer.toolPreview.NoChangesMarker()); err != nil {
					return err
				}
			} else if err := renderer.write(renderer.toolPreview.Feed([]byte(visible))); err != nil {
				return err
			}
		}
		if renderer.toolPreview != nil {
			if err := renderer.write(renderer.toolPreview.Finalize()); err != nil {
				return err
			}
			fallback := execution.FallbackOutput
			if fallback == nil && execution.OutputSummarizesDisplay {
				fallback = execution.Result.Output
			}
			if renderer.toolPresentation.OutputStyle == coretools.OutputUnifiedDiff && renderer.toolPreview.rows == 0 &&
				fallback != nil && *fallback != "" {
				fallbackPreview := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, renderer.width, renderer.styled)
				if err := renderer.write(fallbackPreview.Feed([]byte(*fallback))); err != nil {
					return err
				}
				if err := renderer.write(fallbackPreview.Finalize()); err != nil {
					return err
				}
			}
		}
	}
	renderer.mode = modeIdle
	renderer.toolName = ""
	renderer.toolOutputBytes = 0
	renderer.toolPreview = nil
	return nil
}

// Tick advances the delayed animated status indicator and table composition feedback without
// sleeping.
func (renderer *Renderer) Tick(now time.Time) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	label := renderer.statusLabel
	started := renderer.statusStarted
	showAt := started.Add(statusShowGrace)
	tableStatus := false
	if !renderer.retryDeadline.IsZero() {
		label = renderer.retryLabel(now)
		started = renderer.statusStarted
		showAt = started.Add(statusShowGrace)
	}
	if !renderer.tableStarted.IsZero() && now.Sub(renderer.tableStarted) >= tableStatusDelay {
		label = "composing..."
		started = renderer.tableStarted.Add(tableStatusDelay)
		if renderer.tableStatusReset.After(started) {
			started = renderer.tableStatusReset
		}
		showAt = started.Add(statusShowGrace)
		tableStatus = true
	}
	if label == "" || started.IsZero() {
		return nil
	}
	if renderer.mode != modeIdle && !(tableStatus && renderer.mode == modeText) {
		return nil
	}
	// The show grace delays a first appearance only. A label that replaces a visible one repaints
	// over it on the next tick, as upstream's spinner_set_label redraws without erasing.
	if !renderer.statusVisible && now.Before(showAt) {
		return nil
	}
	frame := int(now.Sub(started) / spinnerInterval)
	if frame < 0 {
		frame = 0
	}
	frame %= len(spinnerFrames)
	if renderer.statusVisible && renderer.statusPainted == label && renderer.statusFrame == frame {
		return nil
	}
	// One frame per write, with the row's tail erased after the paint instead of before it:
	// erasing first leaves a blank row on screen for every frame (upstream draw_label_row_locked
	// followed by finish_row_repaint).
	var row strings.Builder
	// The statusLineNewlines recorded here is the count clearStatus restores, so it has to
	// describe the row this paint commits.
	if renderer.statusVisible {
		row.WriteByte('\r')
	} else {
		renderer.statusLineNewlines = renderer.lineNewlines
		if renderer.collapsedLineOpen {
			// Park the indicator below the open read row: the extra row is the gap the cleared
			// indicator leaves behind (upstream spinner_park).
			row.WriteString("\n\n")
			renderer.statusParkedRows = 2
			renderer.statusParkedColumn = renderer.collapsedCells
		} else if renderer.outputStarted && renderer.lineNewlines == 0 {
			// The paint's own newline ends the row it covers, and the erase returns to that row:
			// without counting it the next block separator lands one row too low, leaving a second
			// blank line before the row that follows. Upstream writes the indicator outside disp,
			// so its count still sees the answer's own newlines.
			row.WriteByte('\n')
			renderer.statusLineNewlines = 1
		}
	}
	if renderer.styled {
		row.WriteString(ansiDim)
	}
	row.WriteString(spinnerFrames[frame])
	row.WriteByte(' ')
	row.WriteString(renderer.statusText(label, now))
	if renderer.styled {
		row.WriteString(ansiReset)
	}
	row.WriteString(ansiEraseTail)
	if err := renderer.write(row.String()); err != nil {
		return err
	}
	renderer.statusVisible = true
	renderer.statusFrame = frame
	renderer.statusPainted = label
	return nil
}

// PausePending changes the delayed status line to explain that Escape is waiting for the current
// provider step to reach a safe turn boundary.
func (renderer *Renderer) PausePending(now time.Time) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return io.ErrClosedPipe
	}
	const label = "pausing... (esc again to interrupt)"
	if renderer.statusLabel == label {
		return nil
	}
	// The pause label takes the row over without erasing it: the next tick paints it in place, so
	// a visible indicator never blinks off and on around the pause.
	renderer.statusLabel = label
	renderer.statusStarted = now
	return nil
}

// HasRenderedAnswer reports whether the current provider attempt produced visible answer content.
func (renderer *Renderer) HasRenderedAnswer() bool { return renderer.answerRendered }

// Err returns the first terminal writer or Markdown stream error.
func (renderer *Renderer) Err() error { return renderer.failure() }

// Close flushes the current block exactly once.
func (renderer *Renderer) Close() error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if renderer.closed {
		return nil
	}
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	if err := renderer.Boundary(); err != nil {
		return err
	}
	if err := renderer.markdown.Close(); err != nil {
		return renderer.fail(err)
	}
	renderer.closed = true
	return nil
}

func (renderer *Renderer) endReasoning() error {
	if renderer.styled {
		if err := renderer.write(ansiReset); err != nil {
			return err
		}
	}
	if err := renderer.write("\n"); err != nil {
		return err
	}
	renderer.mode = modeIdle
	return nil
}

func (renderer *Renderer) openBlock() error {
	if err := renderer.closeCollapsedCluster(); err != nil {
		return err
	}
	if !renderer.outputStarted {
		return nil
	}
	need := 2 - renderer.lineNewlines
	for ; need > 0; need-- {
		if err := renderer.write("\n"); err != nil {
			return err
		}
	}
	return nil
}

func (renderer *Renderer) closeCollapsedCluster() error {
	if !renderer.collapsedCluster {
		return nil
	}
	// The indicator must be hidden first: a parked status restores the open row's cursor, which
	// would otherwise be terminated from the row the status was painted on.
	if err := renderer.clearStatus(); err != nil {
		return err
	}
	if renderer.collapsedLineOpen {
		if err := renderer.write("\n"); err != nil {
			return err
		}
	}
	renderer.collapsedCluster = false
	renderer.collapsedLineOpen = false
	renderer.collapsedLastTool = ""
	renderer.collapsedCells = 0
	return nil
}

func (renderer *Renderer) setStatus(label string) {
	if label == renderer.statusLabel {
		return
	}
	if label == "" {
		if err := renderer.clearStatus(); err != nil {
			return
		}
		renderer.statusLabel = ""
		renderer.statusStarted = time.Time{}
		return
	}
	// A label that replaces a visible one is painted over it by the next tick, never erased
	// first: upstream's spinner_set_label redraws the row and the erase follows the paint.
	renderer.statusLabel = label
	renderer.statusStarted = time.Now()
}

func (renderer *Renderer) clearStatus() error {
	if !renderer.statusVisible {
		return nil
	}
	var clear strings.Builder
	clear.WriteString(ansiEraseLine)
	if renderer.statusParkedRows > 0 {
		// A parked indicator sits below the open read row, so clearing it must climb back and
		// restore the row's column for the next coalesced read.
		fmt.Fprintf(&clear, "\x1b[%dA", renderer.statusParkedRows)
		if renderer.statusParkedColumn > 0 {
			fmt.Fprintf(&clear, "\x1b[%dG", renderer.statusParkedColumn+1)
		}
	}
	if err := renderer.write(clear.String()); err != nil {
		return err
	}
	renderer.lineNewlines = renderer.statusLineNewlines
	if renderer.statusParkedRows > 0 {
		renderer.lineNewlines = 0
	}
	renderer.statusParkedRows = 0
	renderer.statusParkedColumn = 0
	renderer.statusVisible = false
	renderer.statusFrame = -1
	renderer.statusPainted = ""
	return nil
}

func (renderer *Renderer) retryLabel(now time.Time) string {
	remaining := renderer.retryDeadline.Sub(now)
	if renderer.retryAttempt <= 0 || renderer.retryMaxAttempts <= 0 {
		return "retrying..."
	}
	if remaining <= 0 {
		return fmt.Sprintf("retrying (attempt %d/%d)...", renderer.retryAttempt, renderer.retryMaxAttempts)
	}
	seconds := int((remaining + time.Second - 1) / time.Second)
	return fmt.Sprintf("retrying in %ds (attempt %d/%d)...", seconds, renderer.retryAttempt, renderer.retryMaxAttempts)
}

func (renderer *Renderer) statusText(label string, now time.Time) string {
	width := renderer.width
	if width <= 0 {
		width = 80
	}
	label = terminalwidth.FlattenForDisplay(label)
	labelBudget := width - 3 // reserve the spinner, its separator, and the final terminal column
	if labelBudget < 0 {
		labelBudget = 0
	}
	if !renderer.turnStarted.IsZero() {
		elapsed := now.Sub(renderer.turnStarted)
		if elapsed >= 30*time.Second {
			duration := formatStatusDuration(elapsed)
			prefixCells := len(duration) + 3 // spaces and middle dot
			if prefixCells+terminalwidth.Cells(label) <= labelBudget {
				label = duration + " · " + label
				labelBudget -= prefixCells
			}
		}
	}
	return terminalwidth.TruncateForDisplay(label, labelBudget)
}

func formatStatusDuration(duration time.Duration) string {
	seconds := int64(0)
	if duration > 0 {
		seconds = int64(duration / time.Second)
		if duration%time.Second >= 500*time.Millisecond {
			seconds++
		}
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%dh %02dm", seconds/3600, seconds%3600/60)
	}
}

func (renderer *Renderer) write(text string) error {
	if err := renderer.failure(); err != nil {
		return err
	}
	if text == "" || renderer.writer == nil {
		return nil
	}
	data := []byte(text)
	for len(data) > 0 {
		written, err := renderer.writer.Write(data)
		if written > 0 {
			renderer.trackWritten(data[:written])
			data = data[written:]
		}
		if err != nil {
			return renderer.fail(err)
		}
		if written == 0 {
			return renderer.fail(io.ErrShortWrite)
		}
	}
	return nil
}

func (renderer *Renderer) trackWritten(data []byte) {
	if len(data) == 0 {
		return
	}
	renderer.outputStarted = true
	ansi := false
	for _, b := range data {
		if ansi {
			if b >= 0x40 && b <= 0x7e {
				ansi = false
			}
			continue
		}
		if b == 0x1b {
			ansi = true
			continue
		}
		if b == '\n' {
			renderer.lineNewlines++
		} else if b != '\r' {
			renderer.lineNewlines = 0
		}
	}
}

func (renderer *Renderer) failure() error {
	// Markdown stream errors need no separate latch: the stream only fails when its writer
	// callback does, and that callback is renderer.write, which latches through fail.
	return renderer.err
}

func (renderer *Renderer) fail(err error) error {
	if err == nil {
		return renderer.err
	}
	if renderer.err == nil {
		renderer.err = err
	}
	return renderer.err
}

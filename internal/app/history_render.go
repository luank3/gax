package app

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
	terminalLive "github.com/luank3/gax/internal/terminal/live"
	terminalvt "github.com/luank3/gax/internal/terminal/vtresolve"
	coretools "github.com/luank3/gax/internal/tools"
)

type historyDetail uint8

const (
	historyFull historyDetail = iota
	historyBrief
)

func renderHistoryItems(writer io.Writer, items []conversation.Item, registry *coretools.Registry,
	width int, showReasoning bool, detail historyDetail) error {
	return renderHistoryItemsWithBoundary(writer, items, registry, width, showReasoning, detail, false)
}

func renderHistoryItemsWithBoundary(writer io.Writer, items []conversation.Item, registry *coretools.Registry,
	width int, showReasoning bool, detail historyDetail, trailingBoundary bool) error {
	if registry == nil {
		registry = coretools.New(coretools.Options{})
		defer registry.Close()
	}
	renderer := terminalLive.NewRenderer(writer, width, true, showReasoning)
	renderer.SetMarkdownEnabled(true)
	start := 0
	for index, item := range items {
		if item.Kind != conversation.KindTurnBoundary {
			continue
		}
		if err := renderHistoryTurn(renderer, items, start, index, registry, detail); err != nil {
			return err
		}
		if err := renderer.Event(conversation.Event{Kind: conversation.EventDone}); err != nil {
			return err
		}
		start = index + 1
	}
	if err := renderHistoryTurn(renderer, items, start, len(items), registry, detail); err != nil {
		return err
	}
	if trailingBoundary {
		if err := renderer.Separate(); err != nil {
			return err
		}
	}
	return renderer.Close()
}

func renderHistoryTurn(renderer *terminalLive.Renderer, items []conversation.Item, start, end int,
	registry *coretools.Registry, detail historyDetail) error {
	postDispatchFrom := end
	for index := end; index > start; index-- {
		if items[index-1].Kind == conversation.KindToolResult {
			postDispatchFrom = index
			break
		}
	}
	if err := renderHistoryStreamedRange(renderer, items, start, postDispatchFrom); err != nil {
		return err
	}
	for callIndex := start; callIndex < end; callIndex++ {
		if items[callIndex].Kind != conversation.KindToolCall {
			continue
		}
		if err := renderHistoryTool(renderer, items, end, callIndex, registry, detail); err != nil {
			return err
		}
	}
	return renderHistoryStreamedRange(renderer, items, postDispatchFrom, end)
}

func renderHistoryStreamedRange(renderer *terminalLive.Renderer, items []conversation.Item, from, to int) error {
	previous := to
	for index := from; index < to; index++ {
		item := items[index]
		if !isHistoryStreamedKind(item.Kind) {
			continue
		}
		if previous != to && index != previous+1 {
			if err := renderer.Event(conversation.Event{Kind: conversation.EventDone}); err != nil {
				return err
			}
		}
		previous = index
		switch item.Kind {
		case conversation.KindUser:
			switch item.Origin {
			case "":
				if item.Text != nil {
					if err := renderer.UserMessage(*item.Text); err != nil {
						return err
					}
				}
			case conversation.OriginCompactSeed:
				if err := renderer.TaskNote("── conversation compacted ──"); err != nil {
					return err
				}
			case conversation.OriginTaskNote:
				if err := renderer.TaskNote(historyString(item.Text)); err != nil {
					return err
				}
			}
		case conversation.KindAssistant:
			if err := renderHistoryAssistant(renderer, item); err != nil {
				return err
			}
		case conversation.KindReasoning:
			if err := renderer.StoredReasoning(historyString(item.ReasoningText)); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderHistoryAssistant(renderer *terminalLive.Renderer, item conversation.Item) error {
	text := historyString(item.Text)
	if item.Origin == conversation.OriginInterrupted && strings.HasSuffix(text, agent.InterruptMarker) {
		markerAt := len(text) - len(agent.InterruptMarker)
		if markerAt == 0 || text[markerAt-1] == '\n' {
			partial := strings.TrimSuffix(text[:markerAt], "\n")
			if err := renderer.StoredText(string(terminalvt.Resolve([]byte(partial)))); err != nil {
				return err
			}
			return renderer.TaskNote(agent.InterruptMarker)
		}
	}
	return renderer.StoredText(string(terminalvt.Resolve([]byte(text))))
}

func renderHistoryTool(renderer *terminalLive.Renderer, items []conversation.Item, turnEnd, callIndex int,
	registry *coretools.Registry, detail historyDetail) error {
	call := items[callIndex]
	presentation := registry.Presentation(call)
	resultIndex := pairedHistoryResult(items, turnEnd, callIndex)
	var result conversation.Item
	if resultIndex >= 0 {
		result = items[resultIndex]
	}
	marker := ""
	if resultIndex >= 0 {
		switch result.Origin {
		case conversation.OriginSkipped:
			marker = agent.InterruptMarker
		case conversation.OriginRefused:
			marker = "[refused: --raw, no tools advertised]"
		}
	}
	if detail == historyFull && marker == "" {
		presentation.PreviewMode = coretools.PreviewFull
	} else if detail == historyBrief && marker == "" {
		presentation.PreviewMode = coretools.PreviewCollapsed
	}
	if marker != "" {
		presentation.PreviewMode = coretools.PreviewHead
	}
	if err := renderer.ToolStart(call, presentation); err != nil {
		return err
	}
	if marker != "" {
		result.Output = conversation.String(marker)
		result.OutputHiddenTail = 0
	}
	if resultIndex < 0 {
		result = conversation.Item{Kind: conversation.KindToolResult, CallID: call.CallID}
	}
	var fallbackOutput *string
	if resultIndex >= 0 && result.Origin == conversation.OriginSummarized {
		result, fallbackOutput = reconstructSummarizedWrite(call, result)
	}
	return renderer.ToolResult(coretools.ToolExecution{
		Result: result, Presentation: presentation, FallbackOutput: fallbackOutput,
		HiddenTail: hiddenHistoryTail(result),
	})
}

func pairedHistoryResult(items []conversation.Item, turnEnd, callIndex int) int {
	callID := items[callIndex].CallID
	if callID == nil {
		return -1
	}
	for index := callIndex + 1; index < turnEnd; index++ {
		if items[index].Kind == conversation.KindToolResult && items[index].CallID != nil && *items[index].CallID == *callID {
			return index
		}
	}
	return -1
}

func reconstructSummarizedWrite(call, result conversation.Item) (conversation.Item, *string) {
	if call.ToolName == nil || *call.ToolName != "write" || call.Arguments == nil {
		return result, nil
	}
	fallback := result.Output
	var arguments struct {
		Content *string `json:"content"`
	}
	if json.Unmarshal([]byte(*call.Arguments), &arguments) == nil && arguments.Content != nil {
		result.Output = arguments.Content
		result.OutputHiddenTail = 0
		return result, fallback
	}
	return result, nil
}

func hiddenHistoryTail(result conversation.Item) int {
	if result.OutputHiddenTail <= 0 || result.OutputHiddenTail > int64(int(^uint(0)>>1)) {
		return 0
	}
	return int(result.OutputHiddenTail)
}

func isHistoryStreamedKind(kind conversation.Kind) bool {
	return kind == conversation.KindUser || kind == conversation.KindAssistant || kind == conversation.KindReasoning
}

func historyString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

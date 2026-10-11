package app

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/catalog"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider/runtime"
	"github.com/luank3/gax/internal/session"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminalnotify "github.com/luank3/gax/internal/terminal/notify"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

const sessionLabelWidth = 14
const costDisplayMinimum = 0.00005

type sessionRows struct {
	writer           io.Writer
	columns          int
	styled           bool
	printedInGroup   bool
	separatorPending bool
}

func printSessionSummary(writer io.Writer, state *agent.Session, provider *runtime.Provider, resolver *runtime.Resolver,
	log *session.Log, store *config.Store) {
	printSessionSummaryWithStyle(writer, state, provider, resolver, log, store, true)
}

func printSessionSummaryWithStyle(writer io.Writer, state *agent.Session, provider *runtime.Provider, resolver *runtime.Resolver,
	log *session.Log, store *config.Store, styled bool) {
	rows := sessionRows{writer: writer, columns: appDisplayWidth(store), styled: styled}
	hint := "not recorded"
	if id := log.ResumeHint(); id != nil && *id != "" {
		hint = *id
	}
	rows.print("session", hint)
	if preset := currentPreset(store); preset != nil {
		rows.print("preset", *preset)
	}
	if state != nil && provider != nil && state.Model != nil && *state.Model != "" {
		provider.WaitModelMetadata(runtime.ModelMetadataWait, nil)
		var requestedEffort *string
		if store != nil {
			requestedEffort = store.String("effort")
		}
		state.Effort = provider.ResolveEffort(*state.Model, requestedEffort)
	}
	providerName := "?"
	if provider != nil {
		if provider.DisplayName != "" {
			providerName = provider.DisplayName
		} else if provider.Definition != nil && provider.Definition.ID != "" {
			providerName = provider.Definition.ID
		}
	}
	if state != nil && state.ProviderID != nil && providerName == "?" {
		providerName = *state.ProviderID
	}
	model := "?"
	if state != nil && state.Model != nil && *state.Model != "" {
		model = *state.Model
	}
	providerValue := providerName + " · " + model
	if state != nil && state.Effort != nil && *state.Effort != "" {
		providerValue += " · " + *state.Effort
	}
	if terminalwidth.Cells(providerValue) > rows.columns-sessionValueIndent(rows.columns) {
		if state != nil && state.Effort != nil && *state.Effort != "" {
			providerValue = providerName + "\n" + model + " · " + *state.Effort
		} else {
			providerValue = providerName + "\n" + model
		}
	}
	rows.print("provider", providerValue)

	stats := agent.CollectStatsWithRateLookup(state, sessionRateLookup(provider, resolver))
	rows.group()
	if stats.UserTurns > 0 || stats.UndoneUserTurns > 0 {
		value := strconv.FormatInt(stats.UserTurns, 10)
		if stats.UndoneUserTurns > 0 {
			value += " · " + strconv.FormatInt(stats.UndoneUserTurns, 10) + " undone"
		}
		rows.print("user turns", value)
	}
	if stats.ToolCalls > 0 {
		value := strconv.FormatInt(stats.ToolCalls, 10)
		for _, tool := range stats.Tools {
			value += " · " + tool.Name + " " + strconv.FormatInt(tool.Count, 10)
		}
		rows.print("tool calls", value)
	}
	window := int64(0)
	if provider != nil && state != nil && state.Model != nil {
		window = provider.ContextWindowForModel(*state.Model)
	}
	if stats.ContextTokens > 0 {
		rows.print("context", formatContext(stats.ContextTokens, window))
	} else if window > 0 {
		rows.print("context", formatContext(-1, window))
	}

	rows.group()
	if stats.Total.Requests > 0 {
		rows.print("requests", strconv.FormatInt(stats.Total.Requests, 10))
	}
	if stats.WorkedMS > 0 {
		rows.print("time worked", formatDuration(stats.WorkedMS))
	}
	if stats.Total.InputTokens > 0 || stats.Total.OutputTokens > 0 {
		if len(stats.Models) > 1 {
			for index, modelUsage := range stats.Models {
				providerLabel, modelLabel := modelUsage.Provider, modelUsage.Model
				if providerLabel == "" {
					providerLabel = "?"
				}
				if modelLabel == "" {
					modelLabel = "?"
				}
				spend := ""
				if modelUsage.Totals.Spend > 0 {
					spend = formatCost(modelUsage.Totals.Spend)
					if modelUsage.Totals.SpendEstimated {
						spend = "~" + spend
					}
					spend += " · "
				}
				value := providerLabel + " · " + modelLabel + "\n" + spend + formatUsageRow(modelUsage.Totals)
				label := ""
				if index == 0 {
					label = "tokens"
				}
				rows.print(label, value)
			}
		} else {
			rows.print("tokens", formatUsageRow(stats.Total))
		}
	}
	if stats.Total.Spend > 0 {
		value := formatCost(stats.Total.Spend)
		if stats.Total.SpendEstimated {
			value = "~" + value
		}
		rows.print("spend", value)
	}
	if stats.InheritedUserTurns > 0 {
		turns := strconv.FormatInt(stats.InheritedUserTurns, 10) + " user turn"
		if stats.InheritedUserTurns != 1 {
			turns += "s"
		}
		if stats.Inherited.Spend > 0 {
			spend := formatCost(stats.Inherited.Spend)
			if stats.Inherited.SpendEstimated {
				spend = "~" + spend
			}
			turns += " · " + spend
		}
		rows.print("inherited", turns)
	}
}

func sessionRateLookup(active *runtime.Provider, resolver *runtime.Resolver) agent.ModelRateLookup {
	if resolver == nil {
		return nil
	}
	cache := make(map[string]*runtime.Provider)
	if active != nil && active.Definition != nil {
		cache[active.Definition.ID] = active
	}
	return func(providerID, model string) (catalog.Entry, bool) {
		candidate, ok := cache[providerID]
		if !ok {
			var err error
			candidate, err = resolver.Resolve(providerID)
			if err != nil {
				return catalog.Entry{}, false
			}
			cache[providerID] = candidate
		}
		return candidate.ModelMetadata(model)
	}
}

func (rows *sessionRows) group() {
	rows.separatorPending = rows.printedInGroup
	rows.printedInGroup = false
}

func (rows *sessionRows) print(label, value string) {
	if rows.separatorPending {
		_, _ = fmt.Fprintln(rows.writer)
		rows.separatorPending = false
	}
	rows.printedInGroup = true
	styleOpen := ""
	if rows.styled {
		styleOpen = "\x1b[2m"
	}
	alignedColumn := 2 + sessionLabelWidth
	if rows.columns-alignedColumn >= 20 {
		textColumn := alignedColumn
		_, _ = fmt.Fprint(rows.writer, "  ")
		if styleOpen != "" {
			_, _ = fmt.Fprint(rows.writer, styleOpen)
		}
		_, _ = fmt.Fprint(rows.writer, label)
		if styleOpen != "" {
			_, _ = fmt.Fprint(rows.writer, "\x1b[0m")
		}
		padding := textColumn - 2 - terminalwidth.Cells(label)
		if padding > 0 {
			_, _ = io.WriteString(rows.writer, strings.Repeat(" ", padding))
		}
		writeStyledWrappedRows(rows.writer, value, textColumn, rows.columns, styleOpen)
		return
	}
	_, _ = fmt.Fprint(rows.writer, "  ")
	if styleOpen != "" {
		_, _ = fmt.Fprint(rows.writer, styleOpen)
	}
	_, _ = fmt.Fprint(rows.writer, label)
	if styleOpen != "" {
		_, _ = fmt.Fprint(rows.writer, "\x1b[0m")
	}
	_, _ = fmt.Fprintln(rows.writer)
	_, _ = io.WriteString(rows.writer, "    ")
	writeStyledWrappedRows(rows.writer, value, 4, rows.columns, styleOpen)
}

func sessionValueIndent(columns int) int {
	valueColumn := 2 + sessionLabelWidth
	if columns-valueColumn >= 20 {
		return valueColumn
	}
	return 4
}

func writeTurnStatsLineWithStyle(writer io.Writer, state *agent.Session, provider *runtime.Provider,
	contextTokens, elapsedMS int64, store *config.Store, styled bool) error {
	stats := agent.CollectStatsWithProvider(state, provider)
	return writeStatsSegmentsWithStyle(writer, state, provider, contextTokens, elapsedMS, store, styled, stats.Total)
}

func writeStatsSegmentsWithStyle(writer io.Writer, state *agent.Session, provider *runtime.Provider,
	contextTokens, elapsedMS int64, store *config.Store, styled bool, totals agent.UsageTotals) error {
	segments := make([]string, 0, 3)
	if elapsedMS >= 0 {
		segments = append(segments, formatDuration(elapsedMS))
	}
	if contextTokens >= 0 {
		contextLimit := int64(0)
		if provider != nil && state != nil && state.Model != nil {
			contextLimit = provider.ContextWindowForModel(*state.Model)
		}
		context := formatContext(contextTokens, contextLimit)
		if contextLimit <= 0 {
			context = "context " + context
		}
		segments = append(segments, context)
	}
	if totals.Spend > 0 {
		spend := formatCost(totals.Spend)
		if totals.SpendEstimated {
			spend = "~" + spend
		}
		segments = append(segments, spend)
	}
	if len(segments) == 0 {
		return nil
	}

	columns := appDisplayWidth(store)
	rows := make([]string, 0, len(segments))
	var row strings.Builder
	rowCells := 0
	for _, segment := range segments {
		segmentCells := terminalwidth.Cells(segment)
		if rowCells > 0 && rowCells+3+segmentCells > columns {
			rows = append(rows, row.String())
			row.Reset()
			rowCells = 0
		}
		if rowCells > 0 {
			row.WriteString(" · ")
			rowCells += 3
		}
		row.WriteString(segment)
		rowCells += segmentCells
	}
	rows = append(rows, row.String())
	for _, line := range rows {
		if styled {
			if _, err := io.WriteString(writer, "\x1b[2m"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer, line); err != nil {
			return err
		}
		if styled {
			if _, err := io.WriteString(writer, "\x1b[0m"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeOneShotExitNotes(writer io.Writer, state *agent.Session, provider *runtime.Provider, log *session.Log,
	contextTokens, elapsedMS int64, itemsFrom, retiredFrom int, store *config.Store) {
	var resumeHint *string
	if log != nil {
		resumeHint = log.ResumeHint()
	}
	view, stats := collectOneShotStats(state, provider, itemsFrom, retiredFrom)
	haveStats := contextTokens >= 0 || stats.Total.Spend > 0
	if resumeHint == nil && !haveStats {
		return
	}
	_, _ = fmt.Fprintln(writer)
	styled := terminalnotify.IsTerminalWriter(writer)
	if haveStats {
		_ = writeStatsSegmentsWithStyle(writer, &view, provider,
			contextTokens, elapsedMS, store, styled, stats.Total)
	}
	if resumeHint != nil {
		writeNoteLineWithStyle(writer, "resume with: gax --resume="+*resumeHint, styled)
	}
}

func collectOneShotStats(state *agent.Session, provider *runtime.Provider,
	itemsFrom, retiredFrom int) (agent.Session, agent.SessionStats) {
	view := agent.Session{}
	if state != nil {
		view = *state
	}
	if itemsFrom < 0 {
		itemsFrom = 0
	}
	if itemsFrom > len(view.Items) {
		itemsFrom = len(view.Items)
	}
	if retiredFrom < 0 {
		retiredFrom = 0
	}
	if retiredFrom > len(view.Retired) {
		retiredFrom = len(view.Retired)
	}
	view.Items = view.Items[itemsFrom:]
	view.Retired = view.Retired[retiredFrom:]
	stats := agent.CollectStatsWithProvider(&view, provider)
	if stats.Total.Unpriced && provider != nil {
		provider.WaitModelMetadata(3*time.Second, nil)
		stats = agent.CollectStatsWithProvider(&view, provider)
	}
	return view, stats
}

func replayRecentTurn(writer io.Writer, state *agent.Session, heading string, provider *runtime.Provider,
	store *config.Store, registry *coretools.Registry, tty bool) error {
	if !tty || state == nil || len(state.Items) == 0 {
		return nil
	}
	anchor := -1
	earlier := 0
	for index, item := range state.Items {
		if item.Kind != conversation.KindUser || item.Origin != "" && item.Origin != conversation.OriginCompactSeed {
			continue
		}
		if anchor >= 0 {
			earlier++
		}
		anchor = index
	}
	utf8 := terminalocale.HaveUTF8()
	open, close := "-- ", " --"
	if utf8 {
		open, close = "── ", " ──"
	}
	line := open + heading
	if earlier > 0 {
		line += fmt.Sprintf(" · %d earlier message", earlier)
		if earlier != 1 {
			line += "s"
		}
		line += " · ctrl-o for full history"
	} else {
		line += " · ctrl-o for full history"
	}
	if _, err := fmt.Fprintf(writer, "\x1b[2m%s%s\x1b[0m\n\n", line, close); err != nil {
		return err
	}
	if anchor >= 0 {
		items := state.Items[anchor:]
		showReasoning := store != nil && store.Bool("show_reasoning")
		if err := renderHistoryItemsWithBoundary(writer, items, registry, appDisplayWidth(store),
			showReasoning, historyBrief, true); err != nil {
			return err
		}
	}
	if provider != nil {
		if err := writeTurnStatsLineWithStyle(writer, state, provider, state.LastContextTokens(),
			state.LastUserTurnMS, store, true); err != nil {
			return err
		}
		if heading == "resumed" {
			if _, err := io.WriteString(writer, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatUsageRow(totals agent.UsageTotals) string {
	inputCost, cacheCost, writeCost, outputCost := -1.0, -1.0, -1.0, -1.0
	if totals.SplitAvailable {
		inputCost, cacheCost = totals.CostInput, totals.CostCacheRead
		writeCost, outputCost = totals.CostCacheWrite, totals.CostOutput
	}
	segments := []string{formatTokenSegment("in", totals.UncachedInputTokens, inputCost)}
	if totals.CachedTokens > 0 {
		segments = append(segments, formatTokenSegment("cache", totals.CachedTokens, cacheCost))
	}
	if totals.CacheWriteTokens > 0 {
		segments = append(segments, formatTokenSegment("write", totals.CacheWriteTokens, writeCost))
	}
	segments = append(segments, formatTokenSegment("out", totals.OutputTokens, outputCost))
	return strings.Join(segments, " · ")
}

func formatTokenSegment(label string, tokens int64, cost float64) string {
	segment := label + " " + formatTokens(tokens)
	if cost >= costDisplayMinimum {
		segment += " ~" + formatCost(cost)
	}
	return segment
}

func formatTokens(tokens int64) string {
	switch {
	case tokens < 0:
		return "?"
	case tokens < 1000:
		return strconv.FormatInt(tokens, 10)
	case tokens < 10_000:
		return formatOneDecimal(float64(tokens)/1000, 'k')
	case tokens < 1_000_000:
		return strconv.FormatInt((tokens+500)/1000, 10) + "k"
	case tokens < 10_000_000:
		return formatOneDecimal(float64(tokens)/1_000_000, 'M')
	default:
		return strconv.FormatInt((tokens+500_000)/1_000_000, 10) + "M"
	}
}

func formatOneDecimal(value float64, suffix byte) string {
	formatted := strconv.FormatFloat(value, 'f', 1, 64)
	formatted = strings.TrimSuffix(formatted, ".0")
	return formatted + string(suffix)
}

func formatCost(usd float64) string {
	switch {
	case usd < 0.01:
		return "$" + strconv.FormatFloat(usd, 'f', 4, 64)
	case usd < 1:
		return "$" + strconv.FormatFloat(usd, 'f', 3, 64)
	default:
		return "$" + strconv.FormatFloat(usd, 'f', 2, 64)
	}
}

func formatDuration(durationMS int64) string {
	seconds := int64(0)
	if durationMS > 0 {
		seconds = durationMS / 1000
		if durationMS%1000 >= 500 {
			seconds++
		}
	}
	switch {
	case seconds < 60:
		return strconv.FormatInt(seconds, 10) + "s"
	case seconds < 3600 && seconds%60 == 0:
		return strconv.FormatInt(seconds/60, 10) + "m"
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	case seconds%3600 == 0:
		return strconv.FormatInt(seconds/3600, 10) + "h"
	default:
		return fmt.Sprintf("%dh %02dm", seconds/3600, seconds%3600/60)
	}
}

func formatContext(contextTokens, contextLimit int64) string {
	used := formatTokens(contextTokens)
	if contextLimit <= 0 {
		return used
	}
	limit := formatTokens(contextLimit)
	if contextTokens < 0 {
		return used + " / " + limit
	}
	percentage := int64(math.Min(float64(contextTokens)*100/float64(contextLimit), 999))
	return fmt.Sprintf("%s / %s (%d%%)", used, limit, percentage)
}

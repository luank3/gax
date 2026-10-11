// Package transcript renders and mirrors the provider-facing conversation.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

type Mode uint8

const (
	Plain Mode = iota
	ANSI
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
)

type renderer struct {
	writer  io.Writer
	mode    Mode
	palette terminaltheme.Palette
	utf8    bool
}

// Render writes a complete model-facing transcript. The context should come from Session.Context
// so compacted history is represented by the seed sent to the provider.
func Render(writer io.Writer, context provider.Context, mode Mode) error {
	r := renderer{writer: writer, mode: mode, palette: terminaltheme.Current(), utf8: terminalocale.HaveUTF8()}
	if err := r.renderHeader(context.SystemPrompt, context.Tools); err != nil {
		return err
	}
	turn := 0
	return renderItems(r, context.Items, 0, &turn)
}

// RenderItems writes items[first:] and increments turnNumber at each turn boundary.
func RenderItems(writer io.Writer, mode Mode, items []conversation.Item, first int, turnNumber *int) error {
	if first < 0 {
		first = 0
	}
	if first >= len(items) {
		return nil
	}
	if turnNumber == nil {
		turnNumber = new(int)
	}
	r := renderer{writer: writer, mode: mode, palette: terminaltheme.Current(), utf8: terminalocale.HaveUTF8()}
	return renderItems(r, items, first, turnNumber)
}

func renderItems(r renderer, items []conversation.Item, first int, turnNumber *int) error {
	if first < 0 {
		first = 0
	}
	if first >= len(items) {
		return nil
	}
	if turnNumber == nil {
		turnNumber = new(int)
	}
	usedResults := make(map[int]bool)
	for index := first; index < len(items); index++ {
		item := items[index]
		if item.Kind == conversation.KindToolResult && usedResults[index] {
			continue
		}
		switch item.Kind {
		case conversation.KindTurnBoundary:
			(*turnNumber)++
			if err := r.turnRule(*turnNumber); err != nil {
				return err
			}
			continue
		case conversation.KindUser:
			if err := r.user(item); err != nil {
				return err
			}
		case conversation.KindAssistant:
			if err := r.assistant(item); err != nil {
				return err
			}
		case conversation.KindToolCall:
			if err := r.toolCall(item); err != nil {
				return err
			}
			if item.CallID != nil {
				if result := findToolResult(items, index+1, *item.CallID); result >= 0 {
					if _, err := io.WriteString(r.writer, "\n"); err != nil {
						return err
					}
					if err := r.toolResult(items[result], stringValue(item.ToolName)); err != nil {
						return err
					}
					usedResults[result] = true
				}
			}
		case conversation.KindToolResult:
			if err := r.toolResult(item, ""); err != nil {
				return err
			}
		case conversation.KindReasoning:
			if err := r.reasoning(item); err != nil {
				return err
			}
		case conversation.KindTurnUsage:
			if item.Usage == nil {
				continue
			}
			if err := r.usage(item); err != nil {
				return err
			}
			if err := r.provenance(item); err != nil {
				return err
			}
		default:
			continue
		}
		if _, err := io.WriteString(r.writer, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func (r renderer) renderHeader(systemPrompt string, tools []provider.ToolDefinition) error {
	chromeOpen := r.roleOpen(terminaltheme.Chrome)
	if err := r.write(r.style(ansiBold) + chromeOpen + localeGlyph(r.utf8, "┏", "+") + strings.Repeat(localeGlyph(r.utf8, "━", "-"), 58) + localeGlyph(r.utf8, "┓", "+") + r.reset()); err != nil {
		return err
	}
	if err := r.write("\n" + r.style(ansiBold) + chromeOpen + localeGlyph(r.utf8, "┃", "|") + strings.Repeat(" ", 24) + "TRANSCRIPT" + strings.Repeat(" ", 24) + localeGlyph(r.utf8, "┃", "|") + r.reset()); err != nil {
		return err
	}
	if err := r.write("\n" + r.style(ansiBold) + chromeOpen + localeGlyph(r.utf8, "┗", "+") + strings.Repeat(localeGlyph(r.utf8, "━", "-"), 58) + localeGlyph(r.utf8, "┛", "+") + r.reset() + "\n\n"); err != nil {
		return err
	}
	if systemPrompt != "" {
		if err := r.section("system prompt"); err != nil {
			return err
		}
		if err := r.write(systemPrompt); err != nil {
			return err
		}
		if err := ensureNewline(r.writer, systemPrompt); err != nil {
			return err
		}
		if err := r.write("\n"); err != nil {
			return err
		}
	}
	if len(tools) == 0 {
		return nil
	}
	if err := r.section("tools"); err != nil {
		return err
	}
	for index, tool := range tools {
		if err := r.write(chromeOpen + "[" + displayName(tool.Name) + "]" + r.reset() + "\n"); err != nil {
			return err
		}
		if tool.Description != "" {
			if err := r.write(tool.Description); err != nil {
				return err
			}
			if err := ensureNewline(r.writer, tool.Description); err != nil {
				return err
			}
		}
		schema, err := json.MarshalIndent(buildToolSchema(tool), "", "  ")
		if err != nil {
			return err
		}
		if err := r.write("\n" + string(schema) + "\n"); err != nil {
			return err
		}
		if index+1 < len(tools) {
			if err := r.write("\n"); err != nil {
				return err
			}
		}
	}
	return r.write("\n")
}

func renderHeader(writer io.Writer, mode Mode, systemPrompt string, tools []provider.ToolDefinition) error {
	r := renderer{writer: writer, mode: mode, palette: terminaltheme.Current(), utf8: terminalocale.HaveUTF8()}
	return r.renderHeader(systemPrompt, tools)
}

type toolSchema struct {
	Type       string            `json:"type"`
	Properties orderedProperties `json:"properties"`
	Required   []string          `json:"required,omitempty"`
}

type orderedProperties []provider.ToolParam

type schemaItems struct {
	Type string `json:"type"`
}

type schemaProperty struct {
	Type        string       `json:"type,omitempty"`
	Items       *schemaItems `json:"items,omitempty"`
	Description string       `json:"description,omitempty"`
	Minimum     *int64       `json:"minimum,omitempty"`
}

func buildToolSchema(tool provider.ToolDefinition) toolSchema {
	schema := toolSchema{Type: "object", Properties: append(orderedProperties(nil), tool.Params...)}
	for _, parameter := range tool.Params {
		if parameter.Required {
			schema.Required = append(schema.Required, parameter.Name)
		}
	}
	return schema
}

func (properties orderedProperties) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, parameter := range properties {
		if index > 0 {
			output.WriteByte(',')
		}
		key, err := json.Marshal(parameter.Name)
		if err != nil {
			return nil, err
		}
		property := schemaProperty{Type: parameter.Type, Description: parameter.Description}
		if parameter.ItemType != "" {
			property.Items = &schemaItems{Type: parameter.ItemType}
		}
		if parameter.Minimum != 0 {
			property.Minimum = &parameter.Minimum
		}
		value, err := json.Marshal(property)
		if err != nil {
			return nil, err
		}
		output.Write(key)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func (r renderer) section(label string) error {
	rule := localeGlyph(r.utf8, "──", "--")
	return r.write(r.style(ansiDim) + rule + " " + label + " " + rule + r.reset() + "\n")
}

func (r renderer) turnRule(turn int) error {
	label := fmt.Sprintf(" # turn %d ", turn)
	ruleWidth := 60 - len(label)
	if ruleWidth < 4 {
		ruleWidth = 4
	}
	left := ruleWidth / 2
	right := ruleWidth - left
	rule := localeGlyph(r.utf8, "─", "-")
	return r.write(r.style(ansiBold) + r.roleOpen(terminaltheme.Chrome) + strings.Repeat(rule, left) + label +
		strings.Repeat(rule, right) + r.reset() + "\n\n")
}

func localeGlyph(haveUTF8 bool, utf8, ascii string) string {
	if haveUTF8 {
		return utf8
	}
	return ascii
}

func (r renderer) user(item conversation.Item) error {
	label, accent := "user", true
	switch item.Origin {
	case conversation.OriginCompactSeed:
		label, accent = "compaction seed", false
	case conversation.OriginTaskNote:
		label, accent = "task update", false
	case "":
	default:
		label, accent = "continuation", false
	}
	if err := r.section(label); err != nil {
		return err
	}
	text := stringValue(item.Text)
	if !accent {
		return renderStyledLines(r.writer, text, r.style(ansiDim), r.reset())
	}
	return renderStyledLines(r.writer, text, r.roleOpen(terminaltheme.Accent), r.roleClose(terminaltheme.Accent))
}

func (r renderer) assistant(item conversation.Item) error {
	if err := r.section("assistant"); err != nil {
		return err
	}
	text := stringValue(item.Text)
	if err := r.write(text); err != nil {
		return err
	}
	return ensureNewline(r.writer, text)
}

func (r renderer) toolCall(item conversation.Item) error {
	if err := r.write(r.roleOpen(terminaltheme.Chrome) + "[" + displayName(stringValue(item.ToolName)) + "]" + r.reset() + "\n"); err != nil {
		return err
	}
	arguments := stringValue(item.Arguments)
	if arguments == "" {
		return nil
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(arguments), "", "  ") == nil {
		arguments = pretty.String()
	}
	if err := r.write(arguments); err != nil {
		return err
	}
	return ensureNewline(r.writer, arguments)
}

func (r renderer) toolResult(item conversation.Item, toolName string) error {
	if err := r.section("tool result"); err != nil {
		return err
	}
	for _, image := range item.Images {
		if err := r.write(r.style(ansiDim) + provider.ImagePlaceholder(image) + r.reset() + "\n"); err != nil {
			return err
		}
	}
	text := stringValue(item.Output)
	if r.mode == ANSI && toolName == "read" {
		return renderReadBody(r.writer, text)
	}
	if r.mode == ANSI && (toolName == "edit" || toolName == "write") && strings.HasPrefix(text, "--- ") {
		return renderDiffBody(r.writer, text, r.palette)
	}
	if err := r.write(text); err != nil {
		return err
	}
	return ensureNewline(r.writer, text)
}

func (r renderer) reasoning(item conversation.Item) error {
	if item.ReasoningText != nil {
		if err := r.section("reasoning"); err != nil {
			return err
		}
		return renderStyledLines(r.writer, *item.ReasoningText, r.style(ansiDim), r.reset())
	}
	if err := r.write(r.style(ansiDim) + "[reasoning]" + r.reset()); err != nil {
		return err
	}
	if item.ReasoningJSON != nil {
		var object map[string]any
		if json.Unmarshal([]byte(*item.ReasoningJSON), &object) == nil {
			if id, ok := object["id"].(string); ok {
				if err := r.write(" " + r.style(ansiDim) + id + r.reset()); err != nil {
					return err
				}
			}
		}
	}
	return r.write("\n")
}

func (r renderer) usage(item conversation.Item) error {
	usage := item.Usage
	isFirst := true
	if usage.ElapsedMS != nil && *usage.ElapsedMS >= 0 {
		if err := r.usagePart(&isFirst, formatDuration(*usage.ElapsedMS)); err != nil {
			return err
		}
	}
	if usage.CostTotal != nil && *usage.CostTotal > 0 {
		cost := formatCost(*usage.CostTotal)
		if usage.CostEstimated {
			cost = "~" + cost
		}
		if err := r.usagePart(&isFirst, cost); err != nil {
			return err
		}
	}
	cached, write := nonnegativeInt(usage.CachedTokens), nonnegativeInt(usage.CacheWriteTokens)
	if usage.InputTokens != nil && *usage.InputTokens >= 0 {
		if err := r.tokenPart(&isFirst, "in", nonnegativeInt(usage.UncachedInput), positive(usage.CostInput)); err != nil {
			return err
		}
	}
	if cached > 0 {
		if err := r.tokenPart(&isFirst, "cache", cached, positive(usage.CostCacheRead)); err != nil {
			return err
		}
	}
	if write > 0 {
		if err := r.tokenPart(&isFirst, "write", write, positive(usage.CostCacheWrite)); err != nil {
			return err
		}
	}
	if usage.OutputTokens != nil && *usage.OutputTokens >= 0 {
		if err := r.tokenPart(&isFirst, "out", *usage.OutputTokens, positive(usage.CostOutput)); err != nil {
			return err
		}
	}
	return r.write(r.reset() + "\n")
}

func (r renderer) usagePart(first *bool, value string) error {
	separator := " · "
	if !*first {
		if err := r.write(separator); err != nil {
			return err
		}
	}
	*first = false
	return r.write(value)
}

func (r renderer) tokenPart(first *bool, label string, tokens int64, cost *float64) error {
	value := label + " " + formatTokens(tokens)
	if cost != nil && *cost >= 0.00005 {
		value += " ~" + formatCost(*cost)
	}
	return r.usagePart(first, value)
}

func (r renderer) provenance(item conversation.Item) error {
	usage := item.Usage
	providerName := stringValue(usage.ProviderLabel)
	if providerName == "" {
		providerName = stringValue(item.Provider)
	}
	model := stringValue(usage.ModelLabel)
	if model == "" {
		model = stringValue(item.Model)
	}
	if providerName == "" && model == "" {
		return nil
	}
	if err := r.write(r.style(ansiDim)); err != nil {
		return err
	}
	if providerName != "" {
		if err := r.write(providerName); err != nil {
			return err
		}
	}
	if model != "" {
		if providerName != "" {
			if err := r.write(" · "); err != nil {
				return err
			}
		}
		if err := r.write(model); err != nil {
			return err
		}
		if usage.Effort != nil && *usage.Effort != "" {
			if err := r.write(" · " + *usage.Effort); err != nil {
				return err
			}
		}
	}
	if usage.ServedModel != nil || usage.Route != nil {
		served := stringValue(usage.ServedModel)
		if served == "" {
			served = stringValue(usage.Route)
		}
		if err := r.write(" → " + served); err != nil {
			return err
		}
		if usage.ServedModel != nil && *usage.ServedModel != "" && usage.Route != nil && *usage.Route != "" {
			if err := r.write(" via " + *usage.Route); err != nil {
				return err
			}
		}
	}
	return r.write(r.reset() + "\n")
}

func (r renderer) write(value string) error {
	_, err := io.WriteString(r.writer, value)
	return err
}

func (r renderer) style(sequence string) string {
	if r.mode != ANSI {
		return ""
	}
	return sequence
}

func (r renderer) roleOpen(role terminaltheme.Role) string {
	if r.mode != ANSI {
		return ""
	}
	return r.palette.Open(role)
}

func (r renderer) roleClose(role terminaltheme.Role) string {
	if r.mode != ANSI {
		return ""
	}
	return r.palette.Close(role)
}

func (r renderer) reset() string {
	if r.mode != ANSI {
		return ""
	}
	return ansiReset
}

func renderStyledLines(writer io.Writer, text, open, close string) error {
	if text == "" {
		return ensureNewline(writer, text)
	}
	original := text
	for {
		line, rest, found := strings.Cut(text, "\n")
		if _, err := io.WriteString(writer, open+line+close); err != nil {
			return err
		}
		if !found {
			break
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
		text = rest
		if text == "" {
			break
		}
	}
	return ensureNewline(writer, original)
}

func renderReadBody(writer io.Writer, text string) error {
	original := text
	for {
		line, rest, found := strings.Cut(text, "\n")
		prefix := readLinePrefixLength(line)
		if prefix > 0 {
			if _, err := io.WriteString(writer, ansiDim+line[:prefix]+ansiReset+line[prefix:]); err != nil {
				return err
			}
		} else if _, err := io.WriteString(writer, line); err != nil {
			return err
		}
		if !found {
			break
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
		text = rest
		if text == "" {
			break
		}
	}
	return ensureNewline(writer, original)
}

func readLinePrefixLength(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	spaces := len(line) - len(trimmed)
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits == 0 || !strings.HasPrefix(trimmed[digits:], "→") {
		return 0
	}
	return spaces + digits + len("→")
}

func renderDiffBody(writer io.Writer, text string, palette terminaltheme.Palette) error {
	original := text
	inHunk := false
	for {
		line, rest, found := strings.Cut(text, "\n")
		open := ""
		switch {
		case strings.HasPrefix(line, "\\") || strings.HasPrefix(line, "@@"):
			open = ansiDim
		case inHunk && strings.HasPrefix(line, "+"):
			open = palette.Open(terminaltheme.Add)
		case inHunk && strings.HasPrefix(line, "-"):
			open = palette.Open(terminaltheme.Remove)
		case inHunk:
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			open = ansiDim
		case strings.HasPrefix(line, "+"):
			open = palette.Open(terminaltheme.Add)
		case strings.HasPrefix(line, "-"):
			open = palette.Open(terminaltheme.Remove)
		default:
			open = ansiDim
		}
		if open != "" {
			if _, err := io.WriteString(writer, open+line+ansiReset); err != nil {
				return err
			}
		} else if _, err := io.WriteString(writer, line); err != nil {
			return err
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
		}
		if !found {
			break
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
		text = rest
		if text == "" {
			break
		}
	}
	return ensureNewline(writer, original)
}

func ensureNewline(writer io.Writer, text string) error {
	if text == "" || !strings.HasSuffix(text, "\n") {
		_, err := io.WriteString(writer, "\n")
		return err
	}
	return nil
}

func findToolResult(items []conversation.Item, first int, id string) int {
	for index := first; index < len(items); index++ {
		if items[index].Kind == conversation.KindToolResult && items[index].CallID != nil && *items[index].CallID == id {
			return index
		}
	}
	return -1
}

func displayName(value string) string {
	if value == "" {
		return "?"
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func positive[T ~int64 | ~float64](value *T) *T {
	if value == nil || *value < 0 {
		return nil
	}
	copy := *value
	return &copy
}

func nonnegativeInt(value *int64) int64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func formatTokens(tokens int64) string {
	switch {
	case tokens < 0:
		return "?"
	case tokens < 1000:
		return fmt.Sprint(tokens)
	case tokens < 10000:
		return compactOneDecimal(float64(tokens)/1000, 'k')
	case tokens < 1000000:
		return fmt.Sprintf("%dk", tokens/1000+btoi(tokens%1000 >= 500))
	case tokens < 10000000:
		return compactOneDecimal(float64(tokens)/1000000, 'M')
	default:
		return fmt.Sprintf("%dM", tokens/1000000+btoi(tokens%1000000 >= 500000))
	}
}

func compactOneDecimal(value float64, suffix byte) string {
	formatted := fmt.Sprintf("%.1f%c", value, suffix)
	return strings.Replace(formatted, ".0", "", 1)
}

func btoi(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func formatCost(cost float64) string {
	switch {
	case cost <= 0:
		return "$0.00"
	case cost < 0.01:
		return fmt.Sprintf("$%.4f", cost)
	case cost < 1:
		return fmt.Sprintf("$%.3f", cost)
	default:
		return fmt.Sprintf("$%.2f", cost)
	}
}

func formatDuration(milliseconds int64) string {
	seconds := int64(0)
	if milliseconds > 0 {
		seconds = milliseconds / 1000
		if milliseconds%1000 >= 500 {
			seconds++
		}
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600 && seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dh %02dm", seconds/3600, seconds%3600/60)
	}
}

type Log struct {
	file         *os.File
	path         string
	itemsWritten int
	turnNumber   int
	context      provider.Context
}

// Init truncates the configured transcript before provider/session setup, matching hax startup.
func Init(path string) error {
	if path == "" {
		return nil
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	return file.Close()
}

// Open creates the configured plain-text mirror and writes its model-facing header.
func Open(path string, context provider.Context) (*Log, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return nil, err
	}
	log := &Log{file: file, path: path, context: headerContext(context)}
	if err := renderHeader(file, Plain, context.SystemPrompt, context.Tools); err != nil {
		_ = file.Close()
		return nil, err
	}
	return log, nil
}

// Sync appends newly visible items and rebuilds the file if the model-facing header or history
// length changed due to selection, compaction, undo, or a new conversation.
func (log *Log) Sync(context provider.Context, items []conversation.Item) error {
	if log == nil || log.file == nil {
		return nil
	}
	if !sameHeader(log.context, context) || len(items) < log.itemsWritten {
		if err := log.reopen(context); err != nil {
			return err
		}
	}
	if err := RenderItems(log.file, Plain, items, log.itemsWritten, &log.turnNumber); err != nil {
		return err
	}
	if len(items) > log.itemsWritten {
		log.itemsWritten = len(items)
	}
	return nil
}

// Reset truncates the mirror and writes the header and current history.
func (log *Log) Reset(context provider.Context, items []conversation.Item) error {
	if log == nil {
		return nil
	}
	if err := log.reopen(context); err != nil {
		return err
	}
	return log.Sync(context, items)
}

func (log *Log) reopen(context provider.Context) error {
	if log.file != nil {
		_ = log.file.Close()
		log.file = nil
	}
	file, err := os.OpenFile(log.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	log.file = file
	log.context = headerContext(context)
	log.itemsWritten = 0
	log.turnNumber = 0
	return renderHeader(file, Plain, context.SystemPrompt, context.Tools)
}

func (log *Log) Close() error {
	if log == nil || log.file == nil {
		return nil
	}
	err := log.file.Close()
	log.file = nil
	return err
}

func headerContext(context provider.Context) provider.Context {
	return provider.Context{SystemPrompt: context.SystemPrompt, Tools: append([]provider.ToolDefinition(nil), context.Tools...)}
}

func sameHeader(left, right provider.Context) bool {
	return left.SystemPrompt == right.SystemPrompt && reflect.DeepEqual(left.Tools, right.Tools)
}

// Package mock implements gax's in-process scripted and heuristic provider.
package mock

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

const textChunkBytes = 16

// Provider owns the captured script path and its per-session turn cursor.
type Provider struct {
	scriptPath       string
	diagnosticWriter io.Writer
	mu               sync.Mutex
	nextTurn         int
}

// New captures the configured path, matching the provider constructor.
func New(scriptPath *string) *Provider {
	provider := &Provider{diagnosticWriter: os.Stderr}
	if scriptPath != nil {
		provider.scriptPath = *scriptPath
	}
	return provider
}

// SetDiagnosticWriter routes the same script warnings the C provider writes to stderr.
func (provider *Provider) SetDiagnosticWriter(writer io.Writer) {
	if provider == nil {
		return
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if writer == nil {
		writer = os.Stderr
	}
	provider.diagnosticWriter = writer
}

// Stream emits one mock response using the same event contract as HTTP providers.
func (provider *Provider) Stream(ctx context.Context, request coreprovider.Context, tick func() bool,
	callback func(conversation.Event)) (transport.Response, error) {
	if provider == nil {
		return transport.Response{}, errors.New("mock provider is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkTick := func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return tick != nil && tick()
		}
	}
	if provider.scriptPath == "" {
		err := interactiveResponse(request, callback, checkTick)
		return mockResponse(err)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	err := provider.scriptedResponse(checkTick, callback)
	return mockResponse(err)
}

func mockResponse(err error) (transport.Response, error) {
	if errors.Is(err, transport.ErrCancelled) {
		return transport.Response{Cancelled: true}, transport.ErrCancelled
	}
	return transport.Response{}, err
}

type scriptResult uint8

const (
	scriptTurnComplete scriptResult = iota
	scriptExhausted
	scriptAborted
)

func (provider *Provider) scriptedResponse(tick func() bool, callback func(conversation.Event)) error {
	script, err := os.Open(provider.scriptPath)
	if err != nil {
		cause := errors.Unwrap(err)
		if cause == nil {
			cause = err
		}
		detail := cause.Error()
		if detail != "" {
			first, size := utf8.DecodeRuneInString(detail)
			detail = strings.ToUpper(string(first)) + detail[size:]
		}
		message := fmt.Sprintf("mock: cannot open '%s': %s", provider.scriptPath, detail)
		emit(callback, conversation.Event{Kind: conversation.EventError, ErrorMessage: message})
		return errors.New(message)
	}
	defer script.Close()

	reader := bufio.NewReader(script)
	if !skipTurns(reader, provider.nextTurn) {
		return emitExhausted(callback, tick)
	}
	result, err := playTurn(reader, provider.diagnosticWriter, callback, tick)
	if err != nil {
		return err
	}
	switch result {
	case scriptExhausted:
		return emitExhausted(callback, tick)
	case scriptAborted:
		return transport.ErrCancelled
	default:
		provider.nextTurn++
		return nil
	}
}

func skipTurns(reader *bufio.Reader, count int) bool {
	skipped := 0
	for skipped < count {
		line, _, err := readLine(reader)
		line = strings.TrimRight(line, "\r\n")
		if matchDirective(skipWhitespace(line), "end-turn") {
			skipped++
		}
		if err != nil {
			return skipped == count
		}
	}
	return true
}

func playTurn(reader *bufio.Reader, diagnostics io.Writer, callback func(conversation.Event), tick func() bool) (scriptResult, error) {
	delayMS := int64(0)
	usage := unreportedUsage()
	sawDirective := false
	for {
		line, hasLine, err := readLine(reader)
		if !hasLine && err == io.EOF {
			if !sawDirective {
				return scriptExhausted, nil
			}
			emitDone(callback, usage)
			return scriptTurnComplete, nil
		}
		if err != nil && err != io.EOF {
			return scriptAborted, err
		}
		if tick != nil && tick() {
			return scriptAborted, transport.ErrCancelled
		}
		line = strings.TrimRight(line, "\r\n")
		if blankOrComment(line) {
			continue
		}
		directive := skipWhitespace(line)
		if matchDirective(directive, "end-turn") {
			emitDone(callback, usage)
			return scriptTurnComplete, nil
		}
		sawDirective = true
		argument := directiveArgument(directive)
		switch {
		case hasDirective(directive, "delay"):
			delayMS = parseLong(argument, 0)
			if delayMS < 0 {
				delayMS = 0
			}
		case hasDirective(directive, "text"):
			if err := emitScriptText(argument, conversation.EventTextDelta, delayMS, callback, tick); err != nil {
				return scriptAborted, err
			}
		case hasDirective(directive, "reasoning"):
			if err := emitScriptText(argument, conversation.EventReasoningDelta, delayMS, callback, tick); err != nil {
				return scriptAborted, err
			}
		case matchDirective(directive, "space"):
			if err := sleep(delayMS, tick); err != nil {
				return scriptAborted, err
			}
			emit(callback, conversation.Event{Kind: conversation.EventTextDelta, Text: " "})
		case hasDirective(directive, "tool"):
			if err := emitScriptTool(argument, line, delayMS, diagnostics, callback, tick); err != nil {
				return scriptAborted, err
			}
		case hasDirective(directive, "usage"):
			usage = parseUsage(argument)
		default:
			if diagnostics != nil {
				_, _ = fmt.Fprintf(diagnostics, "gax mock: unknown directive: %s\n", line)
			}
		}
	}
}

func readLine(reader *bufio.Reader) (line string, hasLine bool, err error) {
	line, err = reader.ReadString('\n')
	return line, line != "", err
}

func blankOrComment(line string) bool {
	line = skipWhitespace(line)
	return line == "" || strings.HasPrefix(line, "#")
}

func skipWhitespace(text string) string { return strings.TrimLeft(text, " \t") }

func matchDirective(line, name string) bool {
	if !strings.HasPrefix(line, name) {
		return false
	}
	return len(line) == len(name) || line[len(name)] == ' ' || line[len(name)] == '\t'
}

func hasDirective(line, name string) bool { return matchDirective(line, name) }

func directiveArgument(line string) string {
	index := strings.IndexAny(line, " \t")
	if index < 0 {
		return ""
	}
	return skipWhitespace(line[index:])
}

func parseLong(text string, fallback int64) int64 {
	text = strings.TrimLeft(text, " \t")
	index := strings.IndexAny(text, " \t")
	if index >= 0 {
		if strings.Trim(text[index:], " \t") != "" {
			return fallback
		}
		text = text[:index]
	}
	if text == "" {
		return fallback
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func emitScriptText(text string, kind conversation.EventKind, delayMS int64,
	callback func(conversation.Event), tick func() bool) error {
	if err := sleep(delayMS, tick); err != nil {
		return err
	}
	decoded := expandCWD(decodeEscapes(text))
	return emitChunked(decoded, kind, delayMS, callback, tick)
}

func emitScriptTool(spec, line string, delayMS int64, diagnostics io.Writer,
	callback func(conversation.Event), tick func() bool) error {
	index := strings.IndexAny(spec, " \t")
	if index < 0 || skipWhitespace(spec[index:]) == "" {
		if diagnostics != nil {
			_, _ = fmt.Fprintf(diagnostics, "gax mock: 'tool' needs name and JSON args: %s\n", line)
		}
		return nil
	}
	name := spec[:index]
	args := skipWhitespace(spec[index:])
	if err := sleep(delayMS, tick); err != nil {
		return err
	}
	return emitToolCall(name, expandCWD(args), delayMS, callback, tick)
}

func emitChunked(text string, kind conversation.EventKind, delayMS int64,
	callback func(conversation.Event), tick func() bool) error {
	for offset := 0; offset < len(text); {
		if tick != nil && tick() {
			return transport.ErrCancelled
		}
		end := offset + textChunkBytes
		if end > len(text) {
			end = len(text)
		} else if end < len(text) {
			for end > offset && end < len(text) && text[end]&0xc0 == 0x80 {
				end--
			}
			if end == offset {
				end = offset + textChunkBytes
			}
		}
		eventText := text[offset:end]
		emit(callback, conversation.Event{Kind: kind, Text: eventText})
		offset = end
		if offset < len(text) {
			if err := sleep(delayMS, tick); err != nil {
				return err
			}
		}
	}
	return nil
}

func emitToolCall(name, arguments string, delayMS int64, callback func(conversation.Event), tick func() bool) error {
	id := uuid()
	emit(callback, conversation.Event{Kind: conversation.EventToolCallStart, ID: &id, Name: name})
	if err := sleep(delayMS, tick); err != nil {
		return err
	}
	emit(callback, conversation.Event{Kind: conversation.EventToolCallDelta, ID: &id, Delta: arguments})
	emit(callback, conversation.Event{Kind: conversation.EventToolCallEnd, ID: &id})
	return nil
}

func uuid() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// The operating system random source is expected to be available; retain a valid id shape
		// even on an unusual failure so the mock can still exercise tool handling.
		stamp := uint64(time.Now().UnixNano())
		for index := range raw {
			raw[index] = byte(stamp >> (uint(index%8) * 8))
		}
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func emitDone(callback func(conversation.Event), usage *conversation.Usage) {
	stopReason := "end_turn"
	emit(callback, conversation.Event{Kind: conversation.EventDone, StopReason: &stopReason, Usage: usage})
}

func emitExhausted(callback func(conversation.Event), tick func() bool) error {
	if err := emitChunked("Script exhausted — no more turns.", conversation.EventTextDelta, 0, callback, tick); err != nil {
		return err
	}
	emitDone(callback, unreportedUsage())
	return nil
}

func unreportedUsage() *conversation.Usage { return &conversation.Usage{} }

func parseUsage(spec string) *conversation.Usage {
	usage := &conversation.Usage{}
	for _, token := range strings.Fields(spec) {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			continue
		}
		if key == "cost" {
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) {
				usage.Cost = &parsed
			}
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "in", "input":
			usage.InputTokens = &parsed
		case "out", "output":
			usage.OutputTokens = &parsed
		case "cached":
			usage.CachedTokens = &parsed
		case "cache_write":
			usage.CacheWriteTokens = &parsed
		case "cache_write_1h":
			usage.CacheWrite1HTokens = &parsed
		}
	}
	return usage
}

func decodeEscapes(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for index := 0; index < len(text); {
		if text[index] == '\\' && index+1 < len(text) {
			switch text[index+1] {
			case 'n':
				builder.WriteByte('\n')
				index += 2
				continue
			case 't':
				builder.WriteByte('\t')
				index += 2
				continue
			case '\\':
				builder.WriteByte('\\')
				index += 2
				continue
			}
		}
		builder.WriteByte(text[index])
		index++
	}
	return builder.String()
}

func expandCWD(text string) string {
	if !strings.Contains(text, "{{CWD}}") {
		return text
	}
	cwd, err := os.Getwd()
	if err != nil {
		return text
	}
	return strings.ReplaceAll(text, "{{CWD}}", cwd)
}

func sleep(milliseconds int64, tick func() bool) error {
	for milliseconds > 0 {
		if tick != nil && tick() {
			return transport.ErrCancelled
		}
		step := milliseconds
		if step > 50 {
			step = 50
		}
		time.Sleep(time.Duration(step) * time.Millisecond)
		milliseconds -= step
	}
	if tick != nil && tick() {
		return transport.ErrCancelled
	}
	return nil
}

func emit(callback func(conversation.Event), event conversation.Event) {
	if callback != nil {
		callback(event)
	}
}

func interactiveResponse(request coreprovider.Context, callback func(conversation.Event), tick func() bool) error {
	last := lastContentItem(request.Items)
	if last == nil {
		emitDone(callback, unreportedUsage())
		return nil
	}
	if last.Kind == conversation.KindToolResult {
		if err := emitChunked("Tool finished — awaiting next instruction.", conversation.EventTextDelta, 0, callback, tick); err != nil {
			return err
		}
		emitDone(callback, unreportedUsage())
		return nil
	}

	userMessage := lastItemOfKind(request.Items, conversation.KindUser)
	if userMessage == nil || userMessage.Text == nil || *userMessage.Text == "" {
		if err := emitChunked("Hello.", conversation.EventTextDelta, 0, callback, tick); err != nil {
			return err
		}
		emitDone(callback, unreportedUsage())
		return nil
	}
	message := *userMessage.Text
	toolName := "bash"
	if messageStartsWith(message, "read") {
		toolName = "read"
	}
	quoted, ok := extractBacktickText(message)
	if ok && hasTool(request.Tools, toolName) {
		key := "command"
		if toolName == "read" {
			key = "path"
		}
		arguments := fmt.Sprintf("{\"%s\":\"%s\"}", key, escapeJSONString(quoted))
		if err := emitChunked("Sure, on it.", conversation.EventTextDelta, 0, callback, tick); err != nil {
			return err
		}
		if err := emitToolCall(toolName, arguments, 0, callback, tick); err != nil {
			return err
		}
		emitDone(callback, unreportedUsage())
		return nil
	}
	if err := emitChunked("You said: "+message, conversation.EventTextDelta, 0, callback, tick); err != nil {
		return err
	}
	emitDone(callback, unreportedUsage())
	return nil
}

func lastContentItem(items []conversation.Item) *conversation.Item {
	for index := len(items) - 1; index >= 0; index-- {
		kind := items[index].Kind
		if kind != conversation.KindTurnBoundary && kind != conversation.KindTurnUsage {
			return &items[index]
		}
	}
	return nil
}

func lastItemOfKind(items []conversation.Item, kind conversation.Kind) *conversation.Item {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].Kind == kind {
			return &items[index]
		}
	}
	return nil
}

func extractBacktickText(text string) (string, bool) {
	open := strings.IndexByte(text, '`')
	if open < 0 {
		return "", false
	}
	close := strings.IndexByte(text[open+1:], '`')
	if close < 0 {
		return "", false
	}
	return text[open+1 : open+1+close], true
}

func messageStartsWith(message, verb string) bool {
	message = skipWhitespace(message)
	if len(message) < len(verb) || !strings.EqualFold(message[:len(verb)], verb) {
		return false
	}
	if len(message) == len(verb) {
		return true
	}
	switch message[len(verb)] {
	case ' ', '\t', '`':
		return true
	default:
		return false
	}
}

func hasTool(tools []coreprovider.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func escapeJSONString(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for len(text) > 0 {
		runeValue, size := utf8.DecodeRuneInString(text)
		if runeValue == utf8.RuneError && size == 1 {
			builder.WriteByte(text[0])
			text = text[1:]
			continue
		}
		switch runeValue {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if runeValue < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, runeValue))
			} else {
				builder.WriteString(string(runeValue))
			}
		}
		text = text[size:]
	}
	return builder.String()
}

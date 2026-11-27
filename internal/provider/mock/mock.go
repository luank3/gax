// Package mock implements hax's scripted and heuristic mock provider.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
)

// Client replays scripted turns from ScriptPath or falls back to heuristic responses.
type Client struct {
	mu     sync.Mutex
	turns  [][]string
	cursor int
	cwd    string
}

// New constructs a mock provider client. If scriptPath is non-empty, it loads the script.
func New(scriptPath, cwd string) (*Client, error) {
	c := &Client{cwd: cwd}
	if strings.TrimSpace(scriptPath) != "" {
		raw, err := os.ReadFile(scriptPath)
		if err != nil {
			return nil, fmt.Errorf("read mock script %q: %w", scriptPath, err)
		}
		c.turns = parseTurns(string(raw))
	}
	return c, nil
}

func (c *Client) ID() string { return "mock" }

func parseTurns(raw string) [][]string {
	var turns [][]string
	var cur []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "end-turn" {
			turns = append(turns, cur)
			cur = nil
			continue
		}
		cur = append(cur, trimmed)
	}
	if len(cur) > 0 {
		turns = append(turns, cur)
	}
	return turns
}

func (c *Client) Stream(ctx context.Context, req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	c.mu.Lock()
	var lines []string
	hasScript := len(c.turns) > 0
	if hasScript && c.cursor < len(c.turns) {
		lines = c.turns[c.cursor]
		c.cursor++
	}
	c.mu.Unlock()

	if !hasScript {
		return c.streamHeuristic(req, emit)
	}
	return c.streamScript(ctx, lines, req, emit)
}

func (c *Client) streamScript(ctx context.Context, lines []string, req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	var delay time.Duration
	var usage *core.Usage
	callSeq := 0

	for _, line := range lines {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.cwd != "" {
			line = strings.ReplaceAll(line, "{{CWD}}", c.cwd)
		}
		verb, rest, _ := strings.Cut(line, " ")
		switch verb {
		case "delay":
			if ms, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && ms >= 0 {
				delay = time.Duration(ms) * time.Millisecond
			}
		case "space":
			if err := waitDelay(ctx, delay); err != nil {
				return err
			}
			emit(provider.StreamEvent{Kind: provider.EventText, Text: " "})
		case "text":
			decoded := decodeEscapes(rest)
			for _, chunk := range chunkText(decoded, 12) {
				if err := waitDelay(ctx, delay); err != nil {
					return err
				}
				emit(provider.StreamEvent{Kind: provider.EventText, Text: chunk})
			}
		case "reasoning":
			decoded := decodeEscapes(rest)
			for _, chunk := range chunkText(decoded, 12) {
				if err := waitDelay(ctx, delay); err != nil {
					return err
				}
				emit(provider.StreamEvent{Kind: provider.EventReasoning, Text: chunk})
			}
		case "tool":
			name, args, _ := strings.Cut(strings.TrimSpace(rest), " ")
			if args == "" {
				args = "{}"
			}
			callSeq++
			callID := fmt.Sprintf("mock_call_%d", callSeq)
			if err := waitDelay(ctx, delay); err != nil {
				return err
			}
			emit(provider.StreamEvent{Kind: provider.EventToolCallStart, CallID: callID, ToolName: name})
			emit(provider.StreamEvent{Kind: provider.EventToolCallDone, CallID: callID, ToolName: name, Arguments: args})
		case "usage":
			usage = parseUsageDirective(rest)
		case "overflow":
			limit := int64(0)
			if strings.HasPrefix(strings.TrimSpace(rest), "limit=") {
				limit, _ = strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(rest), "limit="), 10, 64)
			}
			if limit == 0 || estimateTokens(req) > limit {
				// Rewind cursor so compaction recovery replays this turn once it fits.
				c.mu.Lock()
				if c.cursor > 0 {
					c.cursor--
				}
				c.mu.Unlock()
				return &provider.OverflowError{Message: "mock context length exceeded"}
			}
		}
	}
	if usage != nil {
		emit(provider.StreamEvent{Kind: provider.EventUsage, Usage: usage})
	}
	emit(provider.StreamEvent{Kind: provider.EventDone})
	return nil
}

func (c *Client) streamHeuristic(req provider.StreamRequest, emit func(provider.StreamEvent)) error {
	lastUser := ""
	for i := len(req.Items) - 1; i >= 0; i-- {
		if req.Items[i].Kind == core.KindUser {
			lastUser = req.Items[i].Text
			break
		}
	}
	if strings.HasPrefix(lastUser, "run `") && strings.HasSuffix(lastUser, "`") {
		cmd := strings.TrimSuffix(strings.TrimPrefix(lastUser, "run `"), "`")
		args, _ := json.Marshal(map[string]string{"command": cmd})
		emit(provider.StreamEvent{Kind: provider.EventToolCallStart, CallID: "mock_1", ToolName: "bash"})
		emit(provider.StreamEvent{Kind: provider.EventToolCallDone, CallID: "mock_1", ToolName: "bash", Arguments: string(args)})
	} else {
		emit(provider.StreamEvent{Kind: provider.EventText, Text: lastUser})
	}
	emit(provider.StreamEvent{Kind: provider.EventUsage, Usage: &core.Usage{Input: 10, Output: 5, InTokens: 10}})
	emit(provider.StreamEvent{Kind: provider.EventDone})
	return nil
}

func parseUsageDirective(s string) *core.Usage {
	u := &core.Usage{}
	for _, field := range strings.Fields(s) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "in":
			u.Input, _ = strconv.ParseInt(v, 10, 64)
		case "out":
			u.Output, _ = strconv.ParseInt(v, 10, 64)
		case "cached":
			u.Cached, _ = strconv.ParseInt(v, 10, 64)
		case "cache_write", "cache_write_1h":
			n, _ := strconv.ParseInt(v, 10, 64)
			u.CacheWrite += n
		case "cost":
			u.CostTotal, _ = strconv.ParseFloat(v, 64)
		}
	}
	u.InTokens = max(0, u.Input-u.Cached)
	return u
}

func estimateTokens(req provider.StreamRequest) int64 {
	bytes := len(req.SystemPrompt)
	for _, it := range req.Items {
		bytes += len(it.Text) + len(it.Arguments) + len(it.Output)
	}
	return int64(bytes+3) / 4
}

func decodeEscapes(s string) string {
	r := strings.NewReplacer(`\\`, "\x00", `\n`, "\n", `\t`, "\t")
	return strings.ReplaceAll(r.Replace(s), "\x00", `\`)
}

func chunkText(s string, size int) []string {
	if len(s) <= size {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		n := min(size, len(s))
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

func waitDelay(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

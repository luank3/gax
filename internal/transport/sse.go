// Package transport provides streaming HTTP/SSE requests, retry/backoff,
// idle timeouts, and OS CA root fallback for provider and catalog clients.
package transport

import (
	"bufio"
	"io"
	"strings"
)

// SSEEvent is a single parsed Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

const maxSSELineBytes = 4 << 20 // 4 MiB bounded line buffer

// ParseSSE streams Server-Sent Events from r, calling fn for each complete event.
// Returns nil on clean EOF, or the first non-EOF read/callback error.
func ParseSSE(r io.Reader, fn func(SSEEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)

	var ev SSEEvent
	var dataLines []string
	hasData := false

	flush := func() error {
		if !hasData && ev.Event == "" && ev.ID == "" {
			return nil
		}
		if hasData {
			ev.Data = strings.Join(dataLines, "\n")
		}
		err := fn(ev)
		ev = SSEEvent{}
		dataLines = dataLines[:0]
		hasData = false
		return err
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, val, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(val, " ") {
			val = val[1:]
		}
		switch field {
		case "event":
			ev.Event = val
		case "data":
			dataLines = append(dataLines, val)
			hasData = true
		case "id":
			ev.ID = val
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

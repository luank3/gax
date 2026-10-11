// Package wiretrace writes redacted HTTP and SSE traffic in the GAX_TRACE markdown format.
package wiretrace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger is safe for concurrent provider discovery, streaming, and authentication requests.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	secrets []string
	start   time.Time
	last    time.Time
}

func New() *Logger { return &Logger{} }

var defaultLogger = New()

// Default is the process-wide logger used by providers and the HTTP transport.
func Default() *Logger { return defaultLogger }

// Configure truncates and enables the trace destination. An empty path disables tracing.
func (logger *Logger) Configure(path string) error {
	if logger == nil {
		return nil
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if logger.file != nil {
		_ = logger.file.Close()
		logger.file = nil
	}
	logger.start = time.Time{}
	logger.last = time.Time{}
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	logger.file = file
	return nil
}

func (logger *Logger) Enabled() bool {
	if logger == nil {
		return false
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return logger.file != nil
}

// RegisterSecret adds a credential value to redact from all later headers and bodies.
func (logger *Logger) RegisterSecret(secret string) {
	if logger == nil || secret == "" {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	for _, existing := range logger.secrets {
		if existing == secret {
			return
		}
	}
	logger.secrets = append(logger.secrets, secret)
}

// Request records an HTTP request. Header strings use the conventional "Name: value" form.
func (logger *Logger) Request(method, url string, headers []string, body []byte) {
	logger.withFile(func(file *os.File) {
		_, _ = fmt.Fprintf(file, "\n## %s %s  (%s)\n\n", method, url, logger.timestampLocked())
		var headerBlock strings.Builder
		for _, header := range headers {
			if colon := strings.IndexByte(header, ':'); colon >= 0 && logger.sensitiveHeaderLocked(header, colon) {
				headerBlock.WriteString(header[:colon])
				headerBlock.WriteString(": <redacted>\n")
			} else {
				headerBlock.WriteString(header)
				headerBlock.WriteByte('\n')
			}
		}
		if headerBlock.Len() > 0 {
			writeFenced(file, "http", []byte(headerBlock.String()))
		}
		if len(body) > 0 {
			logger.emitBodyLocked(file, body)
		}
	})
}

// ResponseStatus records an HTTP status and optional error body.
func (logger *Logger) ResponseStatus(status int, errorBody string) {
	logger.withFile(func(file *os.File) {
		_, _ = fmt.Fprintf(file, "\n**HTTP %d**  (%s)\n", status, logger.timestampLocked())
		if errorBody != "" {
			_, _ = file.Write([]byte{'\n'})
			logger.emitBodyLocked(file, []byte(errorBody))
		}
	})
}

// SSEEvent records one parsed server-sent event.
func (logger *Logger) SSEEvent(name, data string) {
	logger.withFile(func(file *os.File) {
		if name == "" {
			name = "(unnamed)"
		}
		_, _ = fmt.Fprintf(file, "\n### event: %s  (%s)\n\n", name, logger.timestampLocked())
		if data != "" {
			writeJSONOrText(file, []byte(data))
		}
	})
}

func (logger *Logger) Close() error {
	if logger == nil {
		return nil
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if logger.file == nil {
		return nil
	}
	err := logger.file.Close()
	logger.file = nil
	return err
}

func (logger *Logger) withFile(write func(*os.File)) {
	if logger == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if logger.file != nil {
		write(logger.file)
	}
}

func (logger *Logger) timestampLocked() string {
	now := time.Now()
	if logger.start.IsZero() {
		logger.start, logger.last = now, now
	}
	absolute := now.Sub(logger.start).Milliseconds()
	delta := now.Sub(logger.last).Milliseconds()
	logger.last = now
	return fmt.Sprintf("t+%d.%03ds dt+%d.%03ds", absolute/1000, absolute%1000, delta/1000, delta%1000)
}

func (logger *Logger) sensitiveHeaderLocked(header string, colon int) bool {
	name := strings.TrimSpace(header[:colon])
	if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "x-api-key") ||
		strings.EqualFold(name, "api-key") {
		return true
	}
	for _, secret := range logger.secrets {
		if strings.Contains(header, secret) {
			return true
		}
	}
	return false
}

func (logger *Logger) redactLocked(value []byte) []byte {
	var output bytes.Buffer
	position := 0
	for position < len(value) {
		matchAt, matchLen := -1, 0
		for _, secret := range logger.secrets {
			if at := bytes.Index(value[position:], []byte(secret)); at >= 0 {
				absolute := position + at
				if matchAt < 0 || absolute < matchAt || absolute == matchAt && len(secret) > matchLen {
					matchAt, matchLen = absolute, len(secret)
				}
			}
		}
		if matchAt < 0 {
			break
		}
		output.Write(value[position:matchAt])
		output.WriteString("<redacted>")
		position = matchAt + matchLen
	}
	if output.Len() == 0 {
		return value
	}
	output.Write(value[position:])
	return output.Bytes()
}

func (logger *Logger) emitBodyLocked(file *os.File, body []byte) {
	writeJSONOrText(file, logger.redactLocked(body))
}

func writeJSONOrText(file *os.File, body []byte) {
	var pretty bytes.Buffer
	if jsonIndent(&pretty, body) {
		writeFenced(file, "json", pretty.Bytes())
		return
	}
	writeFenced(file, "text", body)
}

// jsonIndent is split out so trace output preserves the order of members in the wire body.
func jsonIndent(destination *bytes.Buffer, body []byte) bool {
	if !json.Valid(body) {
		return false
	}
	if err := json.Indent(destination, body, "", "  "); err != nil {
		return false
	}
	return true
}

func writeFenced(file *os.File, language string, content []byte) {
	fence := 3
	longest, current := 0, 0
	for _, char := range content {
		if char == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	if longest >= fence {
		fence = longest + 1
	}
	marks := strings.Repeat("`", fence)
	_, _ = fmt.Fprintf(file, "%s%s\n", marks, language)
	_, _ = file.Write(content)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		_, _ = file.Write([]byte{'\n'})
	}
	_, _ = fmt.Fprintf(file, "%s\n", marks)
}

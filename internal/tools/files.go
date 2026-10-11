package tools

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/skill"
	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
)

const (
	maxReadLines      = 2000
	maxLineWidth      = 500
	maxImageBytes     = 5 * 1024 * 1024 / 4 * 3
	maxImageSide      = 8000
	maxEditBytes      = 4 * 1024 * 1024
	readLineDelimiter = "→"
)

func (registry *Registry) runRead(arguments string, imageInput int, _ DisplaySink) Result {
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	path, ok := object.string("path")
	if !ok || path == "" {
		return Result{Text: "missing 'path' argument"}
	}
	offset, offsetProvided, validationError := object.integer("offset")
	if validationError != "" {
		return Result{Text: validationError}
	}
	if offset == 0 {
		offset = 1
	}
	limit, limitProvided, validationError := object.integer("limit")
	if validationError != "" {
		return Result{Text: validationError}
	}

	// Built-in skills live in the binary, so they are resolved before any filesystem access. The
	// whole document is returned: it is a fixed-size reference the cap should not slice, and the
	// model-facing shape stays the numbered one the read tool promises.
	if strings.HasPrefix(path, skill.Scheme) {
		body, ok := skill.Read(path)
		if !ok {
			return Result{Text: fmt.Sprintf("error reading %s: no such built-in skill", path)}
		}
		return Result{Text: sanitizeUTF8(numberLines(body))}
	}
	path = expandHome(path)
	info, err := os.Stat(path)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %s", path, err)}
	}
	if !info.Mode().IsRegular() {
		return Result{Text: fmt.Sprintf("%s exists but is not a regular file", path)}
	}
	if kind, _, _ := sniffImageHeader(path); kind != "" {
		return registry.readImage(path, info.Size(), kind, imageInput)
	}
	outputCap := registry.outputLimit()
	if info.Size() > int64(outputCap) && !offsetProvided && !limitProvided {
		return Result{Text: fmt.Sprintf("%s is %d bytes; cap is %d. Pass offset/limit to read a slice, or use bash with grep/head/tail.", path, info.Size(), outputCap)}
	}
	binary, err := hasBinaryPrefix(path)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %s", path, err)}
	}
	if binary {
		return Result{Text: fmt.Sprintf("%s appears to be binary (NUL byte found in first 8 KiB)", path)}
	}
	text, truncation, lineCount, pastEOF, err := readTextSlice(path, offset, limit, limitProvided, outputCap)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %s", path, err)}
	}
	if pastEOF {
		plural := "s"
		if lineCount == 1 {
			plural = ""
		}
		return Result{Text: fmt.Sprintf("(file has %d line%s; offset %d is past EOF)", lineCount, plural, offset)}
	}
	text = capLineLengths(text, maxLineWidth)
	text = sanitizeUTF8(text)
	switch truncation {
	case truncateBytes:
		text += fmt.Sprintf("\n\n[truncated at %d bytes; file is larger — pass offset/limit to read more]", outputCap)
	case truncateLines:
		text += fmt.Sprintf("\n\n[truncated at %d lines; file has more — pass offset/limit to read more]", maxReadLines)
	}
	return Result{Text: text}
}

// numberLines renders text the way readTextSlice renders file content, so a built-in document is
// indistinguishable from a file read apart from its path.
func numberLines(text string) string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out strings.Builder
	out.Grow(len(text) + 8*len(lines))
	for index, line := range lines {
		fmt.Fprintf(&out, "%6d%s%s", index+1, readLineDelimiter, line)
	}
	return out.String()
}

func hasBinaryPrefix(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	var prefix [8192]byte
	n, err := io.ReadFull(file, prefix[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return bytes.IndexByte(prefix[:n], 0) >= 0, nil
}

type readTruncation uint8

const (
	truncateNone readTruncation = iota
	truncateBytes
	truncateLines
)

// readLogicalLine consumes a full line while retaining at most keep bytes. It prevents a single
// giant line from forcing an equally giant allocation.
func readLogicalLine(reader *bufio.Reader, keep int) (line []byte, total int64, eof bool, err error) {
	for {
		chunk, readErr := reader.ReadSlice('\n')
		total += int64(len(chunk))
		if len(line) < keep {
			n := keep - len(line)
			if n > len(chunk) {
				n = len(chunk)
			}
			line = append(line, chunk[:n]...)
		}
		if readErr == nil {
			return line, total, false, nil
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return line, total, true, nil
		}
		return line, total, false, readErr
	}
}

func readTextSlice(path string, offset, limit int64, limitProvided bool, outputCap int) (string, readTruncation, int64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", truncateNone, 0, false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 32*1024)
	output := make([]byte, 0, min(outputCap, 16*1024))
	lineCount := int64(0)
	returnedLines := int64(0)
	lineNumber := int64(1)
	truncation := truncateNone
	lineLimit := int64(maxReadLines)
	if limitProvided && limit < lineLimit {
		lineLimit = limit
	}
	for {
		remaining := outputCap - len(output)
		retain := remaining
		if retain < 0 {
			retain = 0
		}
		line, total, eof, lineErr := readLogicalLine(reader, retain)
		if lineErr != nil {
			return "", truncateNone, lineCount, false, lineErr
		}
		if total == 0 {
			break
		}
		lineCount++
		if lineNumber < offset {
			lineNumber++
			if eof {
				break
			}
			continue
		}

		prefix := fmt.Sprintf("%6d%s", lineNumber, readLineDelimiter)
		if len(line) > 0 {
			if len(prefix) >= outputCap-len(output) {
				truncation = truncateBytes
				break
			}
			output = append(output, prefix...)
			take := len(line)
			if take > outputCap-len(output) {
				take = outputCap - len(output)
			}
			output = append(output, line[:take]...)
			if take < len(line) {
				truncation = truncateBytes
				break
			}
		}
		if int64(len(line)) < total {
			truncation = truncateBytes
			break
		}
		returnedLines++
		lineNumber++
		if limitProvided && returnedLines >= limit {
			break
		}
		if returnedLines >= lineLimit {
			if limitProvided && limit <= maxReadLines {
				break
			}
			_, nextTotal, nextEOF, nextErr := readLogicalLine(reader, 0)
			if nextErr != nil {
				return "", truncateNone, lineCount, false, nextErr
			}
			if nextTotal > 0 {
				truncation = truncateLines
			}
			_ = nextEOF
			break
		}
		if len(output) >= outputCap {
			_, nextTotal, _, nextErr := readLogicalLine(reader, 0)
			if nextErr != nil {
				return "", truncateNone, lineCount, false, nextErr
			}
			if nextTotal > 0 {
				truncation = truncateBytes
			}
			break
		}
		if eof {
			break
		}
	}
	if returnedLines == 0 && truncation == truncateNone && (lineCount > 0 || offset > 1) {
		return "", truncateNone, lineCount, true, nil
	}
	return string(output), truncation, lineCount, false, nil
}

func capLineLengths(text string, maxWidth int) string {
	var output strings.Builder
	for len(text) > 0 {
		line, rest, found := strings.Cut(text, "\n")
		if len(line) > maxWidth {
			fmt.Fprintf(&output, "%s...[%d bytes elided]", line[:maxWidth], len(line)-maxWidth)
		} else {
			output.WriteString(line)
		}
		if !found {
			break
		}
		output.WriteByte('\n')
		text = rest
	}
	return output.String()
}

func sanitizeUTF8(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	var output strings.Builder
	for len(text) > 0 {
		runeValue, size := utf8.DecodeRuneInString(text)
		if runeValue == utf8.RuneError && size == 1 {
			output.WriteRune(utf8.RuneError)
			text = text[1:]
			continue
		}
		output.WriteString(text[:size])
		text = text[size:]
	}
	return output.String()
}

func (registry *Registry) readImage(path string, size int64, kind string, imageInput int) Result {
	if imageInput == 0 {
		return Result{Text: fmt.Sprintf("%s is an image, but the current model does not accept image input, so it was not attached. Ask the user to switch to a vision-capable model (or set image_input=on if this detection is wrong).", path)}
	}
	if size > maxImageBytes {
		return Result{Text: fmt.Sprintf("%s is %d bytes; images over %d bytes exceed provider limits — downscale it first.", path, size, maxImageBytes)}
	}
	data, truncated, err := systemfs.ReadFileCapped(path, maxImageBytes)
	if err != nil || truncated {
		return Result{Text: fmt.Sprintf("error reading %s: file changed while reading", path)}
	}
	mime, width, height, complete := imageInfo(data, kind)
	if !complete || width <= 0 || height <= 0 {
		return Result{Text: fmt.Sprintf("%s looks like %s but is truncated or malformed, so it was not attached. Check that the file is a complete image.", path, mime)}
	}
	if width > maxImageSide || height > maxImageSide {
		return Result{Text: fmt.Sprintf("%s is %dx%d; images over %dpx per side exceed provider limits — downscale it first.", path, width, height, maxImageSide)}
	}
	return Result{
		Text:   fmt.Sprintf("Read image %s (%s, %dx%d, %d bytes).", path, mime, width, height, len(data)),
		Images: []conversation.Image{{MIME: mime, Data: base64.StdEncoding.EncodeToString(data), Width: int64(width), Height: int64(height)}},
	}
}

func sniffImageHeader(path string) (string, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	header := make([]byte, 16)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", nil, err
	}
	header = header[:n]
	switch {
	case bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")):
		return "png", header, nil
	case bytes.HasPrefix(header, []byte("GIF87a")), bytes.HasPrefix(header, []byte("GIF89a")):
		return "gif", header, nil
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "jpeg", header, nil
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "webp", header, nil
	default:
		return "", header, nil
	}
}

func imageInfo(data []byte, kind string) (mime string, width, height int, complete bool) {
	mime = "image/" + kind
	if kind == "jpeg" {
		mime = "image/jpeg"
	}
	if kind != "webp" {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return mime, 0, 0, false
		}
		width, height = config.Width, config.Height
		switch kind {
		case "png":
			complete = len(data) >= 12 && bytes.HasSuffix(data, []byte("IEND\xaeB`\x82"))
		case "gif":
			complete = len(data) > 0 && data[len(data)-1] == ';'
		case "jpeg":
			complete = len(data) >= 2 && bytes.HasSuffix(data, []byte{0xff, 0xd9})
		}
		return mime, width, height, complete
	}
	if len(data) < 30 || string(data[8:12]) != "WEBP" {
		return mime, 0, 0, false
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8X":
		if len(data) < 30 {
			return mime, 0, 0, false
		}
		width = 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		height = 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return mime, 0, 0, false
		}
		width = 1 + (int(data[21]) | (int(data[22])&0x3f)<<8)
		height = 1 + ((int(data[22])>>6)&3 | int(data[23])<<2 | (int(data[24])&0x0f)<<10)
	case "VP8 ":
		for index := 20; index+9 <= len(data); index++ {
			if data[index] == 0x9d && data[index+1] == 0x01 && data[index+2] == 0x2a {
				width = int(data[index+3]) | int(data[index+4]&0x3f)<<8
				height = int(data[index+5]) | int(data[index+6]&0x3f)<<8
				break
			}
		}
	}
	if width <= 0 || height <= 0 {
		return mime, 0, 0, false
	}
	if len(data) < 12 || int(uint32(data[4])|uint32(data[5])<<8|uint32(data[6])<<16|uint32(data[7])<<24)+8 > len(data) {
		return mime, width, height, false
	}
	return mime, width, height, true
}

func (registry *Registry) runWrite(arguments string, _ int, display DisplaySink) Result {
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	rawPath, ok := object.string("path")
	if !ok || rawPath == "" {
		return Result{Text: "missing 'path' argument"}
	}
	content, ok := object.string("content")
	if !ok {
		return Result{Text: "missing 'content' argument"}
	}
	path := expandHome(rawPath)
	created, diff, err := writeWithDiff(path, []byte(content))
	if err != nil {
		return Result{Text: err.Error()}
	}
	if created {
		if display != nil && content != "" {
			_ = display([]byte(content))
			result := Result{OutputSummarizesDisplay: true}
			lineCount := countLines(content)
			plural := "s"
			if lineCount == 1 {
				plural = ""
			}
			bytePlural := "s"
			if len(content) == 1 {
				bytePlural = ""
			}
			result.Text = fmt.Sprintf("created %s (%d line%s, %d byte%s)", path, lineCount, plural, len(content), bytePlural)
			return result
		}
		lineCount := countLines(content)
		if len(content) == 0 {
			return Result{Text: fmt.Sprintf("created %s (empty)", path)}
		}
		plural := "s"
		if lineCount == 1 {
			plural = ""
		}
		bytePlural := "s"
		if len(content) == 1 {
			bytePlural = ""
		}
		return Result{Text: fmt.Sprintf("created %s (%d line%s, %d byte%s)", path, lineCount, plural, len(content), bytePlural)}
	}
	return Result{Text: diff}
}

func (registry *Registry) runEdit(arguments string, _ int, _ DisplaySink) Result {
	object, parseError := parseArguments(arguments)
	if parseError != "" {
		return Result{Text: parseError}
	}
	rawPath, ok := object.string("path")
	if !ok || rawPath == "" {
		return Result{Text: "missing 'path' argument"}
	}
	oldString, ok := object.string("old_string")
	if !ok {
		return Result{Text: "missing 'old_string' argument"}
	}
	newString, ok := object.string("new_string")
	if !ok {
		return Result{Text: "missing 'new_string' argument"}
	}
	if oldString == "" {
		return Result{Text: "'old_string' must be non-empty"}
	}
	if oldString == newString {
		return Result{Text: "'old_string' and 'new_string' are identical — nothing to do"}
	}
	path := expandHome(rawPath)
	info, statError := os.Stat(path)
	if statError == nil && !info.Mode().IsRegular() {
		return Result{Text: fmt.Sprintf("%s exists but is not a regular file", path)}
	}
	if statError == nil && info.Size() > maxEditBytes {
		return Result{Text: fmt.Sprintf("file %s is larger than %d bytes — refusing to edit", path, maxEditBytes)}
	}
	original, truncated, err := systemfs.ReadFileCapped(path, maxEditBytes)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %s", path, err)}
	}
	if truncated {
		return Result{Text: fmt.Sprintf("file %s is larger than %d bytes — refusing to edit", path, maxEditBytes)}
	}
	matches := bytes.Count(original, []byte(oldString))
	if matches == 0 {
		return Result{Text: "'old_string' not found in file"}
	}
	if matches > 1 && !object.boolean("replace_all") {
		return Result{Text: fmt.Sprintf("'old_string' matches %d places in %s — provide more context to disambiguate, or set replace_all=true", matches, path)}
	}
	updated := bytes.ReplaceAll(original, []byte(oldString), []byte(newString))
	_, diff, err := writeWithDiff(path, updated)
	if err != nil {
		return Result{Text: err.Error()}
	}
	return Result{Text: diff}
}

func expandHome(path string) string {
	return systempath.ExpandHome(path, os.Getenv("HOME"))
}

func relativizeToolPath(arguments string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &object) != nil || object == nil {
		return arguments
	}
	var path string
	if json.Unmarshal(object["path"], &path) != nil {
		return arguments
	}
	cwd, err := os.Getwd()
	if err != nil {
		return arguments
	}
	expanded := expandHome(path)
	relative, ok := relativizePath(expanded, cwd)
	if !ok {
		return arguments
	}
	encoded, err := json.Marshal(relative)
	if err != nil {
		return arguments
	}
	object["path"] = encoded
	rewritten, err := json.Marshal(object)
	if err != nil {
		return arguments
	}
	return string(rewritten)
}

func relativizePath(path, cwd string) (string, bool) {
	return systempath.Relativize(path, cwd)
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

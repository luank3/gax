package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/luank3/gax/internal/conversation"
)

type Meta struct {
	ID         *string
	CWD        *string
	Provider   *string
	Model      *string
	ModelLabel *string
	Effort     *string
	Preset     *string
	ForkedFrom *string
}

type Loaded struct {
	Items          []conversation.Item
	Retired        []conversation.Item
	WorkedMS       int64
	LastUserTurnMS *int64
	Meta           Meta
}

func ReadMeta(path string) (Meta, error) {
	var meta Meta
	err := readLines(path, func(line []byte) error {
		if !bytes.Contains(line, []byte(`"type"`)) || !utf8.Valid(line) {
			return nil
		}
		object, err := decodeObject(line)
		if err != nil {
			return nil
		}
		recordType := rawString(object["type"])
		if recordType == nil {
			return nil
		}
		switch *recordType {
		case "session":
			meta.ID = decodeString(object["id"])
			meta.CWD = decodeString(object["cwd"])
			meta.ForkedFrom = decodeString(object["forked_from"])
			applySelection(&meta, object)
		case "selection":
			applySelection(&meta, object)
		}
		return nil
	})
	if err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func LoadAll(path string) (Loaded, error) {
	loaded := Loaded{}
	var headerProvider, headerModel *string
	err := readLines(path, func(line []byte) error {
		if !utf8.Valid(line) {
			return nil
		}
		object, err := decodeObject(line)
		if err != nil {
			return nil
		}
		recordType := rawString(object["type"])
		if recordType != nil {
			switch *recordType {
			case "session":
				if headerProvider == nil {
					headerProvider = decodeString(object["provider"])
				}
				if headerModel == nil {
					headerModel = decodeString(object["model"])
				}
				loaded.Meta.ID = decodeString(object["id"])
				loaded.Meta.CWD = decodeString(object["cwd"])
				loaded.Meta.ForkedFrom = decodeString(object["forked_from"])
				applySelection(&loaded.Meta, object)
			case "selection":
				applySelection(&loaded.Meta, object)
			case "undo":
				applyUndo(&loaded, object)
			case "user_turn":
				applyUserTurn(&loaded, object)
			}
			return nil
		}

		encoded, err := json.Marshal(object)
		if err != nil {
			return nil
		}
		item, err := conversation.DecodeItem(encoded)
		if err != nil {
			return nil
		}
		if item.Kind == conversation.KindReasoning {
			if item.Provider == nil {
				item.Provider = cloneString(headerProvider)
			}
			if item.Model == nil {
				item.Model = cloneString(headerModel)
			}
		}
		loaded.Items = append(loaded.Items, item)
		return nil
	})
	if err != nil {
		return Loaded{}, err
	}
	loaded.Items = removeIncompleteToolCalls(loaded.Items)
	degradeExcessImages(loaded.Items)
	return loaded, nil
}

func Load(path string) ([]conversation.Item, Meta, error) {
	loaded, err := LoadAll(path)
	if err != nil {
		return nil, Meta{}, err
	}
	return loaded.Items, loaded.Meta, nil
}

func readLines(path string, consume func([]byte) error) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		syscall.Close(fd)
		return errors.New("cannot open session file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("session path is not a regular file")
	}

	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if err := consume(line); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func decodeObject(line []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("session record is not an object")
	}
	return object, nil
}

func rawString(raw json.RawMessage) *string {
	if len(raw) == 0 || raw[0] != '"' {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func decodeString(raw json.RawMessage) *string {
	return rawString(raw)
}

func applySelection(meta *Meta, object map[string]json.RawMessage) {
	meta.Provider = decodeString(object["provider"])
	meta.Model = decodeString(object["model"])
	meta.ModelLabel = decodeString(object["model_label"])
	meta.Effort = decodeString(object["effort"])
	meta.Preset = decodeString(object["preset"])
}

func applyUndo(loaded *Loaded, object map[string]json.RawMessage) {
	keep, ok := decodeInteger(object["keep_user_turns"])
	if !ok || keep < 0 {
		return
	}
	cut := len(loaded.Items)
	if keep <= int64(len(loaded.Items)) {
		cut = conversation.UserTurnCut(loaded.Items, int(keep))
	}
	loaded.Retired = append(loaded.Retired, loaded.Items[cut:]...)
	loaded.Items = loaded.Items[:cut]
	loaded.LastUserTurnMS = nil
}

func applyUserTurn(loaded *Loaded, object map[string]json.RawMessage) {
	elapsed, ok := decodeInteger(object["elapsed_ms"])
	if !ok || elapsed < 0 {
		return
	}
	loaded.LastUserTurnMS = &elapsed
	loaded.WorkedMS += elapsed
}

func decodeInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] == '"' || strings.ContainsAny(string(raw), ".eE") {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func removeIncompleteToolCalls(items []conversation.Item) []conversation.Item {
	results := make(map[string]bool)
	for _, item := range items {
		if item.Kind == conversation.KindToolResult && item.CallID != nil {
			results[*item.CallID] = true
		}
	}
	kept := items[:0]
	for _, item := range items {
		if item.Kind == conversation.KindToolCall && (item.CallID == nil || !results[*item.CallID]) {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func degradeExcessImages(items []conversation.Item) {
	const maxEncodedBytes = 20 * 1024 * 1024
	const maxImages = 20
	var encodedBytes, imageCount int
	for i := conversation.ContextFloor(items); i < len(items); i++ {
		item := &items[i]
		if len(item.Images) == 0 {
			continue
		}
		itemBytes := 0
		for _, image := range item.Images {
			itemBytes += len(image.Data)
		}
		if encodedBytes+itemBytes <= maxEncodedBytes && imageCount+len(item.Images) <= maxImages {
			encodedBytes += itemBytes
			imageCount += len(item.Images)
			continue
		}
		output := ""
		if item.Output != nil {
			output = *item.Output
		}
		for _, image := range item.Images {
			output += "\n" + ImagePlaceholder(image)
		}
		item.Output = &output
		item.Images = nil
	}
}

func ImagePlaceholder(image conversation.Image) string {
	bytes := len(image.Data) / 4 * 3
	var size string
	switch {
	case bytes >= 1024*1024:
		size = fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		size = fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	default:
		size = fmt.Sprintf("%d bytes", bytes)
	}
	mime := image.MIME
	if mime == "" {
		mime = "image"
	}
	if image.Width > 0 && image.Height > 0 {
		return fmt.Sprintf("[image: %s, %dx%d, %s]", mime, image.Width, image.Height, size)
	}
	return fmt.Sprintf("[image: %s, %s]", mime, size)
}

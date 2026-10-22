package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	systemfs "github.com/luank3/gax/internal/system/fs"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const pickerLabelScanCap = 64 * 1024

type PickerLabel struct {
	Prompt     string
	Provider   string
	Model      string
	Effort     string
	Preset     string
	GitBranch  string
	GitSubject string
}

// ReadPickerLabel reads only the bounded prefix needed to build a picker row. Invalid or
// incomplete records are ignored so one damaged session does not prevent other sessions loading.
func ReadPickerLabel(path string, maxCells int) PickerLabel {
	var label PickerLabel
	file, err := systemfs.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return label
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return label
	}
	reader := bufio.NewReader(io.LimitReader(file, pickerLabelScanCap))
	sawCompactionSeed := false
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			object, decodeErr := decodeObject(line)
			if decodeErr == nil {
				if readPickerSelection(&label, object) {
					// Selection records after the opening prompt do not describe the session start.
				} else if recordOrigin(object) != "" {
					if recordOrigin(object) == "compact_seed" {
						sawCompactionSeed = true
					}
				} else if rawString(object["kind"]) != nil && *rawString(object["kind"]) == "user" {
					if prompt := decodeString(object["text"]); prompt != nil {
						flattened := terminalwidth.FlattenForDisplay(*prompt)
						label.Prompt = terminalwidth.TruncateForDisplay(flattened, maxCells)
						break
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if label.Prompt == "" && sawCompactionSeed {
		label.Prompt = "(compacted)"
	}
	return label
}

func readPickerSelection(label *PickerLabel, object map[string]json.RawMessage) bool {
	// See recordOrigin below for user entries; records with a type field are metadata.
	recordType := rawString(object["type"])
	if recordType == nil || (*recordType != "session" && *recordType != "selection") {
		return false
	}
	label.Provider = stringValue(object["provider"])
	label.Model = stringValue(object["model_label"])
	if label.Model == "" {
		label.Model = stringValue(object["model"])
	}
	label.Effort = stringValue(object["effort"])
	label.Preset = stringValue(object["preset"])
	if *recordType == "session" {
		label.GitBranch = stringValue(object["git_branch"])
		label.GitSubject = stringValue(object["git_subject"])
	}
	return true
}

func recordOrigin(object map[string]json.RawMessage) string {
	if origin := rawString(object["origin"]); origin != nil {
		return *origin
	}
	return ""
}

func stringValue(raw json.RawMessage) string {
	if value := decodeString(raw); value != nil {
		return *value
	}
	return ""
}

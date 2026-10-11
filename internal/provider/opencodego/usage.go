// Package opencodego implements OpenCode Go's quota-window usage report.
package opencodego

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luank3/gax/internal/provider/usagerender"
	"github.com/luank3/gax/internal/transport"
)

const (
	usageTimeout      = 30 * time.Second
	maxBodySize       = 1 << 20
	maxUsageWindows   = 8
	fallbackTextWidth = 100
)

var errUsageShape = errors.New("unrecognized usage response shape (no usage windows)")

// Window is one provider-reported quota window and its reset time.
type Window struct {
	Label   string
	Percent float64
	ResetAt time.Time
	Note    *string
}

// UsageReport contains valid usage windows in the order returned by OpenCode Go.
type UsageReport struct {
	Windows []Window
}

type objectMember struct {
	name  string
	value json.RawMessage
}

// ParseUsage reads valid quota windows from a /usage response, preserving their source order.
func ParseUsage(body []byte) (UsageReport, error) {
	var rootJSON json.RawMessage
	if err := json.Unmarshal(body, &rootJSON); err != nil {
		return UsageReport{}, fmt.Errorf("usage response is not valid JSON: %w", err)
	}
	usageJSON := objectValue(rootJSON, "usage")
	usageMembers := objectMembers(usageJSON)
	report := UsageReport{Windows: make([]Window, 0, maxUsageWindows)}
	for _, member := range usageMembers {
		if len(report.Windows) == maxUsageWindows {
			break
		}
		windowMembers := objectMembers(member.value)
		if windowMembers == nil {
			continue
		}
		values := make(map[string]json.RawMessage, len(windowMembers))
		for _, value := range windowMembers {
			values[value.name] = value.value
		}
		percent := jsonNumber(values["percent"])
		resetsAt := jsonString(values["resetsAt"])
		if percent == nil || resetsAt == "" {
			continue
		}
		resetAt, ok := parseUTCTimestamp(resetsAt)
		if !ok {
			continue
		}
		window := Window{Label: member.name, Percent: *percent, ResetAt: resetAt}
		if status, ok := jsonStringValue(values["status"]); ok && status != "ok" {
			window.Note = &status
		}
		report.Windows = append(report.Windows, window)
	}
	if len(report.Windows) == 0 {
		return UsageReport{}, errUsageShape
	}
	return report, nil
}

// QueryUsage fetches the OpenCode Go usage endpoint with the provider's usage headers.
func QueryUsage(ctx context.Context, baseURL string, headers []string, hasAPIKey bool,
	client *transport.Client, tick func() bool) (UsageReport, error) {
	if !hasAPIKey {
		return UsageReport{}, errors.New("no OPENCODE_API_KEY configured")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return UsageReport{}, errors.New("OpenCode provider has no base URL")
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()

	requestHeaders := make([]string, 0, len(headers)+1)
	requestHeaders = append(requestHeaders, "Accept: application/json")
	requestHeaders = append(requestHeaders, headers...)
	url := baseURL + "/usage"
	body, status, err := client.Get(ctx, url, requestHeaders, usageTimeout, maxBodySize, tick)
	if err != nil {
		if status == http.StatusUnauthorized {
			return UsageReport{}, errors.New("opencode rejected the configured API key (401)")
		}
		return UsageReport{}, fmt.Errorf("failed to fetch usage from %s", url)
	}
	return ParseUsage(body)
}

// WriteUsageReport renders quota bars and reset information at the requested display width.
func WriteUsageReport(writer io.Writer, name string, report UsageReport, displayWidth int) {
	if displayWidth <= 0 {
		displayWidth = fallbackTextWidth
	}
	usagerender.WriteHeading(writer, name)
	for _, window := range report.Windows {
		usagerender.WriteWindow(writer, usagerender.Window{
			Label: window.Label, UsedPercent: window.Percent, ResetAt: window.ResetAt, Note: window.Note,
		}, displayWidth)
	}
}

func objectValue(raw json.RawMessage, key string) json.RawMessage {
	for _, member := range objectMembers(raw) {
		if member.name == key {
			return member.value
		}
	}
	return nil
}

func objectMembers(raw json.RawMessage) []objectMember {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil
	}
	var members []objectMember
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
		members = append(members, objectMember{name: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	return members
}

func jsonString(raw json.RawMessage) string {
	value, _ := jsonStringValue(raw)
	return value
}

func jsonStringValue(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func jsonNumber(raw json.RawMessage) *float64 {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return nil
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func parseUTCTimestamp(text string) (time.Time, bool) {
	if len(text) < 20 || text[4] != '-' || text[7] != '-' || text[10] != 'T' ||
		text[13] != ':' || text[16] != ':' {
		return time.Time{}, false
	}
	year, okYear := parseDigits(text[0:4])
	month, okMonth := parseDigits(text[5:7])
	day, okDay := parseDigits(text[8:10])
	hour, okHour := parseDigits(text[11:13])
	minute, okMinute := parseDigits(text[14:16])
	second, okSecond := parseDigits(text[17:19])
	if !okYear || !okMonth || !okDay || !okHour || !okMinute || !okSecond || year < 1970 ||
		month < 1 || month > 12 || hour > 23 || minute > 59 || second > 60 {
		return time.Time{}, false
	}
	monthDays := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
	maxDay := monthDays[month-1]
	if month == 2 && leap {
		maxDay++
	}
	if day < 1 || day > maxDay {
		return time.Time{}, false
	}
	index := 19
	if text[index] == '.' {
		index++
		start := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
		}
		if index == start {
			return time.Time{}, false
		}
	}
	if index != len(text)-1 || text[index] != 'Z' {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC), true
}

func parseDigits(text string) (int, bool) {
	value := 0
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + int(digit-'0')
	}
	return value, true
}

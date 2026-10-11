// Package openrouter implements OpenRouter-specific account usage reporting.
package openrouter

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
	usageTimeout = 30 * time.Second
	maxBodySize  = 1 << 20
)

var errUsageShape = errors.New("unrecognized usage response shape (no data object)")

// UsageReport contains the key-level usage and, when available, prepaid account credits.
type UsageReport struct {
	Label            string
	FreeTier         bool
	Spent            *float64
	Limit            *float64
	LimitRemaining   *float64
	CreditsTotal     *float64
	CreditsRemaining *float64
}

// ParseUsage reads the required /key data and optional /credits response.
func ParseUsage(keyData, creditsData []byte) (UsageReport, error) {
	var rootJSON json.RawMessage
	if err := json.Unmarshal(keyData, &rootJSON); err != nil {
		return UsageReport{}, fmt.Errorf("decode OpenRouter usage: %w", err)
	}
	trimmedRoot := bytes.TrimSpace(rootJSON)
	if len(trimmedRoot) == 0 || trimmedRoot[0] != '{' {
		return UsageReport{}, errUsageShape
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmedRoot, &root); err != nil {
		return UsageReport{}, errUsageShape
	}
	var data map[string]json.RawMessage
	if root == nil || json.Unmarshal(root["data"], &data) != nil || data == nil {
		return UsageReport{}, errUsageShape
	}

	report := UsageReport{
		Label:    jsonString(data["label"]),
		FreeTier: bytes.Equal(bytes.TrimSpace(data["is_free_tier"]), []byte("true")),
		Spent:    jsonNumber(data["usage"]),
		Limit:    jsonNumber(data["limit"]),
	}
	if report.Limit != nil {
		report.LimitRemaining = jsonNumber(data["limit_remaining"])
	}

	var creditsRoot map[string]json.RawMessage
	var credits map[string]json.RawMessage
	if json.Unmarshal(creditsData, &creditsRoot) == nil && creditsRoot != nil &&
		json.Unmarshal(creditsRoot["data"], &credits) == nil && credits != nil {
		total := jsonNumber(credits["total_credits"])
		spent := jsonNumber(credits["total_usage"])
		if total != nil && spent != nil && *total > 0 {
			remaining := *total - *spent
			if remaining < 0 {
				remaining = 0
			}
			report.CreditsTotal = total
			report.CreditsRemaining = &remaining
		}
	}
	return report, nil
}

// WriteUsageReport renders provider-supplied text without terminal control sequences.
func WriteUsageReport(writer io.Writer, report UsageReport) {
	details := make([]string, 0, 2)
	label := report.Label
	if strings.HasPrefix(label, "sk-") {
		label = ""
	}
	if label = usagerender.StripLineControls(label); label != "" {
		details = append(details, label)
	}
	if report.FreeTier {
		details = append(details, "free tier")
	}
	usagerender.WriteHeading(writer, "openrouter", details...)
	if report.Spent != nil {
		usagerender.WriteValue(writer, "spent", formatCost(*report.Spent))
	}
	if report.Limit != nil {
		value := formatCost(*report.Limit)
		if report.LimitRemaining != nil {
			value += " · " + formatCost(*report.LimitRemaining) + " remaining"
		}
		usagerender.WriteValue(writer, "limit", value)
	}
	if report.CreditsTotal != nil && report.CreditsRemaining != nil {
		usagerender.WriteValue(writer, "credits", formatCost(*report.CreditsRemaining)+" of "+formatCost(*report.CreditsTotal)+" remaining")
	}
}

// QueryUsage retrieves the required /key report and best-effort /credits report.
func QueryUsage(ctx context.Context, baseURL string, headers []string, hasAPIKey bool,
	client *transport.Client, tick func() bool) (UsageReport, error) {
	if !hasAPIKey {
		return UsageReport{}, errors.New("no OpenRouter API key configured")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return UsageReport{}, errors.New("OpenRouter provider has no base URL")
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
	keyURL := baseURL + "/key"
	keyData, status, err := client.Get(ctx, keyURL, requestHeaders, usageTimeout, maxBodySize, tick)
	if err != nil || len(keyData) == 0 {
		if status == http.StatusUnauthorized {
			return UsageReport{}, errors.New("OpenRouter rejected the configured API key (401)")
		}
		return UsageReport{}, fmt.Errorf("failed to fetch usage from %s", keyURL)
	}
	creditsData, _, _ := client.Get(ctx, baseURL+"/credits", requestHeaders, usageTimeout, maxBodySize, tick)
	report, err := ParseUsage(keyData, creditsData)
	if err != nil {
		if errors.Is(err, errUsageShape) {
			return UsageReport{}, err
		}
		return UsageReport{}, fmt.Errorf("usage response is not valid JSON: %w", err)
	}
	return report, nil
}

func jsonString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func jsonNumber(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
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

func formatCost(value float64) string {
	if value <= 0 {
		return "$0.00"
	}
	if value < 0.01 {
		return fmt.Sprintf("$%.4f", value)
	}
	if value < 1 {
		return fmt.Sprintf("$%.3f", value)
	}
	return fmt.Sprintf("$%.2f", value)
}

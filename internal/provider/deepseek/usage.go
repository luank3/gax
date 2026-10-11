package deepseek

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
	maxBalances  = 4
)

var errUsageShape = errors.New("unrecognized balance response shape (no balances)")

// Balance is one currency balance returned by DeepSeek.
type Balance struct {
	Currency string
	Total    string
	Granted  string
}

// UsageReport contains the balance availability flag and valid currency balances.
type UsageReport struct {
	Available bool
	Balances  []Balance
}

// ParseUsage reads up to four well-formed balance entries. Missing is_available defaults to true.
func ParseUsage(data []byte) (UsageReport, error) {
	var rootJSON json.RawMessage
	if err := json.Unmarshal(data, &rootJSON); err != nil {
		return UsageReport{}, fmt.Errorf("usage response is not valid JSON: %w", err)
	}
	trimmed := bytes.TrimSpace(rootJSON)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return UsageReport{}, errUsageShape
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil || root == nil {
		return UsageReport{}, errUsageShape
	}
	report := UsageReport{Available: !bytes.Equal(bytes.TrimSpace(root["is_available"]), []byte("false"))}
	var entries []json.RawMessage
	if json.Unmarshal(root["balance_infos"], &entries) == nil {
		for _, raw := range entries {
			if len(report.Balances) == maxBalances {
				break
			}
			var entry map[string]json.RawMessage
			if json.Unmarshal(raw, &entry) != nil || entry == nil {
				continue
			}
			currency := jsonString(entry["currency"])
			total := jsonString(entry["total_balance"])
			if currency == "" || !validAmount(total) {
				continue
			}
			granted := jsonString(entry["granted_balance"])
			if !validAmount(granted) || !positiveAmount(granted) {
				granted = ""
			}
			report.Balances = append(report.Balances, Balance{Currency: currency, Total: total, Granted: granted})
		}
	}
	if len(report.Balances) == 0 {
		return UsageReport{}, errUsageShape
	}
	return report, nil
}

// QueryUsage retrieves DeepSeek's /user/balance report using resolved provider headers.
func QueryUsage(ctx context.Context, baseURL string, headers []string, hasAPIKey bool,
	client *transport.Client, tick func() bool) (UsageReport, error) {
	if !hasAPIKey {
		return UsageReport{}, errors.New("no DEEPSEEK_API_KEY configured")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return UsageReport{}, errors.New("DeepSeek provider has no base URL")
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()

	requestHeaders := append([]string{"Accept: application/json"}, headers...)
	url := baseURL + "/user/balance"
	data, status, err := client.Get(ctx, url, requestHeaders, usageTimeout, maxBodySize, tick)
	if err != nil {
		if status == http.StatusUnauthorized {
			return UsageReport{}, errors.New("DeepSeek rejected the configured API key (401)")
		}
		return UsageReport{}, fmt.Errorf("failed to fetch usage from %s", url)
	}
	return ParseUsage(data)
}

// WriteUsageReport renders the balance report while stripping controls from provider text.
func WriteUsageReport(writer io.Writer, name string, report UsageReport) {
	var details []string
	if !report.Available {
		details = append(details, "balance too low for API calls")
	}
	usagerender.WriteHeading(writer, name, details...)
	for _, balance := range report.Balances {
		usagerender.WriteValue(writer, "balance", formatBalance(balance))
	}
}

func formatBalance(balance Balance) string {
	formatAmount := func(amount, currency string) string {
		switch currency {
		case "USD":
			return "$" + amount
		case "CNY":
			return "¥" + amount
		default:
			return amount + " " + usagerender.StripLineControls(currency)
		}
	}
	text := formatAmount(balance.Total, balance.Currency)
	if balance.Granted != "" {
		text += " · " + formatAmount(balance.Granted, balance.Currency) + " granted"
	}
	return text
}

func validAmount(text string) bool {
	if text == "" || !((text[0] >= '0' && text[0] <= '9') || text[0] == '-') {
		return false
	}
	value, err := strconv.ParseFloat(text, 64)
	return err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func positiveAmount(text string) bool {
	value, err := strconv.ParseFloat(text, 64)
	return err == nil && value > 0
}

func jsonString(raw json.RawMessage) string {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

// Package codex implements Codex authentication loading and account usage reporting.
package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/luank3/gax/internal/provider/usagerender"
	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/transport"
)

const (
	usageEndpoint = "https://chatgpt.com/backend-api/wham/usage"
	usageTimeout  = 30 * time.Second
	maxBodySize   = 1 << 20
)

// Auth is a resolved Codex credential, either gax-managed or borrowed from the Codex CLI.
type Auth struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	Email        string
	Source       Source
	storePath    string
	home         string
	stateHome    string
}

// Source identifies whether Codex credentials are gax-managed or borrowed from the CLI.
type Source string

const (
	SourceCLI Source = "codex-cli"
	SourceGax Source = "gax"
)

// UsageWindow is one Codex quota window. Unrecognized windows retain their fallback label.
type UsageWindow struct {
	Label      string
	Percent    float64
	ResetAt    time.Time
	Recognized bool
}

// UsageReport contains Codex account labels and its primary/secondary rate-limit windows.
type UsageReport struct {
	Email        string
	Plan         string
	HasRateLimit bool
	Windows      []UsageWindow
	Warnings     []string
}

// LoadCLIAuth reads credentials borrowed from <home>/.codex/auth.json.
func LoadCLIAuth(home string) (Auth, error) {
	if home == "" {
		home = os.Getenv("HOME")
	}
	path := systempath.ExpandHome("~/.codex/auth.json", home)
	body, err := systemfs.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Auth{}, fmt.Errorf("no ChatGPT login found — run /login, or log in with the codex CLI (%s)", path)
		}
		return Auth{}, fmt.Errorf("could not read Codex CLI auth file: %w", err)
	}
	return ParseCLIAuth(body)
}

// LoadAuth prefers gax-managed credentials at <stateHome>/gax/auth.json, then falls back to the
// Codex CLI's auth.json when the store is absent, unreadable, or has no complete Codex entry.
func LoadAuth(home, stateHome string) (Auth, error) {
	if storePath, pathErr := authStorePath(home, stateHome); pathErr == nil {
		body, err := systemfs.ReadFile(storePath)
		if err == nil {
			root := jsonObject(json.RawMessage(body))
			if entry := root["codex"]; len(entry) > 0 {
				if auth, authErr := parseGaxAuth(entry); authErr == nil {
					if resolved, resolveErr := filepath.EvalSymlinks(storePath); resolveErr == nil {
						storePath = resolved
					}
					auth.storePath = storePath
					auth.home = home
					auth.stateHome = stateHome
					return auth, nil
				}
			}
		}
	}
	return LoadCLIAuth(home)
}

// ParseCLIAuth reads the borrowed token fields and extracts an optional email claim from id_token.
func ParseCLIAuth(body []byte) (Auth, error) {
	var rootJSON json.RawMessage
	if err := json.Unmarshal(body, &rootJSON); err != nil {
		return Auth{}, fmt.Errorf("~/.codex/auth.json is not valid JSON: %w", err)
	}
	var root map[string]json.RawMessage
	if !isJSONObject(rootJSON) || json.Unmarshal(rootJSON, &root) != nil {
		return Auth{}, errors.New("auth.json missing tokens.access_token or tokens.account_id")
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(root["tokens"], &tokens) != nil || tokens == nil {
		return Auth{}, errors.New("auth.json missing tokens.access_token or tokens.account_id")
	}
	auth := Auth{AccessToken: jsonString(tokens["access_token"]), AccountID: jsonString(tokens["account_id"]), Source: SourceCLI}
	if auth.AccessToken == "" || auth.AccountID == "" {
		return Auth{}, errors.New("auth.json missing tokens.access_token or tokens.account_id")
	}
	auth.Email = jwtEmail(jsonString(tokens["id_token"]))
	return auth, nil
}

func parseGaxAuth(raw json.RawMessage) (Auth, error) {
	entry := jsonObject(raw)
	auth := Auth{
		AccessToken:  jsonString(entry["access_token"]),
		RefreshToken: jsonString(entry["refresh_token"]),
		AccountID:    jsonString(entry["account_id"]),
		Email:        jwtEmail(jsonString(entry["id_token"])),
		Source:       SourceGax,
	}
	if auth.AccessToken == "" || auth.RefreshToken == "" || auth.AccountID == "" {
		return Auth{}, errors.New("credential entry has no complete Codex token set")
	}
	return auth, nil
}

// ParseUsage maps the fixed Codex usage response into renderable windows.
func ParseUsage(body []byte, auth Auth) (UsageReport, error) {
	var rootJSON json.RawMessage
	if err := json.Unmarshal(body, &rootJSON); err != nil {
		return UsageReport{}, fmt.Errorf("usage response is not valid JSON: %w", err)
	}
	root := jsonObject(rootJSON)
	rateLimitRaw := root["rate_limit"]
	report := UsageReport{
		Email:        auth.Email,
		Plan:         jsonString(root["plan_type"]),
		HasRateLimit: len(bytes.TrimSpace(rateLimitRaw)) > 0 && !bytes.Equal(bytes.TrimSpace(rateLimitRaw), []byte("null")),
	}
	if !report.HasRateLimit {
		return report, nil
	}
	rateLimit := jsonObject(rateLimitRaw)
	for _, fallback := range []string{"primary", "secondary"} {
		windowRaw := rateLimit[fallback+"_window"]
		if len(bytes.TrimSpace(windowRaw)) == 0 || bytes.Equal(bytes.TrimSpace(windowRaw), []byte("null")) {
			continue
		}
		window := UsageWindow{Label: fallback}
		values := jsonObject(windowRaw)
		percent := jsonNumber(values["used_percent"])
		reset := jsonNumber(values["reset_at"])
		const int64UpperExclusive = 9223372036854775808.0
		if percent != nil && reset != nil && *reset >= -int64UpperExclusive && *reset < int64UpperExclusive {
			window.Percent = *percent
			window.ResetAt = time.Unix(int64(*reset), 0)
			window.Recognized = true
			if duration := jsonInteger(values["limit_window_seconds"]); duration != nil {
				window.Label = formatWindowLabel(*duration, fallback)
			}
		}
		report.Windows = append(report.Windows, window)
	}
	return report, nil
}

// QueryUsage fetches Codex account usage with the account ID bound to the resolved credential.
func QueryUsage(ctx context.Context, auth *Auth, extraHeaders []string,
	client *transport.Client, tick func() bool) (UsageReport, error) {
	if auth == nil || auth.AccessToken == "" || auth.AccountID == "" {
		return UsageReport{}, errors.New("codex is not logged in — run /login")
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	warnings := make([]string, 0, 1)
	if _, warning := ensureFresh(ctx, auth, false, client, tick); warning != "" {
		warnings = append(warnings, warning)
	}
	body, status, err := fetchUsage(ctx, auth, extraHeaders, client, tick)
	if err != nil {
		if status == http.StatusUnauthorized {
			if auth.Source == SourceGax {
				result, warning := ensureFresh(ctx, auth, true, client, tick)
				if warning != "" {
					warnings = append(warnings, warning)
				}
				if result == refreshFresh {
					body, status, err = fetchUsage(ctx, auth, extraHeaders, client, tick)
					if err == nil {
						report, parseErr := ParseUsage(body, *auth)
						report.Warnings = warnings
						return report, parseErr
					}
				}
				if result == refreshDead {
					if fallbackAuth, fallbackErr := LoadAuth(auth.home, auth.stateHome); fallbackErr == nil {
						if fallbackAuth.AccountID != auth.AccountID {
							return UsageReport{}, errors.New("the codex login belongs to a different account — run /login or /provider to switch")
						}
						if !sameAuth(*auth, fallbackAuth) {
							*auth = fallbackAuth
							body, status, err = fetchUsage(ctx, auth, extraHeaders, client, tick)
							if err == nil {
								report, parseErr := ParseUsage(body, *auth)
								report.Warnings = warnings
								return report, parseErr
							}
						}
					}
				}
				if result == refreshTransient {
					return UsageReport{}, refreshErrorMessage()
				}
				if status != http.StatusUnauthorized {
					return UsageReport{}, fmt.Errorf("failed to fetch usage from %s", usageEndpoint)
				}
				if auth.Source == SourceCLI {
					return UsageReport{}, errors.New("codex CLI token expired — rerun `codex`, or use /login")
				}
				return UsageReport{}, errors.New("codex login expired — run /login again")
			}
			return UsageReport{}, errors.New("codex CLI token expired — rerun `codex`, or use /login")
		}
		return UsageReport{}, fmt.Errorf("failed to fetch usage from %s", usageEndpoint)
	}
	report, err := ParseUsage(body, *auth)
	report.Warnings = warnings
	return report, err
}

func sameAuth(left, right Auth) bool {
	return left.AccessToken == right.AccessToken && left.AccountID == right.AccountID
}

func fetchUsage(ctx context.Context, auth *Auth, extraHeaders []string, client *transport.Client,
	tick func() bool) ([]byte, int, error) {
	headers := []string{
		"Authorization: Bearer " + auth.AccessToken,
		"chatgpt-account-id: " + auth.AccountID,
		"Accept: application/json",
	}
	headers = append(headers, extraHeaders...)
	return client.Get(ctx, usageEndpoint, headers, usageTimeout, maxBodySize, tick)
}

// WriteUsageReport prints Codex plan details and rate-limit windows.
func WriteUsageReport(writer io.Writer, report UsageReport, displayWidth int) {
	usagerender.WriteHeading(writer, "codex", report.Email, report.Plan)
	if !report.HasRateLimit {
		fmt.Fprintln(writer, "  no rate-limit windows reported for this plan")
		return
	}
	for _, window := range report.Windows {
		if !window.Recognized {
			usagerender.WriteValue(writer, window.Label, "(unrecognized window shape)")
			continue
		}
		usagerender.WriteWindow(writer, usagerender.Window{
			Label: window.Label, UsedPercent: window.Percent, ResetAt: window.ResetAt,
		}, displayWidth)
	}
}

func formatWindowLabel(seconds int64, fallback string) string {
	switch {
	case seconds <= 0:
		return "?"
	case seconds == 604800:
		return "weekly"
	case seconds == 86400:
		return "daily"
	case seconds < 60:
		return strconv.FormatInt(seconds, 10) + "s"
	case seconds < 3600:
		return strconv.FormatInt(seconds/60, 10) + "m"
	case seconds < 86400:
		return strconv.FormatInt(seconds/3600, 10) + "h"
	default:
		return strconv.FormatInt(seconds/86400, 10) + "d"
	}
}

func jwtEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 3 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	claims := jsonObject(decoded)
	if email := jsonString(claims["email"]); email != "" {
		return email
	}
	profile := jsonObject(claims[`https://api.openai.com/profile`])
	return jsonString(profile["email"])
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if !isJSONObject(raw) || json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

func jsonString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
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

func jsonInteger(raw json.RawMessage) *int64 {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return nil
	}
	value, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

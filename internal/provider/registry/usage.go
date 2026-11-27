package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/luank3/gax/internal/platform"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/transport"
)

// QueryAccountUsage queries provider-specific account/subscription usage (/usage).
func QueryAccountUsage(ctx context.Context, providerID string, clients ...*transport.Client) (provider.UsageReport, error) {
	var hc *transport.Client
	if len(clients) > 0 {
		hc = clients[0]
	}
	switch providerID {
	case "codex":
		dirs := platform.ResolveDirs()
		creds, _, err := codex.LoadCredentials(dirs.State, dirs.Home)
		if err != nil {
			return provider.UsageReport{}, err
		}
		return codex.QueryUsage(ctx, creds.AccessToken)
	case "openrouter":
		return queryJSONUsage(ctx, hc, "https://openrouter.ai/api/v1/credits", "Bearer", os.Getenv("OPENROUTER_API_KEY"), func(raw []byte) (string, error) {
			var r struct {
				Data struct {
					TotalCredits float64 `json:"total_credits"`
					TotalUsage   float64 `json:"total_usage"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return "", err
			}
			return fmt.Sprintf("openrouter: used $%.4f / $%.4f credits", r.Data.TotalUsage, r.Data.TotalCredits), nil
		})
	case "deepseek":
		return queryJSONUsage(ctx, hc, "https://api.deepseek.com/user/balance", "Bearer", os.Getenv("DEEPSEEK_API_KEY"), func(raw []byte) (string, error) {
			var r struct {
				BalanceInfos []struct {
					Currency     string `json:"currency"`
					TotalBalance string `json:"total_balance"`
				} `json:"balance_infos"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return "", err
			}
			if len(r.BalanceInfos) == 0 {
				return "deepseek: balance unavailable", nil
			}
			b := r.BalanceInfos[0]
			return fmt.Sprintf("deepseek: %s %s", b.TotalBalance, b.Currency), nil
		})
	case "opencode-go":
		return queryJSONUsage(ctx, hc, "https://opencode.ai/zen/go/v1/usage", "Bearer", os.Getenv("OPENCODE_API_KEY"), func(raw []byte) (string, error) {
			return "opencode-go: " + strings.TrimSpace(string(raw)), nil
		})
	default:
		return provider.UsageReport{}, fmt.Errorf("/usage is not supported by %s", providerID)
	}
}

func queryJSONUsage(ctx context.Context, hc *transport.Client, url, authKind, key string, parse func([]byte) (string, error)) (provider.UsageReport, error) {
	if key == "" {
		return provider.UsageReport{}, fmt.Errorf("missing API key")
	}
	hdr := http.Header{}
	if authKind == "Bearer" {
		hdr.Set("Authorization", "Bearer "+key)
	}
	raw, status, err := hc.DoGet(ctx, url, hdr, 64*1024)
	if err != nil {
		return provider.UsageReport{}, err
	}
	if status < 200 || status >= 300 {
		return provider.UsageReport{}, fmt.Errorf("HTTP %d: %s", status, strings.TrimSpace(string(raw)))
	}
	summary, err := parse(raw)
	if err != nil {
		return provider.UsageReport{}, err
	}
	return provider.UsageReport{Summary: summary}, nil
}

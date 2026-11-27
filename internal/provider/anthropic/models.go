package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luank3/gax/internal/provider"
)

// ListModels queries an Anthropic-style /models endpoint.
func ListModels(ctx context.Context, baseURL, apiKey, version string) ([]provider.ModelEntry, error) {
	if version == "" {
		version = "2023-06-01"
	}
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", version)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]provider.ModelEntry, 0, len(body.Data))
	for _, m := range body.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, provider.ModelEntry{ID: m.ID, DisplayName: name})
	}
	return out, nil
}

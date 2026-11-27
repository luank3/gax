package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luank3/gax/internal/provider"
)

// ListModels queries an OpenAI-style /models endpoint (and /props for llamacpp).
func ListModels(ctx context.Context, baseURL, apiKey string, extraHeaders map[string]string) ([]provider.ModelEntry, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
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
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int64  `json:"context_length"`
			Status        struct {
				Value string `json:"value"`
			} `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]provider.ModelEntry, 0, len(body.Data))
	for _, m := range body.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, provider.ModelEntry{
			ID:           m.ID,
			DisplayName:  name,
			ContextLimit: m.ContextLength,
			Loaded:       m.Status.Value == "loaded",
		})
	}
	return out, nil
}

// ProbeLlamaCppProps queries llama-server /props for active context size and multimodal support.
func ProbeLlamaCppProps(ctx context.Context, baseURL, apiKey string) (contextLimit int64, imageInput bool) {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/props", nil)
	if err != nil {
		return 0, false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var props struct {
		DefaultGenerationSettings struct {
			NCtx int64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&props) == nil {
		return props.DefaultGenerationSettings.NCtx, props.Modalities.Vision
	}
	return 0, false
}

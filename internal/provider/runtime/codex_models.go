package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luank3/gax/internal/provider/codex"
	"github.com/luank3/gax/internal/transport"
)

func (provider *Provider) listCodexModels(ctx context.Context, client *transport.Client,
	tick func() bool) ([]ModelInfo, error) {
	if provider.CodexAuth() == nil || provider.CodexAuth().AccessToken == "" {
		return nil, fmt.Errorf("codex is not logged in — run /login")
	}
	if !provider.WaitModelProbe(ModelMetadataWait, tick) {
		return nil, fmt.Errorf("codex model catalog request was interrupted")
	}
	if client == nil {
		client = transport.DefaultClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const timeout = 5 * time.Second
	const maxResponseBytes = 32 << 20
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	auth := provider.CodexAuth()
	codex.PrepareRequestAuth(ctx, auth, client, tick)
	endpoint := strings.TrimRight(provider.BaseURL, "/") + "/models?client_version=999.0.0"
	data, status, err := client.Get(ctx, endpoint, provider.MetadataHeaders(""), timeout, maxResponseBytes, tick)
	if err != nil && status == http.StatusUnauthorized {
		retry, _, message := codex.RecoverUnauthorized(ctx, auth, client, tick)
		if !retry {
			if message == "" {
				message = "codex login expired — run /login again"
			}
			return nil, fmt.Errorf("%s", message)
		}
		data, status, err = client.Get(ctx, endpoint, provider.MetadataHeaders(""), timeout, maxResponseBytes, tick)
	}
	if err != nil {
		if status == http.StatusUnauthorized {
			return nil, fmt.Errorf("codex login expired — run /login again")
		}
		if status != 0 {
			return nil, fmt.Errorf("codex model catalog fetch failed (HTTP %d)", status)
		}
		return nil, fmt.Errorf("could not reach chatgpt.com to list models — check your network")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, fmt.Errorf("codex sent an empty or truncated model catalog response")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(trimmed, &envelope) != nil || envelope == nil {
		return nil, fmt.Errorf("codex model catalog response has no model list")
	}
	var entries []json.RawMessage
	if json.Unmarshal(envelope["models"], &entries) != nil || entries == nil {
		return nil, fmt.Errorf("codex model catalog response has no model list")
	}
	models := make([]ModelInfo, 0, len(entries))
	for _, entry := range entries {
		var object map[string]json.RawMessage
		if json.Unmarshal(entry, &object) != nil || object == nil || rawString(object["visibility"]) == "hide" {
			continue
		}
		id := rawString(object["slug"])
		if id == "" {
			continue
		}
		if model, ok := parseCodexModel(entry, id); ok {
			models = append(models, model)
		}
	}
	if len(models) == 0 && len(entries) > 0 {
		return nil, fmt.Errorf("codex model catalog contains no usable models")
	}
	provider.storeListedModels(models)
	return models, nil
}

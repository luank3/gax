package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/mcp"
)

func TestZeroCostWhenNoServersConfigured(t *testing.T) {
	m := mcp.NewManager(nil, t.TempDir())
	if m != nil {
		t.Fatal("expected nil Manager when no servers configured")
	}
	if idx := m.PromptIndex(); idx != "" {
		t.Fatalf("expected empty PromptIndex, got %q", idx)
	}
}

func TestStreamableHTTPLazyDiscoveryAndCall(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fake", Version: "1.0"}, nil)
	type echoIn struct {
		Msg string `json:"msg"`
	}
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "echo", Description: "Echo a message"}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in echoIn) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo:" + in.Msg}},
		}, nil, nil
	})

	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	cacheDir := t.TempDir()
	mgr := mcp.NewManager(map[string]config.MCPServerConfig{
		"demo": {Transport: "http", URL: ts.URL},
	}, cacheDir)
	defer mgr.Close()

	// Before discovery: index lists server name without spawning/connecting.
	if idx := mgr.PromptIndex(); !strings.Contains(idx, "- demo") {
		t.Fatalf("unexpected initial PromptIndex: %q", idx)
	}

	// Lazy connect + schema fetch
	schema, err := mgr.ListTools(context.Background(), "demo", "echo")
	if err != nil || !strings.Contains(schema, "[mcp_schema: demo/echo]") || !strings.Contains(schema, `"msg"`) {
		t.Fatalf("ListTools: schema=%q err=%v", schema, err)
	}

	// Disk cache populated
	if _, err := os.Stat(filepath.Join(cacheDir, "mcp", "demo.json")); err != nil {
		t.Fatalf("expected cache file: %v", err)
	}
	if idx := mgr.PromptIndex(); !strings.Contains(idx, "echo (Echo a message)") {
		t.Fatalf("expected cached tool in PromptIndex: %q", idx)
	}

	// Tool call
	out, isErr, err := mgr.CallTool(context.Background(), "demo", "echo", map[string]any{"msg": "hi"})
	if err != nil || isErr || out != "echo:hi" {
		t.Fatalf("CallTool: out=%q isErr=%v err=%v", out, isErr, err)
	}
}

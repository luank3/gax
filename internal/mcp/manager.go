// Package mcp implements lazy-loaded MCP server connections (stdio and
// streamable-HTTP) using github.com/modelcontextprotocol/go-sdk.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/luank3/gax/internal/config"
)

const defaultMCPTimeout = 30 * time.Second

type cachedTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type serverEntry struct {
	name string
	cfg  config.MCPServerConfig

	mu      sync.Mutex
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// Manager lazily spawns/connects to configured MCP servers on demand.
type Manager struct {
	cacheDir string
	servers  map[string]*serverEntry
	order    []string
}

// NewManager returns nil when no MCP servers are configured (zero-cost invariant).
func NewManager(servers map[string]config.MCPServerConfig, cacheDir string) *Manager {
	if len(servers) == 0 {
		return nil
	}
	m := &Manager{
		cacheDir: cacheDir,
		servers:  make(map[string]*serverEntry, len(servers)),
	}
	for name, cfg := range servers {
		m.servers[name] = &serverEntry{name: name, cfg: cfg}
		m.order = append(m.order, name)
	}
	sort.Strings(m.order)
	return m
}

// PromptIndex returns a compact # MCP Servers listing from disk cache (no schemas, no spawns).
func (m *Manager) PromptIndex() string {
	if m == nil || len(m.order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# MCP Servers\nUse `mcp_tools` to inspect a server's tool schemas on demand, and `mcp_call` to invoke a tool:")
	for _, name := range m.order {
		fmt.Fprintf(&b, "\n- %s", name)
		if cached := m.readCache(name); len(cached) > 0 {
			names := make([]string, 0, len(cached))
			for _, ct := range cached {
				if ct.Description != "" {
					desc := ct.Description
					if len(desc) > 60 {
						desc = desc[:57] + "..."
					}
					names = append(names, fmt.Sprintf("%s (%s)", ct.Name, desc))
				} else {
					names = append(names, ct.Name)
				}
			}
			fmt.Fprintf(&b, ": %s", strings.Join(names, ", "))
		}
	}
	return b.String()
}

// ListTools connects to serverName if needed and returns either a compact tool list
// or the full JSON schema for toolName.
func (m *Manager) ListTools(ctx context.Context, serverName, toolName string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("no MCP servers configured")
	}
	ent, ok := m.servers[serverName]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q (available: %s)", serverName, strings.Join(m.order, ", "))
	}

	toolsList, err := m.ensureTools(ctx, ent)
	if err != nil {
		return "", err
	}
	if toolName != "" {
		for _, t := range toolsList {
			if t.Name == toolName {
				b, err := json.MarshalIndent(t, "", "  ")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("[mcp_schema: %s/%s]\n%s", serverName, toolName, string(b)), nil
			}
		}
		return "", fmt.Errorf("tool %q not found on MCP server %q", toolName, serverName)
	}

	if len(toolsList) == 0 {
		return fmt.Sprintf("MCP server %q has no tools", serverName), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tools on MCP server %q:\n", serverName)
	for _, t := range toolsList {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// CallTool invokes toolName on serverName, respawning once on closed connection.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("no MCP servers configured")
	}
	ent, ok := m.servers[serverName]
	if !ok {
		return "", false, fmt.Errorf("unknown MCP server %q", serverName)
	}

	for attempt := 0; attempt < 2; attempt++ {
		sess, err := m.ensureSession(ctx, ent)
		if err != nil {
			return "", false, err
		}
		callCtx, cancel := withTimeout(ctx, ent.cfg.Timeout)
		res, err := sess.CallTool(callCtx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		})
		cancel()
		if err != nil {
			if attempt == 0 && errors.Is(err, mcp.ErrConnectionClosed) {
				m.resetSession(ent)
				continue
			}
			return "", false, err
		}
		return formatToolResult(res), res.IsError, nil
	}
	return "", false, fmt.Errorf("MCP server %q connection failed after retry", serverName)
}

// Close closes all active MCP server sessions.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	for _, ent := range m.servers {
		m.resetSession(ent)
	}
}

func (m *Manager) ensureSession(ctx context.Context, ent *serverEntry) (*mcp.ClientSession, error) {
	ent.mu.Lock()
	defer ent.mu.Unlock()
	if ent.session != nil {
		return ent.session, nil
	}

	transport, err := buildTransport(ent.cfg)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "gax", Version: "v2"}, nil)
	connCtx, cancel := withTimeout(ctx, ent.cfg.Timeout)
	defer cancel()
	sess, err := client.Connect(connCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP %q: %w", ent.name, err)
	}
	ent.session = sess
	return sess, nil
}

func (m *Manager) ensureTools(ctx context.Context, ent *serverEntry) ([]*mcp.Tool, error) {
	sess, err := m.ensureSession(ctx, ent)
	if err != nil {
		return nil, err
	}
	ent.mu.Lock()
	if ent.tools != nil {
		toolsCopy := append([]*mcp.Tool(nil), ent.tools...)
		ent.mu.Unlock()
		return toolsCopy, nil
	}
	ent.mu.Unlock()

	listCtx, cancel := withTimeout(ctx, ent.cfg.Timeout)
	defer cancel()
	res, err := sess.ListTools(listCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools on MCP %q: %w", ent.name, err)
	}
	ent.mu.Lock()
	ent.tools = res.Tools
	ent.mu.Unlock()

	m.writeCache(ent.name, res.Tools)
	return res.Tools, nil
}

func (m *Manager) resetSession(ent *serverEntry) {
	ent.mu.Lock()
	defer ent.mu.Unlock()
	if ent.session != nil {
		_ = ent.session.Close()
		ent.session = nil
	}
	ent.tools = nil
}

func buildTransport(cfg config.MCPServerConfig) (mcp.Transport, error) {
	switch cfg.Transport {
	case "", "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio MCP server requires command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			env := os.Environ()
			for k, raw := range cfg.Env {
				if val, ok := config.ExpandEnvValue(raw); ok {
					env = append(env, k+"="+val)
				}
			}
			cmd.Env = env
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http", "streamable-http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("http MCP server requires url")
		}
		httpClient := &http.Client{}
		if len(cfg.Headers) > 0 {
			httpClient.Transport = &headerRoundTripper{base: http.DefaultTransport, headers: cfg.Headers}
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: httpClient,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", cfg.Transport)
	}
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, raw := range h.headers {
		if val, ok := config.ExpandEnvValue(raw); ok && val != "" {
			clone.Header.Set(k, val)
		}
	}
	return h.base.RoundTrip(clone)
}

func withTimeout(ctx context.Context, raw string) (context.Context, context.CancelFunc) {
	d := defaultMCPTimeout
	if raw != "" {
		if parsed, err := config.ParseDuration(raw); err == nil && parsed > 0 {
			d = parsed
		}
	}
	return context.WithTimeout(ctx, d)
}

func formatToolResult(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		} else if b, err := json.Marshal(c); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

func (m *Manager) cachePath(serverName string) string {
	if m.cacheDir == "" {
		return ""
	}
	return filepath.Join(m.cacheDir, "mcp", serverName+".json")
}

func (m *Manager) readCache(serverName string) []cachedTool {
	p := m.cachePath(serverName)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []cachedTool
	_ = json.Unmarshal(b, &out)
	return out
}

func (m *Manager) writeCache(serverName string, list []*mcp.Tool) {
	p := m.cachePath(serverName)
	if p == "" {
		return
	}
	out := make([]cachedTool, 0, len(list))
	for _, t := range list {
		out = append(out, cachedTool{Name: t.Name, Description: t.Description})
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if b, err := json.Marshal(out); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

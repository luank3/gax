package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/core"
)

// MCPClient is the consumer-defined interface for lazy MCP tool discovery and calls.
type MCPClient interface {
	ListTools(ctx context.Context, server, tool string) (string, error)
	CallTool(ctx context.Context, server, tool string, args map[string]any) (string, bool, error)
}

// SetMCP registers mcp_tools and mcp_call when client is non-nil.
func (r *Registry) SetMCP(client MCPClient) {
	if client == nil {
		return
	}
	r.Register(&mcpToolsTool{reg: r, client: client})
	r.Register(&mcpCallTool{reg: r, client: client})
}

type mcpToolsTool struct {
	reg    *Registry
	client MCPClient
}

func (t *mcpToolsTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "mcp_tools",
		Description: "Discover tools on a configured MCP server, or fetch the full JSON schema of a specific tool.",
		Params: []core.ToolParam{
			{Name: "server", Type: "string", Required: true, Description: "Configured MCP server name."},
			{Name: "tool", Type: "string", Description: "Optional tool name to fetch full schema for."},
		},
	}
}

func (t *mcpToolsTool) Run(ctx context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if strings.TrimSpace(args.Server) == "" {
		return Result{Text: "error: 'server' is required", IsError: true}
	}
	out, err := t.client.ListTools(ctx, args.Server, args.Tool)
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err), IsError: true}
	}
	if args.Tool != "" && t.schemaAlreadyInActiveContext(out) {
		return Result{Text: fmt.Sprintf("[mcp_schema: %s/%s — already loaded in active conversation; reuse the schema above]", args.Server, args.Tool)}
	}
	return Result{Text: capOutput(out, t.reg.outputCap(), "gax-mcp")}
}

func (t *mcpToolsTool) schemaAlreadyInActiveContext(doc string) bool {
	t.reg.mu.Lock()
	fn := t.reg.activeItems
	t.reg.mu.Unlock()
	if fn == nil {
		return false
	}
	for _, it := range fn() {
		if it.Kind == core.KindToolResult && it.ToolName == "mcp_tools" && it.Output == doc {
			return true
		}
	}
	return false
}

type mcpCallTool struct {
	reg    *Registry
	client MCPClient
}

func (t *mcpCallTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "mcp_call",
		Description: "Invoke a tool on a configured MCP server.",
		Params: []core.ToolParam{
			{Name: "server", Type: "string", Required: true, Description: "Configured MCP server name."},
			{Name: "tool", Type: "string", Required: true, Description: "Tool name on that server."},
			{Name: "arguments", Type: "object", Description: "JSON arguments object for the MCP tool."},
		},
	}
}

func (t *mcpCallTool) Run(ctx context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Server    string         `json:"server"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if strings.TrimSpace(args.Server) == "" || strings.TrimSpace(args.Tool) == "" {
		return Result{Text: "error: 'server' and 'tool' are required", IsError: true}
	}
	out, isErr, err := t.client.CallTool(ctx, args.Server, args.Tool, args.Arguments)
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err), IsError: true}
	}
	return Result{Text: capOutput(out, t.reg.outputCap(), "gax-mcp"), IsError: isErr}
}

// Package mcp wraps the upstream mark3labs/mcp-go client and exposes
// remote MCP tools as native Looper Agent tools.
//
// A ToolProvider connects to an MCP server (stdio subprocess, in-process,
// or HTTP), discovers its tools via the standard tools/list request, and
// returns a slice of *tool.Tool whose Execute calls back to the server.
// The agent loop sees these as ordinary tools — no MCP plumbing leaks
// into user code.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// ToolProvider exposes an MCP server's tools as native looper *tool.Tool
// values. Construct via NewToolProvider (with an existing client),
// NewStdioToolProvider (subprocess transport), or compose with the
// mark3labs/mcp-go SDK directly for other transports.
type ToolProvider struct {
	client *mcpgo.Client
	tools  []*tool.Tool
}

// NewToolProvider wraps an already-constructed MCP client. The
// constructor initialises the client (mandatory before any RPC) and
// discovers the server's tool catalogue. The returned provider owns the
// client lifecycle — call Close when finished.
func NewToolProvider(ctx context.Context, c *mcpgo.Client) (*ToolProvider, error) {
	if c == nil {
		return nil, fmt.Errorf("mcp: client is nil")
	}
	// MCP requires an Initialize handshake before any other call.
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp list tools: %w", err)
	}
	tools := make([]*tool.Tool, 0, len(list.Tools))
	for _, t := range list.Tools {
		wrapper, err := buildToolWrapper(c, t)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("mcp tool %q: %w", t.Name, err)
		}
		tools = append(tools, wrapper)
	}
	return &ToolProvider{client: c, tools: tools}, nil
}

// NewStdioToolProvider spawns the MCP server as a subprocess over stdio
// and discovers its tools. Equivalent to wiring NewStdioMCPClient +
// NewToolProvider but in one call — matches the most common deployment
// (local MCP servers shipped as binaries).
func NewStdioToolProvider(ctx context.Context, command string, env []string, args ...string) (*ToolProvider, error) {
	c, err := mcpgo.NewStdioMCPClient(command, env, args...)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio client: %w", err)
	}
	return NewToolProvider(ctx, c)
}

// Tools returns the discovered tools as native looper *tool.Tool values.
// Safe to call multiple times — the slice is stable across calls so
// the agent can register it once at construction.
func (p *ToolProvider) Tools() []*tool.Tool {
	return p.tools
}

// Close terminates the MCP client and releases the transport (stdio
// subprocess, HTTP connection, etc.).
func (p *ToolProvider) Close() error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}

// buildToolWrapper turns a server-side MCP tool into a *tool.Tool whose
// Execute issues an MCP tools/call against the wrapped client. The MCP
// schema becomes the tool's schema; non-strict to avoid stricter looper
// validation rejecting servers that don't emit additionalProperties:false.
func buildToolWrapper(c *mcpgo.Client, t mcp.Tool) (*tool.Tool, error) {
	schemaJSON, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode input schema: %w", err)
	}
	// MCP servers commonly omit additionalProperties — patch it in so
	// downstream strict validators (OpenAI strict mode) accept the
	// tool. This mirrors what our own schema generator does and keeps
	// behaviour consistent across local and MCP-sourced tools.
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaJSON, &schemaMap); err == nil {
		ensureAdditionalPropertiesFalse(schemaMap)
		if patched, err := json.Marshal(schemaMap); err == nil {
			schemaJSON = patched
		}
	}
	return tool.NewToolFromRawSchema(t.Name, t.Description, schemaJSON, func(ctx context.Context, args json.RawMessage) (string, error) {
		var argsMap map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &argsMap); err != nil {
				return "", fmt.Errorf("mcp %s: parse args: %w", t.Name, err)
			}
		}
		res, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: t.Name, Arguments: argsMap},
		})
		if err != nil {
			return "", fmt.Errorf("mcp call %s: %w", t.Name, err)
		}
		text := concatTextContent(res.Content)
		if res.IsError {
			return "", fmt.Errorf("mcp %s: %s", t.Name, text)
		}
		return text, nil
	})
}

// concatTextContent joins every TextContent block in a CallToolResult
// into a single string. Non-text blocks (image, audio, embedded
// resource) are skipped — wire-shape they survive on the MCP side but
// the agent loop expects a plain string today. A future multi-modal
// extension can return Parts here.
func concatTextContent(content []mcp.Content) string {
	var b strings.Builder
	for _, c := range content {
		if tc, ok := c.(mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// ensureAdditionalPropertiesFalse walks a JSON Schema map and sets
// additionalProperties=false on every object node that doesn't have it
// — required by OpenAI strict mode and harmless to all other providers.
func ensureAdditionalPropertiesFalse(node map[string]any) {
	if node["type"] == "object" {
		if _, ok := node["additionalProperties"]; !ok {
			node["additionalProperties"] = false
		}
		if props, ok := node["properties"].(map[string]any); ok {
			for _, v := range props {
				if sub, ok := v.(map[string]any); ok {
					ensureAdditionalPropertiesFalse(sub)
				}
			}
		}
	}
}

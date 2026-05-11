// Package mcp provides MCP (Model Context Protocol) client integration.
// It allows the Looper Agent framework to consume MCP-compatible tools
// as native framework tools.
//
// The MCP debug server is implemented separately in cmd/looper/mcp.go
// and is NOT part of the framework runtime.
package mcp

import (
	"context"
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// MCPToolProvider connects to an MCP server and exposes its tools
// as native Looper Agent tools. Supports stdio and HTTP transports.
type MCPToolProvider struct {
	serverCommand string
	args          []string
	tools         []*tool.Tool
}

// NewMCPToolProvider creates a new MCP tool provider that connects
// to an MCP server via the given command (e.g., "npx", "python", etc.).
func NewMCPToolProvider(serverCommand string, args ...string) (*MCPToolProvider, error) {
	return &MCPToolProvider{
		serverCommand: serverCommand,
		args:          args,
	}, nil
}

// Tools returns the MCP server's tools as Looper Agent tools.
// The tools are discovered lazily on first call and cached.
func (p *MCPToolProvider) Tools(ctx context.Context) ([]*tool.Tool, error) {
	if len(p.tools) > 0 {
		return p.tools, nil
	}

	// Placeholder: in production, this would connect to the MCP server
	// via stdio or HTTP, call tools/list, and convert each tool to
	// a Looper Agent *tool.Tool.
	_ = ctx
	return nil, fmt.Errorf("MCP client not yet implemented")
}

// Close terminates the connection to the MCP server.
func (p *MCPToolProvider) Close() error {
	return nil
}

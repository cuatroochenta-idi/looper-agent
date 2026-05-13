package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// newTestServer wires a tiny in-process MCP server with a single echo
// tool so the client tests run without subprocesses or networking.
func newTestServer(t *testing.T) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test-server", "0.0.1")
	echoTool := mcp.Tool{
		Name:        "echo",
		Description: "Returns the message field back to the caller.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"message": map[string]any{"type": "string", "description": "Text to echo"},
			},
			Required: []string{"message"},
		},
	}
	s.AddTool(echoTool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("echo: " + req.GetString("message", "")), nil
	})
	return s
}

func newInProcessProvider(t *testing.T) *ToolProvider {
	t.Helper()
	s := newTestServer(t)
	c, err := mcpgo.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("in-process client: %v", err)
	}
	p, err := NewToolProvider(context.Background(), c)
	if err != nil {
		t.Fatalf("tool provider: %v", err)
	}
	return p
}

// TestToolProvider_DiscoversTools asserts ListTools-style discovery: a
// fresh provider exposes one *tool.Tool per server-side tool, names and
// descriptions preserved.
func TestToolProvider_DiscoversTools(t *testing.T) {
	p := newInProcessProvider(t)
	defer p.Close()

	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name() != "echo" {
		t.Errorf("expected echo, got %q", tools[0].Name())
	}
	if !strings.Contains(tools[0].Description(), "echoes") && !strings.Contains(tools[0].Description(), "Returns") {
		t.Errorf("unexpected description: %q", tools[0].Description())
	}
}

// TestToolProvider_InvokesUnderlyingTool asserts end-to-end behaviour:
// calling the generated *tool.Tool.Execute reaches the MCP server, the
// server-side handler runs, and the text response surfaces as a string.
func TestToolProvider_InvokesUnderlyingTool(t *testing.T) {
	p := newInProcessProvider(t)
	defer p.Close()

	tools := p.Tools()
	if len(tools) == 0 {
		t.Fatal("no tools exposed")
	}
	out, err := tools[0].Execute(context.Background(), json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "echo: hi" {
		t.Errorf("expected 'echo: hi', got %q", out)
	}
}

// TestToolProvider_ErrorResultSurfaces asserts that an MCP server-side
// IsError result becomes a Go error on the client side — so the agent
// loop's "tool error → feedback to LLM" path is preserved.
func TestToolProvider_ErrorResultSurfaces(t *testing.T) {
	s := server.NewMCPServer("test-err", "0.0.1")
	s.AddTool(mcp.Tool{
		Name:        "always_fails",
		Description: "Always returns an error",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := mcp.NewToolResultText("nope")
		r.IsError = true
		return r, nil
	})

	c, err := mcpgo.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	p, err := NewToolProvider(context.Background(), c)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	defer p.Close()

	_, err = p.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected MCP error result to surface as Go error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should carry server's message, got %v", err)
	}
}

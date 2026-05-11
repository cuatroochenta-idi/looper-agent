package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// mcpCmd starts the MCP debug server over stdio.
// This is a CLI-level facility for debugging, NOT part of the framework runtime.
func mcpCmd(args []string) {
	_ = args
	fmt.Fprintln(os.Stderr, "MCP debug server starting on stdio...")

	// MCP protocol: read JSON-RPC from stdin, write to stdout
	// Handshake: initialize → list tools/resources/prompts
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		var req map[string]any
		if err := decoder.Decode(&req); err != nil {
			break
		}

		method, _ := req["method"].(string)
		id := req["id"]

		switch method {
		case "initialize":
			encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo": map[string]any{
						"name":    "looper-mcp-debug",
						"version": "0.1.0",
					},
					"capabilities": map[string]any{
						"tools":     map[string]any{},
						"resources": map[string]any{},
						"prompts":   map[string]any{},
					},
				},
			})

		case "tools/list":
			encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "looper_run",
							"description": "Launch a Looper Agent execution",
						},
						{
							"name":        "looper_analyze_trace",
							"description": "Analyze a Looper Agent trace for issues",
						},
						{
							"name":        "looper_replay",
							"description": "Re-run a Looper Agent execution with changes",
						},
						{
							"name":        "looper_list_history",
							"description": "List conversation history for a run",
						},
					},
				},
			})

		case "resources/list":
			encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"resources": []map[string]any{
						{"uri": "looper://runs", "name": "Recent runs", "mimeType": "application/json"},
						{"uri": "looper://costs", "name": "Cost summary", "mimeType": "application/json"},
					},
				},
			})

		case "prompts/list":
			encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"prompts": []map[string]any{
						{
							"name":        "looper_debug",
							"description": "Analyze a failed Looper Agent trace",
						},
					},
				},
			})

		default:
			encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error": map[string]any{
					"code":    -32601,
					"message": fmt.Sprintf("Method not found: %s", method),
				},
			})
		}
	}
}

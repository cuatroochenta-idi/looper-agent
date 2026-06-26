package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// nativeRFProvider is a mockProvider that advertises native response_format
// support (like the OpenAI/Gemini providers do), so the native-vs-tool
// decision can be exercised without a real provider.
type nativeRFProvider struct{ mockProvider }

func (p *nativeRFProvider) SupportsResponseFormat() bool { return true }

// TestUseNativeResponseFormat_ToollessUsesNative: a structured-output agent
// with NO tools of its own and a native-capable provider uses native
// response_format and does NOT inject the final_response tool.
func TestUseNativeResponseFormat_ToollessUsesNative(t *testing.T) {
	lp := NewAgentLoop(
		&nativeRFProvider{mockProvider{model: "m"}},
		func(_ context.Context) string { return "s" },
		nil, // no tools
		WithLoopStructuredOutput(json.RawMessage(`{"type":"object"}`)),
	)
	if !lp.useNativeResponseFormat() {
		t.Error("tool-less agent on a native-capable provider must use native response_format")
	}
	for _, tl := range lp.buildToolList(context.Background(), nil) {
		if tl.Name() == "final_response" {
			t.Error("native path must NOT inject the final_response tool")
		}
	}
}

// TestUseNativeResponseFormat_WithToolsUsesToolPath: the moment the agent has
// a tool of its own, structured output must ride the injected final_response
// tool and native response_format must be off — otherwise a strict
// OpenAI-compatible server would suppress all tool calls.
func TestUseNativeResponseFormat_WithToolsUsesToolPath(t *testing.T) {
	lp := NewAgentLoop(
		&nativeRFProvider{mockProvider{model: "m"}},
		func(_ context.Context) string { return "s" },
		[]*tool.Tool{nopTool("add_tables")},
		WithLoopStructuredOutput(json.RawMessage(`{"type":"object"}`)),
	)
	if lp.useNativeResponseFormat() {
		t.Error("a tool-using agent must NOT use native response_format")
	}
	var sawFinal bool
	for _, tl := range lp.buildToolList(context.Background(), nil) {
		if tl.Name() == "final_response" {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Error("tool path must inject the final_response tool for structured output")
	}
}

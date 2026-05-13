//go:build e2e

package e2e

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type weatherIn struct {
	City string `json:"city" jsonschema:"description=City name,required"`
}

// TestE2E_ToolCall_OpenAI asserts an end-to-end tool-calling loop:
// model picks the tool, the framework runs it, the model digests the
// result and produces a final answer mentioning the tool's output.
func TestE2E_ToolCall_OpenAI(t *testing.T) {
	p := openAIProvider(t)

	var called atomic.Int32
	weather := tool.MustNewTool(weatherIn{},
		func(_ context.Context, in weatherIn) (string, error) {
			called.Add(1)
			return "Sunny, 22°C in " + in.City + ".", nil
		},
		tool.ToolConfig{
			Name:        "get_weather",
			Description: "Returns the current weather for a city.",
		},
	)

	agent := looper.MustNewAgent(p,
		"You are a concise weather assistant. ALWAYS call get_weather "+
			"before answering, then summarize what the tool returned.",
		weather,
	)

	res, err := agent.Run(context.Background(), "What's the weather in Barcelona?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if called.Load() == 0 {
		t.Error("expected get_weather to be called at least once")
	}
	out := strings.ToLower(res.Output)
	if !strings.Contains(out, "barcelona") && !strings.Contains(out, "sunny") {
		t.Errorf("final answer should reference the tool result, got %q", res.Output)
	}
}

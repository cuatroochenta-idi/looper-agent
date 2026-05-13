// Example: agent with tools, streaming, and cost tracking.
//
// This demonstrates:
//   - Multiple tools with different schemas
//   - Sequential vs parallel tool execution
//   - Streaming iteration for real-time output
//   - Cost tracking per turn
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/03_tools_streaming/main.go
package main

import (
	"context"
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// SearchInput is the input schema for the web_search tool.
type SearchInput struct {
	Query string `json:"query" jsonschema:"description=Search query"`
}

// WeatherInput is the input schema for the weather tool.
type WeatherInput struct {
	City string `json:"city" jsonschema:"description=City name"`
}

func main() {
	ctx := context.Background()

	// 1. Create a provider
	p := openai.NewProvider("demo-key") // replace with real key

	// 2. Define tools
	searchTool := tool.MustNewTool(SearchInput{},
		func(ctx context.Context, input SearchInput) (string, error) {
			return fmt.Sprintf("Search results for: %s", input.Query), nil
		},
		tool.ToolConfig{
			Name:        "web_search",
			Description: "Search the web for information",
			Parallel:    true,
		},
	)

	weatherTool := tool.MustNewTool(WeatherInput{},
		func(ctx context.Context, input WeatherInput) (string, error) {
			return fmt.Sprintf("Weather in %s: 22C, sunny", input.City), nil
		},
		tool.ToolConfig{
			Name:        "get_weather",
			Description: "Get current weather for a city",
			Parallel:    false,
		},
	)

	// 3. Create the agent with tools
	agent := looper.MustNewAgent(p,
		"You are a helpful assistant with access to search and weather tools. Use tools when needed.",
		searchTool, weatherTool,
	)

	// 4. Run with streaming iteration
	fmt.Println("=== Agent Output ===")
	iter := agent.Iterate(ctx, "What's the weather in Barcelona and search for best tapas places?")
	for step := range iter.Next() {
		switch step.Type {
		case loop.StepLLMCall:
			fmt.Printf("[Turn %d] Thinking...\n", step.Turn)
		case loop.StepToolCall:
			fmt.Printf("[Turn %d] Calling tool: %s(%s)\n", step.Turn, step.ToolName, step.ToolArgs)
		case loop.StepToolResult:
			fmt.Printf("[Turn %d] Tool result: %s\n", step.Turn, step.Content)
		case loop.StepFinalResponse:
			fmt.Printf("\nFinal: %s\n", step.Content)
		}
	}

	fmt.Println("\nDone!")
}

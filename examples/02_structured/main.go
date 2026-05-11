// Example: structured output — the agent returns a typed Go struct.
//
// When the response type is a struct, the framework automatically:
//  1. Injects a final_response tool with the struct's JSON schema
//  2. Adds instructions to the system prompt requiring structured output
//  3. Validates the LLM response against the struct schema
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/02_structured/main.go
package main

import (
	"context"
	"fmt"
	"os"

	looper "github.com/cuatroochenta-idi/looper-agent"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// AnalysisResult is the structured output we expect from the agent.
type AnalysisResult struct {
	Sentiment string   `json:"sentiment" jsonschema:"description=Positive/Negative/Neutral,enum=Positive|Negative|Neutral"`
	Score     float64  `json:"score" jsonschema:"description=Confidence score 0-1,minimum=0,maximum=1"`
	Keywords  []string `json:"keywords" jsonschema:"description=Key topics found in the text"`
}

func main() {
	ctx := context.Background()

	// 1. Create a provider
	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))

	// 2. Define the system prompt
	systemPrompt := "You are a sentiment analysis assistant. Always analyze text carefully."

	// 3. Create the agent (no tools needed — final_response is injected automatically)
	agent := looper.NewAgent(p, systemPrompt)

	// 4. Run with structured output type
	// The framework detects AnalysisResult and handles everything automatically.
	result, err := agent.Run(ctx, "Analyze this: 'I absolutely love this product, it's amazing!'")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// 5. Print results
	fmt.Printf("Sentiment: %s\n", result.Output)
	fmt.Printf("Cost:      $%.6f (%d turns)\n", result.Cost.TotalUSD, result.Turns)

	// _ is used to suppress the unused import of tool package
	_ = tool.ToolConfig{}
}

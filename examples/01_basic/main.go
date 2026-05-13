// Example: basic conversational agent with Looper Agent.
//
// This demonstrates the simplest possible agent: a provider, a system prompt,
// and a single user input. No tools, no structured output, no streaming.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/01_basic/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

func main() {
	ctx := context.Background()

	// 1. Create a provider (OpenAI)
	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))

	// 2. Define the system prompt
	systemPrompt := "You are a helpful and concise assistant. Answer in one sentence."

	// 3. Create the agent
	agent := looper.MustNewAgent(p, systemPrompt)

	// 4. Run the agent
	result, err := agent.Run(ctx, "What is the capital of France?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// 5. Print the result
	fmt.Printf("Output: %s\n", result.Output)
	fmt.Printf("Cost:   $%.6f\n", result.Cost.TotalUSD)
	fmt.Printf("Turns:  %d\n", result.Turns)
	fmt.Printf("Status: %s\n", result.Status)
}

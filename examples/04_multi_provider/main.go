// Example: same agent, swappable provider (OpenAI / Anthropic / Google).
//
// The framework abstracts providers behind a uniform LLMProvider interface, so
// the agent code is identical no matter which vendor you pick. The selection
// happens with a single env var:
//
//	export LOOPER_PROVIDER=openai     # or "anthropic", or "google"
//	go run examples/04_multi_provider/main.go
//
// API keys come from `.env.local` — load it first:
//
//	set -a && source .env.local && set +a
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

func buildProvider(name string) (provider.LLMProvider, string, error) {
	switch name {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("ANTHROPIC_API_KEY is empty")
		}
		return anthropic.NewProvider(key), "Anthropic", nil
	case "google":
		key := os.Getenv("GOOGLE_API_KEY")
		if key == "" {
			key = os.Getenv("GEMINI_API_KEY")
		}
		if key == "" {
			return nil, "", fmt.Errorf("GOOGLE_API_KEY (or GEMINI_API_KEY) is empty")
		}
		return google.NewProvider(key), "Google", nil
	case "", "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("OPENAI_API_KEY is empty")
		}
		return openai.NewProvider(key), "OpenAI", nil
	default:
		return nil, "", fmt.Errorf("unknown provider %q (use openai|anthropic|google)", name)
	}
}

func main() {
	ctx := context.Background()

	name := os.Getenv("LOOPER_PROVIDER")
	p, label, err := buildProvider(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Provider setup: %v\n", err)
		os.Exit(1)
	}

	agent := looper.MustNewAgent(p,
		"You are a concise assistant. Answer in one sentence.",
	)

	result, err := agent.Run(ctx, "Name three rivers in Spain.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent error (%s): %v\n", label, err)
		os.Exit(1)
	}

	fmt.Printf("=== Provider: %s ===\n", label)
	fmt.Printf("Output: %s\n", result.Output)
	fmt.Printf("Cost:   $%.6f  (in=%d / out=%d / cached=%d tokens)\n",
		result.Cost.TotalUSD,
		result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CachedTokens,
	)
	fmt.Printf("Turns:  %d   Status: %s\n", result.Turns, result.Status)
}

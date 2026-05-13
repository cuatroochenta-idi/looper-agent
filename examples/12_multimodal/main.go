// Example: multi-modal input — text + image.
//
// Demonstrates message.Part: a user message can carry text and image parts
// in order. The framework's Translator for each provider converts Parts
// into the right native shape (OpenAI content arrays, Anthropic image
// blocks, Gemini inline data / file data).
//
// Because looper.Agent.Run takes a string input, multi-modal messages enter
// the agent via History: build a History with NewUserMessageWithParts, then
// run the agent with WithHistory and an empty input.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/12_multimodal/main.go
//
// Switch provider via LOOPER_PROVIDER=openai|anthropic|google.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

func pickProvider() (provider.LLMProvider, string, error) {
	switch os.Getenv("LOOPER_PROVIDER") {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("ANTHROPIC_API_KEY required")
		}
		return anthropic.NewProvider(key), "anthropic", nil
	case "google":
		key := os.Getenv("GOOGLE_API_KEY")
		if key == "" {
			key = os.Getenv("GEMINI_API_KEY")
		}
		if key == "" {
			return nil, "", fmt.Errorf("GOOGLE_API_KEY or GEMINI_API_KEY required")
		}
		return google.NewProvider(key), "google", nil
	default:
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("OPENAI_API_KEY required")
		}
		return openai.NewProvider(key), "openai", nil
	}
}

func main() {
	p, name, err := pickProvider()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Build a history with a single multi-modal user message.
	hist := message.NewHistory()
	hist.AddUserMessageParts(
		message.TextPart("What objects do you see in this image? Answer in one short sentence."),
		message.ImageURLPart("https://upload.wikimedia.org/wikipedia/commons/4/47/PNG_transparency_demonstration_1.png"),
	)

	agent := looper.MustNewAgent(p, "You are a concise vision assistant.")

	// Empty input — the message is already in the history.
	res, err := agent.Run(context.Background(), "", looper.WithHistory(hist))
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[%s] %s\n", name, res.Output)
	fmt.Printf("cost: $%.6f  turns: %d  status: %s\n", res.Cost.TotalUSD, res.Turns, res.Status)
}

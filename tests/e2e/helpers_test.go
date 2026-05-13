//go:build e2e

// Package e2e contains real-network integration tests gated by the e2e
// build tag. Run with `go test -tags e2e ./tests/e2e/...`.
package e2e

import (
	"os"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

// openAIProvider returns a configured OpenAI provider, or t.Skip when
// the API key is missing.
func openAIProvider(t *testing.T) provider.LLMProvider {
	t.Helper()
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set; skipping")
	}
	return openai.NewProvider(key, openai.WithModel("gpt-4o-mini"))
}

// geminiProvider returns a configured Google provider, or t.Skip when
// no Gemini key is set. Accepts either GOOGLE_API_KEY or GEMINI_API_KEY.
func geminiProvider(t *testing.T) provider.LLMProvider {
	t.Helper()
	key := os.Getenv("GOOGLE_API_KEY")
	if key == "" {
		key = os.Getenv("GEMINI_API_KEY")
	}
	if key == "" {
		t.Skip("GOOGLE_API_KEY / GEMINI_API_KEY not set; skipping")
	}
	return google.NewProvider(key,
		google.WithModel("gemini-flash-latest"),
		// Avoid the thinking-model "burns budget on hidden reasoning"
		// trap surfaced by the multi-modal e2e probe.
		google.WithThinkingBudget(0),
	)
}

// anthropicProvider — same pattern for Claude.
func anthropicProvider(t *testing.T) provider.LLMProvider {
	t.Helper()
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping")
	}
	return anthropic.NewProvider(key)
}

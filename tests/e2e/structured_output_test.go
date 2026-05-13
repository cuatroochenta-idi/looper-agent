//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// sentimentResult is the shape we ask the model to fill. Constraints
// (enum, range) get translated into the JSON Schema we hand the
// provider — exercising both the native response_format path (OpenAI,
// Gemini) and the final_response tool-injection fallback (Anthropic).
type sentimentResult struct {
	Sentiment string  `json:"sentiment" jsonschema:"enum=positive|negative|neutral,required"`
	Score     float64 `json:"score" jsonschema:"minimum=0,maximum=1"`
}

func runStructuredProbe(t *testing.T, p provider.LLMProvider) sentimentResult {
	t.Helper()
	agent := looper.MustNewAgent(p,
		"You are a sentiment analyzer. Be precise.",
		looper.WithStructuredOutput[sentimentResult](),
	)
	res, err := agent.Run(context.Background(),
		"Analyze: 'this product is amazing, I love it!'")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var out sentimentResult
	if err := looper.Decode(res, &out); err != nil {
		t.Fatalf("decode: %v\nraw=%s", err, res.Output)
	}
	return out
}

// TestE2E_StructuredOutput_OpenAI exercises the native response_format
// path on OpenAI.
func TestE2E_StructuredOutput_OpenAI(t *testing.T) {
	got := runStructuredProbe(t, openAIProvider(t))
	if !strings.EqualFold(got.Sentiment, "positive") {
		t.Errorf("expected positive sentiment, got %+v", got)
	}
	if got.Score < 0 || got.Score > 1 {
		t.Errorf("score out of range: %v", got.Score)
	}
}

// TestE2E_StructuredOutput_Gemini exercises Gemini's responseSchema.
func TestE2E_StructuredOutput_Gemini(t *testing.T) {
	got := runStructuredProbe(t, geminiProvider(t))
	if !strings.EqualFold(got.Sentiment, "positive") {
		t.Errorf("expected positive sentiment, got %+v", got)
	}
}

// TestE2E_StructuredOutput_Anthropic exercises the tool-injection
// fallback on Anthropic (no native response_format).
func TestE2E_StructuredOutput_Anthropic(t *testing.T) {
	got := runStructuredProbe(t, anthropicProvider(t))
	if !strings.EqualFold(got.Sentiment, "positive") {
		t.Errorf("expected positive sentiment, got %+v", got)
	}
}

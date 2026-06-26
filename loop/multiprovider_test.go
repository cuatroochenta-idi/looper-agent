package loop

import (
	"math"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
)

// TestRunStats_APICostAccumulatesAndOverridesMatrix proves the full cost
// threading: per-call provider.Usage.Cost is summed in the accumulator and,
// at snapshot time, the cost model uses that API-reported total instead of
// the hardcoded matrix. This is the end-to-end check for the
// API-cost-with-matrix-fallback feature at the loop layer.
func TestRunStats_APICostAccumulatesAndOverridesMatrix(t *testing.T) {
	stats := newRunStats()
	stats.add(&provider.LLMResponse{
		ProviderID: "openai",
		ModelID:    "gpt-4o",
		Usage:      provider.Usage{InputTokens: 1000, OutputTokens: 500, Cost: 0.20},
	}, "openai", "gpt-4o")
	stats.add(&provider.LLMResponse{
		ProviderID: "openai",
		ModelID:    "gpt-4o",
		Usage:      provider.Usage{InputTokens: 1000, OutputTokens: 500, Cost: 0.30},
	}, "openai", "gpt-4o")

	cm := telemetry.NewCostModel()
	out := stats.snapshot(func(p, m string, u provider.Usage) CostBreakdown {
		return providerCostFor(cm, p, m, u)
	})
	if len(out) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(out))
	}
	// 0.20 + 0.30 = 0.50 reported by the API, NOT the matrix's 0.015 for
	// 2000 in / 1000 out.
	if got := out[0].Cost.TotalUSD; math.Abs(got-0.50) > 1e-9 {
		t.Errorf("entry.Cost.TotalUSD = %v, want 0.50 (accumulated API cost)", got)
	}
	if c := out[0].Cost; math.Abs((c.InputUSD+c.OutputUSD+c.CachedUSD)-c.TotalUSD) > 1e-9 {
		t.Errorf("split %v+%v+%v != total %v", c.InputUSD, c.OutputUSD, c.CachedUSD, c.TotalUSD)
	}
}

// TestRunStats_PerProviderBreakdown drives the runStats accumulator
// directly to validate that per-(provider, model) usage and fallback
// counts survive into the public ProviderStats slice in first-seen order.
func TestRunStats_PerProviderBreakdown(t *testing.T) {
	stats := newRunStats()

	// Two openai calls, then one google call with Fallback=true.
	stats.add(&provider.LLMResponse{
		ProviderID: "openai",
		ModelID:    "gpt-x",
		Usage:      provider.Usage{InputTokens: 100, OutputTokens: 50},
	}, "openai", "gpt-x")
	stats.add(&provider.LLMResponse{
		ProviderID: "openai",
		ModelID:    "gpt-x",
		Usage:      provider.Usage{InputTokens: 200, OutputTokens: 80},
	}, "openai", "gpt-x")
	stats.add(&provider.LLMResponse{
		ProviderID: "google",
		ModelID:    "gemini-x",
		Fallback:   true,
		Usage:      provider.Usage{InputTokens: 300, OutputTokens: 100},
	}, "openai", "gpt-x")

	// snapshot without a cost model still reports tokens.
	out := stats.snapshot(nil)
	if len(out) != 2 {
		t.Fatalf("len(snapshot) = %d, want 2 (one openai entry + one google entry)", len(out))
	}
	openai, google := out[0], out[1]
	if openai.Provider != "openai" || openai.Model != "gpt-x" {
		t.Errorf("entry[0] = %+v, want openai/gpt-x", openai)
	}
	if openai.Calls != 2 {
		t.Errorf("openai.Calls = %d, want 2", openai.Calls)
	}
	if openai.FallbackCalls != 0 {
		t.Errorf("openai.FallbackCalls = %d, want 0", openai.FallbackCalls)
	}
	if openai.Usage.InputTokens != 300 || openai.Usage.OutputTokens != 130 {
		t.Errorf("openai.Usage = %+v, want In=300 Out=130", openai.Usage)
	}
	if google.Provider != "google" || google.Model != "gemini-x" {
		t.Errorf("entry[1] = %+v, want google/gemini-x", google)
	}
	if google.Calls != 1 || google.FallbackCalls != 1 {
		t.Errorf("google entry: Calls=%d FallbackCalls=%d, want 1/1", google.Calls, google.FallbackCalls)
	}
	if stats.fallbackCount() != 1 {
		t.Errorf("stats.fallbackCount() = %d, want 1", stats.fallbackCount())
	}
}

// TestRunStats_FallbackDefaults asserts that empty ProviderID/ModelID on
// the response fall back to the loop-level labels so single-provider
// deployments still see a properly-labelled bucket.
func TestRunStats_FallbackDefaults(t *testing.T) {
	stats := newRunStats()
	stats.add(&provider.LLMResponse{
		// No ProviderID / ModelID — legacy provider.
		Usage: provider.Usage{InputTokens: 50, OutputTokens: 10},
	}, "anthropic", "claude-x")

	out := stats.snapshot(nil)
	if len(out) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(out))
	}
	if out[0].Provider != "anthropic" || out[0].Model != "claude-x" {
		t.Errorf("entry = %+v, want anthropic/claude-x (defaulted from fallback labels)", out[0])
	}
}

// TestRunStats_ChunkPath asserts addChunk only records final chunks
// with usage, so intermediate streaming deltas don't double-count.
func TestRunStats_ChunkPath(t *testing.T) {
	stats := newRunStats()
	// Intermediate chunk — should be ignored.
	stats.addChunk(provider.StreamChunk{
		ProviderID: "openai",
		ModelID:    "gpt-x",
		IsFinal:    false,
		Usage:      &provider.Usage{InputTokens: 999},
	}, "openai", "gpt-x")
	// Final chunk — should land.
	stats.addChunk(provider.StreamChunk{
		ProviderID: "openai",
		ModelID:    "gpt-x",
		IsFinal:    true,
		Usage:      &provider.Usage{InputTokens: 42, OutputTokens: 8},
	}, "openai", "gpt-x")

	out := stats.snapshot(nil)
	if len(out) != 1 {
		t.Fatalf("len(snapshot) = %d, want 1", len(out))
	}
	if out[0].Usage.InputTokens != 42 || out[0].Usage.OutputTokens != 8 {
		t.Errorf("entry.Usage = %+v, want In=42 Out=8 (intermediate chunk must be dropped)", out[0].Usage)
	}
}

package telemetry

import (
	"testing"
)

func TestCostModelCalculate(t *testing.T) {
	cm := NewCostModel()

	tests := []struct {
		name       string
		provider   string
		model      string
		usage      Usage
		wantTotal  float64
		wantCached float64
		wantSavings float64
	}{
		{
			name:       "gpt-4o basic",
			provider:   "openai",
			model:      "gpt-4o",
			usage:      Usage{InputTokens: 1000, OutputTokens: 500, CachedTokens: 0},
			wantTotal:  0.0075, // 1000/1M * 2.50 + 500/1M * 10.00 = 0.0025 + 0.005 = 0.0075
			wantCached: 0,
			wantSavings: 0,
		},
		{
			name:       "gpt-4o with cached tokens",
			provider:   "openai",
			model:      "gpt-4o",
			usage:      Usage{InputTokens: 1000, OutputTokens: 500, CachedTokens: 1000},
			wantTotal:  0.00625, // 0/1M*2.50 + 1000/1M*1.25 + 500/1M*10 = 0 + 0.00125 + 0.005 = 0.00625
			wantCached: 0.00125,
			wantSavings: 0.00125, // (1000/1M*2.50) - 0.00125 = 0.0025 - 0.00125 = 0.00125
		},
		{
			name:       "gpt-4o-mini zero tokens",
			provider:   "openai",
			model:      "gpt-4o-mini",
			usage:      Usage{InputTokens: 0, OutputTokens: 0, CachedTokens: 0},
			wantTotal:  0,
			wantCached: 0,
			wantSavings: 0,
		},
		{
			name:       "claude sonnet",
			provider:   "anthropic",
			model:      "claude-sonnet-4-20250514",
			usage:      Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CachedTokens: 0},
			wantTotal:  18.0, // 1M/1M*3.0 + 1M/1M*15.0 = 3 + 15 = 18
			wantCached: 0,
			wantSavings: 0,
		},
		{
			name:       "claude sonnet cached",
			provider:   "anthropic",
			model:      "claude-sonnet-4-20250514",
			usage:      Usage{InputTokens: 1_000_000, OutputTokens: 500_000, CachedTokens: 1_000_000},
			wantTotal:  7.8, // 0 + 1M/1M*0.30 + 500k/1M*15 = 0 + 0.30 + 7.5 = 7.80
			wantCached: 0.30,
			wantSavings: 2.70, // (1M/1M*3.0) - 0.30 = 2.70
		},
		{
			name:       "gemini flash",
			provider:   "google",
			model:      "gemini-2.5-flash",
			usage:      Usage{InputTokens: 100_000, OutputTokens: 10_000, CachedTokens: 0},
			wantTotal:  0.021, // 100k/1M*0.15 + 10k/1M*0.60 = 0.015 + 0.006 = 0.021
			wantCached: 0,
			wantSavings: 0,
		},
		{
			name:       "custom model with zero cost",
			provider:   "custom",
			model:      "local-llama",
			usage:      Usage{InputTokens: 10000, OutputTokens: 5000},
			wantTotal:  0,
			wantCached: 0,
			wantSavings: 0,
		},
		{
			name:       "unknown model returns zero",
			provider:   "unknown",
			model:      "nonexistent",
			usage:      Usage{InputTokens: 1000, OutputTokens: 100},
			wantTotal:  0,
			wantCached: 0,
			wantSavings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cm.Calculate(tt.provider, tt.model, tt.usage)

			if !almostEqual(result.TotalUSD, tt.wantTotal, 0.0001) {
				t.Errorf("TotalUSD = %.6f, want %.6f", result.TotalUSD, tt.wantTotal)
			}
			if !almostEqual(result.CachedUSD, tt.wantCached, 0.0001) {
				t.Errorf("CachedUSD = %.6f, want %.6f", result.CachedUSD, tt.wantCached)
			}
			if !almostEqual(result.SavingsUSD, tt.wantSavings, 0.0001) {
				t.Errorf("SavingsUSD = %.6f, want %.6f", result.SavingsUSD, tt.wantSavings)
			}
			if result.InputTokens != tt.usage.InputTokens {
				t.Errorf("InputTokens = %d, want %d", result.InputTokens, tt.usage.InputTokens)
			}
			if result.OutputTokens != tt.usage.OutputTokens {
				t.Errorf("OutputTokens = %d, want %d", result.OutputTokens, tt.usage.OutputTokens)
			}
		})
	}
}

func TestCostModelUpdateCost(t *testing.T) {
	cm := NewCostModel()

	// Override gpt-4o price
	cm.UpdateCost("openai", "gpt-4o", CostConfig{
		InputCostPer1MTokens:  1.0,
		OutputCostPer1MTokens: 5.0,
		CachedCostPer1MTokens: 0.5,
	})

	result := cm.Calculate("openai", "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 500_000})
	expected := 1.0 + 2.5 // 1M/1M*1.0 + 500k/1M*5.0 = 1.0 + 2.5 = 3.5
	if !almostEqual(result.TotalUSD, expected, 0.0001) {
		t.Errorf("after update: TotalUSD = %.6f, want %.6f", result.TotalUSD, expected)
	}
}

func TestCostModelWithCustomCost(t *testing.T) {
	cm := NewCostModel()

	cm.WithCustomCost("my-llm", CostConfig{
		InputCostPer1MTokens:  0.50,
		OutputCostPer1MTokens: 1.00,
		CachedCostPer1MTokens: 0.25,
	})

	result := cm.Calculate("custom", "my-llm", Usage{InputTokens: 1_000_000, OutputTokens: 500_000})
	expected := 0.50 + 0.50 // 1M/1M*0.50 + 500k/1M*1.00 = 0.50 + 0.50 = 1.00
	if !almostEqual(result.TotalUSD, expected, 0.0001) {
		t.Errorf("custom model: TotalUSD = %.6f, want %.6f", result.TotalUSD, expected)
	}
}

func TestCostBreakdownFields(t *testing.T) {
	cm := NewCostModel()
	result := cm.Calculate("openai", "gpt-4o", Usage{
		InputTokens:  1000,
		OutputTokens: 500,
		CachedTokens: 0,
	})

	if result.InputTokens != 1000 {
		t.Errorf("input tokens not preserved")
	}
	if result.OutputTokens != 500 {
		t.Errorf("output tokens not preserved")
	}
	if result.CachedTokens != 0 {
		t.Errorf("cached tokens wrong")
	}
	if result.InputUSD+result.OutputUSD != result.TotalUSD {
		t.Errorf("input + output != total: %.6f + %.6f = %.6f vs %.6f",
			result.InputUSD, result.OutputUSD, result.InputUSD+result.OutputUSD, result.TotalUSD)
	}
}

func TestCostModelDefaultPricesExist(t *testing.T) {
	cm := NewCostModel()

	providers := []string{"openai", "anthropic", "google"}
	for _, p := range providers {
		result := cm.Calculate(p, "nonexistent-model", Usage{InputTokens: 100, OutputTokens: 50})
		// Should return zero for unknown models in known providers
		if result.TotalUSD != 0 {
			t.Errorf("provider %s unknown model should return 0, got %.6f", p, result.TotalUSD)
		}
	}
}

// TestCostModelFamilyPrefixLookup verifies that dated and minor-version
// model ids inherit pricing from their registered family entry. This is
// the safety net that keeps cost tracking from silently dropping to $0
// whenever OpenAI/Anthropic/Google ship a new point release.
func TestCostModelFamilyPrefixLookup(t *testing.T) {
	cm := NewCostModel()

	tests := []struct {
		name       string
		provider   string
		model      string
		familyKey  string // the entry the prefix lookup is expected to resolve to
		usage      Usage
	}{
		{
			name:      "openai gpt-5 dated id falls back to family",
			provider:  "openai",
			model:     "gpt-5-2025-08-07",
			familyKey: "gpt-5",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		},
		{
			name:      "openai gpt-5.5 inherits gpt-5 pricing across dot boundary",
			provider:  "openai",
			model:     "gpt-5.5",
			familyKey: "gpt-5",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000},
		},
		{
			name:      "openai gpt-5-mini wins over gpt-5 (longest prefix)",
			provider:  "openai",
			model:     "gpt-5-mini-2025-08-07",
			familyKey: "gpt-5-mini",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000},
		},
		{
			name:      "anthropic claude-opus-4-7 inherits opus-4 family",
			provider:  "anthropic",
			model:     "claude-opus-4-7-20260301",
			familyKey: "claude-opus-4",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000},
		},
		{
			name:      "anthropic claude-sonnet-4-5 inherits sonnet-4 family",
			provider:  "anthropic",
			model:     "claude-sonnet-4-5",
			familyKey: "claude-sonnet-4",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000, CachedTokens: 400_000},
		},
		{
			name:      "anthropic claude-haiku-4 family lookup",
			provider:  "anthropic",
			model:     "claude-haiku-4-1",
			familyKey: "claude-haiku-4",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000},
		},
		{
			name:      "openai dated gpt-4o resolves to family",
			provider:  "openai",
			model:     "gpt-4o-2024-08-06",
			familyKey: "gpt-4o",
			usage:     Usage{InputTokens: 1_000_000, OutputTokens: 500_000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cm.Calculate(tt.provider, tt.model, tt.usage)
			want := cm.Calculate(tt.provider, tt.familyKey, tt.usage)
			if !almostEqual(got.TotalUSD, want.TotalUSD, 0.0001) {
				t.Errorf("TotalUSD = %.6f, want %.6f (inherited from %q)",
					got.TotalUSD, want.TotalUSD, tt.familyKey)
			}
			if got.TotalUSD == 0 {
				t.Errorf("family lookup returned zero — expected inheritance from %q", tt.familyKey)
			}
		})
	}
}

// TestCostModelFamilyPrefixBoundary makes sure prefix matching respects
// `-` / `.` family separators so we never silently bill an unrelated model
// with a sister family's price.
func TestCostModelFamilyPrefixBoundary(t *testing.T) {
	cm := NewCostModel()
	// Inject a synthetic family entry whose prefix would naively match a
	// neighbouring id. With the boundary guard, "gpt-40" must NOT pick up
	// "gpt-4" pricing because the next char after the prefix is `0`, not
	// `-` or `.`.
	cm.UpdateCost("openai", "gpt-4", CostConfig{
		InputCostPer1MTokens:  99.0,
		OutputCostPer1MTokens: 99.0,
	})

	result := cm.Calculate("openai", "gpt-40", Usage{InputTokens: 1_000_000})
	if result.TotalUSD != 0 {
		t.Errorf("gpt-40 must not inherit gpt-4 pricing, got %.6f", result.TotalUSD)
	}
}

// TestCostModelWarnMissDoesNotPanic exercises the warn path on a miss and
// confirms repeated calls for the same (provider, model) stay quiet — the
// log itself is best-effort, but the dedupe map must not blow up.
func TestCostModelWarnMissDoesNotPanic(t *testing.T) {
	cm := NewCostModel()
	for range 5 {
		_ = cm.Calculate("unknown-provider", "unknown-model", Usage{InputTokens: 100, OutputTokens: 50})
	}
}

func almostEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}

// gpt-5.6 tiers must price at their own rates, not fall back to the gpt-5
// family entry (4× cheaper for Sol), and dated ids must inherit their tier.
func TestCostModelGPT56Tiers(t *testing.T) {
	cm := NewCostModel()

	tests := []struct {
		model     string
		wantTotal float64
	}{
		// 1M in + 1M out at the official July-2026 rates.
		{"gpt-5.6-sol", 35.00},
		{"gpt-5.6-terra", 17.50},
		{"gpt-5.6-luna", 7.00},
		// Dated suffix inherits its tier via longest-family-prefix.
		{"gpt-5.6-sol-2026-07-09", 35.00},
	}
	usage := Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	for _, tt := range tests {
		got := cm.Calculate("openai", tt.model, usage)
		if !got.Estimated {
			t.Errorf("%s: want Estimated=true, got false", tt.model)
		}
		if diff := got.TotalUSD - tt.wantTotal; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: TotalUSD = %v, want %v", tt.model, got.TotalUSD, tt.wantTotal)
		}
	}

	// Cache reads keep the 90% discount; cache writes fall back to 1.25×
	// input, matching the official $6.25/M for Sol.
	cw := cm.Calculate("openai", "gpt-5.6-sol", Usage{InputTokens: 1_000_000, CachedTokens: 500_000, CacheWriteTokens: 500_000})
	want := 0.50*0.5 + 6.25*0.5 // cached half + cache-write half
	if diff := cw.TotalUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("sol cache buckets: TotalUSD = %v, want %v", cw.TotalUSD, want)
	}
}

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

func almostEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}

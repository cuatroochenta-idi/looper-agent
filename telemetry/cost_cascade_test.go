package telemetry

import "testing"

// TestCostCascade_CustomBareKeyBeatsMatrix asserts a bare-key WithCustomCosts
// entry outranks the built-in provider matrix: the same (provider, model) must
// price from the custom "custom" table, not openai's default gpt-4o rates.
func TestCostCascade_CustomBareKeyBeatsMatrix(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCosts(map[string]CostConfig{
		"gpt-4o": {InputCostPer1MTokens: 1.0, OutputCostPer1MTokens: 2.0},
	})

	got := cm.Calculate("openai", "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	// Custom: 1.0 + 2.0 = 3.0. Built-in matrix would bill 2.50 + 10.00 = 12.50.
	if !almostEqual(got.TotalUSD, 3.0, 0.0001) {
		t.Errorf("TotalUSD = %.6f, want 3.0 (custom overrides matrix)", got.TotalUSD)
	}
}

// TestCostCascade_CustomFamilyPrefix asserts a bare-key family entry in the
// custom table is inherited by a dated / minor-version id through the same
// `-`/`.` boundary rule the matrix uses.
func TestCostCascade_CustomFamilyPrefix(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCosts(map[string]CostConfig{
		"my-model": {InputCostPer1MTokens: 5.0, OutputCostPer1MTokens: 10.0},
	})

	got := cm.Calculate("any-provider", "my-model-v2", Usage{InputTokens: 1_000_000})
	if !almostEqual(got.TotalUSD, 5.0, 0.0001) {
		t.Errorf("TotalUSD = %.6f, want 5.0 (my-model-v2 inherits my-model)", got.TotalUSD)
	}
}

// TestCostCascade_ProviderScopedKeyOverridesTable asserts a "provider/model"
// key writes into that provider's own table (via UpdateCost) and therefore
// replaces the matrix entry directly rather than racing it.
func TestCostCascade_ProviderScopedKeyOverridesTable(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCosts(map[string]CostConfig{
		"openai/gpt-4o": {InputCostPer1MTokens: 7.0, OutputCostPer1MTokens: 9.0},
	})

	got := cm.Calculate("openai", "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	// 7.0 + 9.0 = 16.0; distinct from the built-in 12.50.
	if !almostEqual(got.TotalUSD, 16.0, 0.0001) {
		t.Errorf("TotalUSD = %.6f, want 16.0 (provider-scoped override)", got.TotalUSD)
	}
}

// TestCostCascade_CacheWritePricing covers both cache-write branches: an
// explicit CacheWriteCostPer1MTokens is used verbatim, and a zero rate with
// cache-write tokens present falls back to the 1.25× input Anthropic rule.
func TestCostCascade_CacheWritePricing(t *testing.T) {
	tests := []struct {
		name          string
		cfg           CostConfig
		usage         Usage
		wantCacheUSD  float64
	}{
		{
			name:         "explicit cache-write rate used verbatim",
			cfg:          CostConfig{InputCostPer1MTokens: 10.0, CacheWriteCostPer1MTokens: 20.0},
			usage:        Usage{InputTokens: 1_000_000, CacheWriteTokens: 1_000_000},
			wantCacheUSD: 20.0,
		},
		{
			name:         "zero rate falls back to 1.25x input",
			cfg:          CostConfig{InputCostPer1MTokens: 8.0},
			usage:        Usage{InputTokens: 1_000_000, CacheWriteTokens: 1_000_000},
			wantCacheUSD: 10.0, // 8.0 * 1.25
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewCostModel()
			cm.WithCustomCost("cw-model", tt.cfg)
			got := cm.Calculate("custom", "cw-model", tt.usage)
			if !almostEqual(got.CacheWriteUSD, tt.wantCacheUSD, 0.0001) {
				t.Errorf("CacheWriteUSD = %.6f, want %.6f", got.CacheWriteUSD, tt.wantCacheUSD)
			}
		})
	}
}

// TestCostCascade_ClampNoNegativeInput asserts the ⊆-contract guard: when a
// provider over-reports cached + cache-write tokens beyond the inclusive
// InputTokens total, the full-rate input bucket clamps to zero instead of
// going negative and cancelling real charges.
func TestCostCascade_ClampNoNegativeInput(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("clamp-model", CostConfig{
		InputCostPer1MTokens:      10.0,
		CachedCostPer1MTokens:     1.0,
		CacheWriteCostPer1MTokens: 12.0,
	})

	// Cached (80) + cache-write (80) exceed InputTokens (100): non-cached input
	// is max(100-160,0)=0, never negative.
	got := cm.Calculate("custom", "clamp-model", Usage{
		InputTokens: 100, CachedTokens: 80, CacheWriteTokens: 80,
	})
	if got.InputUSD < 0 {
		t.Errorf("InputUSD = %.6f, must not be negative", got.InputUSD)
	}
	// InputUSD is exactly the cached + cache-write charges (no full-rate term).
	wantInput := got.CachedUSD + got.CacheWriteUSD
	if !almostEqual(got.InputUSD, wantInput, 0.0001) {
		t.Errorf("InputUSD = %.6f, want %.6f (cached + cache-write only)", got.InputUSD, wantInput)
	}
}

// TestCostCascade_EstimatedFlag pins the three Estimated states: true only on
// pure table pricing, false when an API cost is authoritative, and false on a
// total pricing miss (the $0 case warns instead of claiming an estimate).
func TestCostCascade_EstimatedFlag(t *testing.T) {
	cm := NewCostModel()

	t.Run("table pricing is estimated", func(t *testing.T) {
		got := cm.Calculate("openai", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 500})
		if !got.Estimated {
			t.Errorf("Estimated = false, want true for table-priced total")
		}
	})

	t.Run("api-reported cost is not estimated", func(t *testing.T) {
		got := cm.Calculate("openai", "gpt-4o", Usage{InputTokens: 1000, Cost: 0.5})
		if got.Estimated {
			t.Errorf("Estimated = true, want false when usage.Cost is authoritative")
		}
		if !almostEqual(got.TotalUSD, 0.5, 0.0001) {
			t.Errorf("TotalUSD = %.6f, want 0.5 (API cost)", got.TotalUSD)
		}
	})

	t.Run("total miss is not estimated", func(t *testing.T) {
		got := cm.Calculate("nope-provider", "nope-model", Usage{InputTokens: 1000})
		if got.Estimated {
			t.Errorf("Estimated = true, want false on a pricing miss")
		}
		if got.TotalUSD != 0 {
			t.Errorf("TotalUSD = %.6f, want 0 on a pricing miss", got.TotalUSD)
		}
	})
}

// TestCostCascade_APICostRescalesSplit asserts case (g): an API-reported total
// with a matching pricing table re-scales the component split so it still sums
// to the authoritative total, and CacheWriteUSD participates in that split.
func TestCostCascade_APICostRescalesSplit(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("rescale-model", CostConfig{
		InputCostPer1MTokens:      10.0,
		OutputCostPer1MTokens:     20.0,
		CacheWriteCostPer1MTokens: 25.0,
	})

	got := cm.Calculate("custom", "rescale-model", Usage{
		InputTokens:      1_000_000,
		CacheWriteTokens: 1_000_000,
		OutputTokens:     1_000_000,
		Cost:             100.0,
	})

	if got.Estimated {
		t.Errorf("Estimated = true, want false with an API-reported cost")
	}
	if !almostEqual(got.TotalUSD, 100.0, 0.0001) {
		t.Errorf("TotalUSD = %.6f, want 100.0 (API cost is authoritative)", got.TotalUSD)
	}
	if got.CacheWriteUSD <= 0 {
		t.Errorf("CacheWriteUSD = %.6f, want > 0 (participates in the re-scaled split)", got.CacheWriteUSD)
	}
	// The re-scaled components still reconcile to the authoritative total.
	if !almostEqual(got.InputUSD+got.OutputUSD, got.TotalUSD, 0.0001) {
		t.Errorf("InputUSD + OutputUSD = %.6f, want %.6f", got.InputUSD+got.OutputUSD, got.TotalUSD)
	}
}

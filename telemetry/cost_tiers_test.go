package telemetry

import (
	"encoding/json"
	"testing"
)

// tieredConfig is a synthetic model with one long-context tier, so these
// tests pin the tier mechanics independently of the built-in price table.
func tieredConfig() CostConfig {
	return CostConfig{
		InputCostPer1MTokens:      1.00,
		OutputCostPer1MTokens:     10.00,
		CachedCostPer1MTokens:     0.10,
		CacheWriteCostPer1MTokens: 1.25,
		Tiers: []PriceTier{{
			AboveInputTokens:          272_000,
			InputCostPer1MTokens:      2.00,
			OutputCostPer1MTokens:     15.00,
			CachedCostPer1MTokens:     0.20,
			CacheWriteCostPer1MTokens: 2.50,
		}},
	}
}

func TestCostTiers_ThresholdIsStrictlyAbove(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("tiered", tieredConfig())

	tests := []struct {
		name  string
		input int
		want  float64 // input + 1M output
	}{
		{"below threshold bills base", 100_000, 0.1*1.00 + 10.00},
		{"at threshold bills base", 272_000, 0.272*1.00 + 10.00},
		{"one token above bills tier", 272_001, 0.272001*2.00 + 15.00},
		{"far above bills tier", 900_000, 0.9*2.00 + 15.00},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cm.Calculate("custom", "tiered", Usage{InputTokens: tt.input, OutputTokens: 1_000_000})
			if !almostEqual(got.TotalUSD, tt.want, 1e-9) {
				t.Errorf("TotalUSD = %.9f, want %.9f", got.TotalUSD, tt.want)
			}
		})
	}
}

// The prompt size that picks the tier includes cached reads and cache
// writes: providers bill by the whole prompt, and Usage.InputTokens is the
// inclusive total.
func TestCostTiers_CachedTokensCountTowardThreshold(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("tiered", tieredConfig())

	got := cm.Calculate("custom", "tiered", Usage{
		InputTokens:      300_000, // 20K fresh + 250K cached + 30K written
		CachedTokens:     250_000,
		CacheWriteTokens: 30_000,
	})
	want := 0.02*2.00 + 0.25*0.20 + 0.03*2.50
	if !almostEqual(got.TotalUSD, want, 1e-9) {
		t.Errorf("TotalUSD = %.9f, want %.9f (every bucket at the tier rate)", got.TotalUSD, want)
	}
	// Savings are measured against the tier's input rate, the rate those
	// cached tokens would otherwise have paid.
	if wantSav := 0.25*2.00 - 0.25*0.20; !almostEqual(got.SavingsUSD, wantSav, 1e-9) {
		t.Errorf("SavingsUSD = %.9f, want %.9f", got.SavingsUSD, wantSav)
	}
}

// A zero rate in a tier keeps the base rate; the cache-write fallback then
// runs against the effective (tier) input rate.
func TestCostTiers_ZeroTierRateKeepsBase(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("partial", CostConfig{
		InputCostPer1MTokens:  1.00,
		OutputCostPer1MTokens: 10.00,
		CachedCostPer1MTokens: 0.10,
		Tiers:                 []PriceTier{{AboveInputTokens: 1000, InputCostPer1MTokens: 2.00}},
	})

	got := cm.Calculate("custom", "partial", Usage{
		InputTokens:      1_000_000,
		CachedTokens:     500_000,
		CacheWriteTokens: 500_000,
		OutputTokens:     1_000_000,
	})
	want := 0.5*0.10 + 0.5*(2.00*1.25) + 10.00
	if !almostEqual(got.TotalUSD, want, 1e-9) {
		t.Errorf("TotalUSD = %.9f, want %.9f", got.TotalUSD, want)
	}
}

// With several tiers the highest one the prompt exceeds wins, whatever
// order they are declared in.
func TestCostTiers_HighestApplicableTierWins(t *testing.T) {
	cm := NewCostModel()
	cm.WithCustomCost("stairs", CostConfig{
		InputCostPer1MTokens: 1.00,
		Tiers: []PriceTier{
			{AboveInputTokens: 500_000, InputCostPer1MTokens: 3.00},
			{AboveInputTokens: 100_000, InputCostPer1MTokens: 2.00},
		},
	})

	for input, rate := range map[int]float64{50_000: 1.00, 200_000: 2.00, 600_000: 3.00} {
		got := cm.Calculate("custom", "stairs", Usage{InputTokens: input})
		if want := float64(input) / 1e6 * rate; !almostEqual(got.TotalUSD, want, 1e-9) {
			t.Errorf("input %d: TotalUSD = %.9f, want %.9f (rate %.2f)", input, got.TotalUSD, want, rate)
		}
	}
}

// looper.json model_costs written before tiers existed must keep decoding
// to flat pricing, and the new shape must round-trip.
func TestCostTiers_JSONBackwardCompatible(t *testing.T) {
	var flat CostConfig
	if err := json.Unmarshal([]byte(`{"input": 3, "output": 15, "cached": 0.3, "cache_write": 3.75}`), &flat); err != nil {
		t.Fatal(err)
	}
	if flat.Tiers != nil {
		t.Errorf("legacy JSON: Tiers = %v, want nil (flat)", flat.Tiers)
	}
	if out, _ := json.Marshal(flat); string(out) != `{"input":3,"output":15,"cached":0.3,"cache_write":3.75}` {
		t.Errorf("flat config must marshal without a tiers key, got %s", out)
	}

	var tiered CostConfig
	src := `{"input": 2, "output": 10, "tiers": [{"above_input_tokens": 272000, "input": 4, "output": 15, "cached": 0.4, "cache_write": 5}]}`
	if err := json.Unmarshal([]byte(src), &tiered); err != nil {
		t.Fatal(err)
	}
	want := PriceTier{AboveInputTokens: 272_000, InputCostPer1MTokens: 4, OutputCostPer1MTokens: 15, CachedCostPer1MTokens: 0.4, CacheWriteCostPer1MTokens: 5}
	if len(tiered.Tiers) != 1 || tiered.Tiers[0] != want {
		t.Errorf("Tiers = %+v, want [%+v]", tiered.Tiers, want)
	}
}

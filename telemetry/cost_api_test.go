package telemetry

import (
	"math"
	"testing"
)

const costEpsilon = 1e-9

func approxEqual(a, b float64) bool { return math.Abs(a-b) < costEpsilon }

// When the upstream API reports a cost (Usage.Cost > 0), it is authoritative
// for TotalUSD; the input/output/cached split is estimated from the matrix
// ratio so the breakdown stays populated.
func TestCostModelCalculate_APICostWithMatrixSplit(t *testing.T) {
	cm := NewCostModel()
	// gpt-4o: input 2.50/1M, output 10.00/1M. For 1000 in / 500 out the matrix
	// total is 0.0075 with ratios input=1/3, output=2/3.
	u := Usage{InputTokens: 1000, OutputTokens: 500, Cost: 0.50}

	br := cm.Calculate("openai", "gpt-4o", u)

	if !approxEqual(br.TotalUSD, 0.50) {
		t.Errorf("TotalUSD = %v, want 0.50 (API-reported cost is authoritative)", br.TotalUSD)
	}
	if !approxEqual(br.InputUSD, 0.50*(1.0/3.0)) {
		t.Errorf("InputUSD = %v, want %v (matrix-ratio split of API cost)", br.InputUSD, 0.50*(1.0/3.0))
	}
	if !approxEqual(br.OutputUSD, 0.50*(2.0/3.0)) {
		t.Errorf("OutputUSD = %v, want %v", br.OutputUSD, 0.50*(2.0/3.0))
	}
	if !approxEqual(br.InputUSD+br.OutputUSD+br.CachedUSD, br.TotalUSD) {
		t.Errorf("split does not sum to total: %v + %v + %v != %v",
			br.InputUSD, br.OutputUSD, br.CachedUSD, br.TotalUSD)
	}
	if br.InputTokens != 1000 || br.OutputTokens != 500 {
		t.Errorf("token counts not preserved: in=%d out=%d", br.InputTokens, br.OutputTokens)
	}
}

// When the API reports a cost for a model with no matrix entry, the total is
// still the API cost; the split degrades to zero rather than being invented.
func TestCostModelCalculate_APICostNoMatrixMatch(t *testing.T) {
	cm := NewCostModel()
	u := Usage{InputTokens: 1000, OutputTokens: 500, Cost: 0.42}

	br := cm.Calculate("custom", "some-unlisted-model-xyz", u)

	if !approxEqual(br.TotalUSD, 0.42) {
		t.Errorf("TotalUSD = %v, want 0.42 (API cost survives matrix miss)", br.TotalUSD)
	}
	if br.InputUSD != 0 || br.OutputUSD != 0 || br.CachedUSD != 0 {
		t.Errorf("split should be zero on matrix miss, got in=%v out=%v cached=%v",
			br.InputUSD, br.OutputUSD, br.CachedUSD)
	}
}

// Usage.Cost == 0 means "API reported no cost" → fall back to the matrix,
// preserving the original behaviour exactly.
func TestCostModelCalculate_NoAPICostFallsBackToMatrix(t *testing.T) {
	cm := NewCostModel()
	u := Usage{InputTokens: 1000, OutputTokens: 500, Cost: 0}

	br := cm.Calculate("openai", "gpt-4o", u)

	if !approxEqual(br.TotalUSD, 0.0075) {
		t.Errorf("TotalUSD = %v, want 0.0075 (matrix fallback)", br.TotalUSD)
	}
}

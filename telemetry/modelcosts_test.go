package telemetry

import "testing"

// rates prices each bucket on its own for a call with the given prompt size
// and returns the effective per-1M rates, so a test can pin every column of
// a table row (and its tier) rather than just a total.
func rates(cm *CostModel, provider, model string, prompt int) (in, out, cached, write float64) {
	mtok := float64(prompt) / 1e6
	in = cm.Calculate(provider, model, Usage{InputTokens: prompt}).TotalUSD / mtok
	cached = cm.Calculate(provider, model, Usage{InputTokens: prompt, CachedTokens: prompt}).TotalUSD / mtok
	write = cm.Calculate(provider, model, Usage{InputTokens: prompt, CacheWriteTokens: prompt}).TotalUSD / mtok
	out = cm.Calculate(provider, model, Usage{InputTokens: prompt, OutputTokens: 1_000_000}).TotalUSD - in*mtok
	return in, out, cached, write
}

// Every id this table adds or reprices resolves to its OWN row. The
// family-prefix lookup would otherwise bill claude-opus-5-5 as Opus 5,
// claude-fable-5-1 with Fable 5's 4x dearer cache reads, and the gpt-6 /
// gpt-5.x siblings at whatever shorter key happens to prefix them.
// Rates: official pricing pages read 2026-09-23 (see modelcosts.go).
func TestDefaultCosts_ReleasesResolveToOwnRow(t *testing.T) {
	cm := NewCostModel()

	tests := []struct {
		provider, model             string
		wantIn, wantOut, wantCached float64
		wantWrite                   float64
	}{
		// OpenAI gpt-6 family.
		{"openai", "gpt-6-astra", 10.00, 50.00, 1.00, 12.50},
		{"openai", "gpt-6-sol", 2.00, 10.00, 0.20, 2.50},
		{"openai", "gpt-6-luna", 0.10, 0.50, 0.01, 0.125},
		// gpt-5.x siblings the "gpt-5" key used to swallow.
		{"openai", "gpt-5.6-sol", 4.00, 20.00, 0.40, 5.00},
		{"openai", "gpt-5.4", 2.50, 15.00, 0.25, 2.50},
		{"openai", "gpt-5.4-2026-03-05", 2.50, 15.00, 0.25, 2.50},
		{"openai", "gpt-5.4-mini", 0.75, 4.50, 0.075, 0.75},
		{"openai", "gpt-5.4-nano", 0.20, 1.25, 0.02, 0.20},
		{"openai", "gpt-5.2", 1.75, 14.00, 0.175, 1.75},
		{"openai", "gpt-5.3-codex", 1.75, 14.00, 0.175, 1.75},
		// Pre-5.6 models write to cache at the plain input rate.
		{"openai", "gpt-5.5", 5.00, 30.00, 0.50, 5.00},
		{"openai", "gpt-4o", 2.50, 10.00, 1.25, 2.50},

		// Anthropic Claude 5.x, first-party and platform spellings.
		{"anthropic", "claude-opus-5-5", 4.00, 20.00, 0.20, 5.00},
		{"anthropic", "anthropic.claude-opus-5-5", 4.00, 20.00, 0.20, 5.00},
		{"anthropic", "us.anthropic.claude-opus-5-5", 4.00, 20.00, 0.20, 5.00},
		{"anthropic", "claude-opus-5", 5.00, 25.00, 0.50, 6.25},
		{"anthropic", "claude-fable-5-1", 10.00, 50.00, 0.25, 12.50},
		{"anthropic", "anthropic.claude-fable-5-1", 10.00, 50.00, 0.25, 12.50},
		{"anthropic", "claude-mythos-5-1", 10.00, 50.00, 0.25, 12.50},
		{"anthropic", "claude-fable-5", 10.00, 50.00, 1.00, 12.50},
		{"anthropic", "claude-sonnet-5", 2.00, 10.00, 0.20, 2.50},
		{"anthropic", "claude-haiku-4-5-20251001", 1.00, 5.00, 0.10, 1.25},
		{"anthropic", "claude-haiku-4-5@20251001", 1.00, 5.00, 0.10, 1.25},
		{"anthropic", "claude-3-5-haiku-20241022", 0.80, 4.00, 0.08, 1.00},

		// Gemini Flash releases; no cache-write bucket, so the 1.25x
		// fallback applies to a write the provider never reports.
		{"google", "gemini-3.7-flash", 0.75, 3.75, 0.075, 0.9375},
		{"google", "gemini-3.8-flash", 0.75, 3.75, 0.075, 0.9375},
		{"google", "gemini-3-flash-preview", 0.50, 3.00, 0.05, 0.625},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			in, out, cached, write := rates(cm, tt.provider, tt.model, 100_000)
			for _, c := range []struct {
				name      string
				got, want float64
			}{
				{"input", in, tt.wantIn},
				{"output", out, tt.wantOut},
				{"cached", cached, tt.wantCached},
				{"cache_write", write, tt.wantWrite},
			} {
				if !almostEqual(c.got, c.want, 1e-9) {
					t.Errorf("%s rate = %.6f, want %.6f", c.name, c.got, c.want)
				}
			}
		})
	}
}

// The built-in long-context tiers switch on at the published thresholds —
// OpenAI above 272K prompt tokens, Gemini Pro above 200K — and Claude, with
// no long-context premium, stays flat across its 1M window.
func TestDefaultCosts_LongContextTiers(t *testing.T) {
	cm := NewCostModel()

	tests := []struct {
		provider, model             string
		prompt                      int
		wantIn, wantOut, wantCached float64
		wantWrite                   float64
	}{
		{"openai", "gpt-6-luna", 272_000, 0.10, 0.50, 0.01, 0.125},
		{"openai", "gpt-6-luna", 272_001, 0.20, 0.75, 0.02, 0.25},
		{"openai", "gpt-6-sol", 400_000, 4.00, 15.00, 0.40, 5.00},
		{"openai", "gpt-6-astra", 400_000, 20.00, 75.00, 2.00, 25.00},
		{"openai", "gpt-5.6-sol", 400_000, 8.00, 30.00, 0.80, 10.00},
		{"openai", "gpt-5.4", 400_000, 5.00, 22.50, 0.50, 5.00},
		// No tier published for these: flat at any size.
		{"openai", "gpt-5.4-mini", 400_000, 0.75, 4.50, 0.075, 0.75},
		{"openai", "gpt-5.5-pro", 400_000, 30.00, 180.00, 30.00, 30.00},

		{"google", "gemini-2.5-pro", 200_000, 1.25, 10.00, 0.125, 1.5625},
		{"google", "gemini-2.5-pro", 200_001, 2.50, 15.00, 0.25, 3.125},
		{"google", "gemini-3.1-pro-preview", 300_000, 4.00, 18.00, 0.40, 5.00},
		{"google", "gemini-2.5-flash", 300_000, 0.30, 2.50, 0.03, 0.375},

		{"anthropic", "claude-opus-5-5", 900_000, 4.00, 20.00, 0.20, 5.00},
		{"anthropic", "claude-fable-5-1", 900_000, 10.00, 50.00, 0.25, 12.50},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			in, out, cached, write := rates(cm, tt.provider, tt.model, tt.prompt)
			for _, c := range []struct {
				name      string
				got, want float64
			}{
				{"input", in, tt.wantIn},
				{"output", out, tt.wantOut},
				{"cached", cached, tt.wantCached},
				{"cache_write", write, tt.wantWrite},
			} {
				if !almostEqual(c.got, c.want, 1e-9) {
					t.Errorf("prompt %d: %s rate = %.6f, want %.6f", tt.prompt, c.name, c.got, c.want)
				}
			}
		})
	}
}

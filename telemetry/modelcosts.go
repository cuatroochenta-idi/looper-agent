package telemetry

// defaultCosts returns the base pricing registry with official model costs.
// Prices are in USD per 1 million tokens.
//
// Keys are family-level identifiers (e.g. "claude-opus-4", "gpt-5") wherever
// pricing has remained stable across point releases. CostModel.lookup falls
// back from an exact model id to the longest registered family prefix, so a
// dated id like "claude-opus-4-7-20260301" or "gpt-5-2025-08-07" inherits its
// family price without needing an explicit entry. Override any specific
// version via CostModel.UpdateCost when the provider diverges.
//
// Prices reflect publicly listed rates as of May 2026; treat them as a
// reasonable default rather than billing-grade — call UpdateCost for
// contract pricing or new releases the framework hasn't been updated for.
func defaultCosts() map[string]map[string]CostConfig {
	return map[string]map[string]CostConfig{
		"openai": {
			// gpt-5.6 family — released July 2026 (Sol / Terra / Luna tiers).
			// Cache writes bill at 1.25× input, which is exactly the
			// Calculate fallback, so no explicit cache_write rate is needed.
			"gpt-5.6-sol": {
				InputCostPer1MTokens:  5.00,
				OutputCostPer1MTokens: 30.00,
				CachedCostPer1MTokens: 0.50,
			},
			"gpt-5.6-terra": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 12.00,
				CachedCostPer1MTokens: 0.20,
			},
			"gpt-5.6-luna": {
				InputCostPer1MTokens:  0.20,
				OutputCostPer1MTokens: 1.20,
				CachedCostPer1MTokens: 0.02,
			},
			// gpt-5.5 needs its own entry: "gpt-5" is a family prefix of
			// "gpt-5.5" (the next char is `.`), so without this it would
			// silently inherit gpt-5's much cheaper rate.
			"gpt-5.5": {
				InputCostPer1MTokens:  5.00,
				OutputCostPer1MTokens: 30.00,
				CachedCostPer1MTokens: 0.50,
			},
			// gpt-5 family — released August 2025.
			"gpt-5": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.125, // 90% discount on cache hits
			},
			"gpt-5-mini": {
				InputCostPer1MTokens:  0.25,
				OutputCostPer1MTokens: 2.00,
				CachedCostPer1MTokens: 0.025,
			},
			"gpt-5-nano": {
				InputCostPer1MTokens:  0.05,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.005,
			},
			// gpt-4.1 family.
			"gpt-4.1": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 8.00,
				CachedCostPer1MTokens: 0.50,
			},
			"gpt-4.1-mini": {
				InputCostPer1MTokens:  0.40,
				OutputCostPer1MTokens: 1.60,
				CachedCostPer1MTokens: 0.10,
			},
			"gpt-4.1-nano": {
				InputCostPer1MTokens:  0.10,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.025,
			},
			// gpt-4o family.
			"gpt-4o": {
				InputCostPer1MTokens:  2.50,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 1.25, // 50% discount
			},
			"gpt-4o-mini": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.075,
			},
			// o-series reasoning models. o-prefixed ids share `applyMaxTokens`
			// routing in provider/openai; the family-prefix lookup mirrors that.
			//
			// o1 / o1-mini / o3-mini are no longer listed on OpenAI's public
			// pricing page — those rates are UNVERIFIED historical values,
			// kept so existing callers keep a non-zero estimate. Override
			// with UpdateCost if you still route to them.
			"o1": {
				InputCostPer1MTokens:  15.00,
				OutputCostPer1MTokens: 60.00,
				CachedCostPer1MTokens: 7.50,
			},
			"o1-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
			"o3": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 8.00,
				CachedCostPer1MTokens: 0.50,
			},
			"o3-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
			"o4-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.275,
			},
		},
		"anthropic": {
			// Claude 5 family. Opus 5 holds Opus 4.8's price point; Fable 5
			// (and its Project Glasswing twin Mythos 5) sit above it. Sonnet 5
			// lists at 3.00/15.00 — an introductory 2.00/10.00 ran through
			// 2026-08-31; register that via UpdateCost if you billed under it.
			"claude-fable-5": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     1.00,  // 90% discount
				CacheWriteCostPer1MTokens: 12.50, // 1.25x input
			},
			"claude-mythos-5": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     1.00,
				CacheWriteCostPer1MTokens: 12.50,
			},
			"claude-opus-5": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25, // 1.25x input
			},
			"claude-sonnet-5": {
				InputCostPer1MTokens:      3.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.30,
				CacheWriteCostPer1MTokens: 3.75, // 1.25x input
			},
			// Claude 4 family. Opus pricing is NOT flat across the line: 4.0
			// and 4.1 bill at 15.00/75.00, while 4.5 onward dropped to
			// 5.00/25.00. Each of those minors needs its own entry — the
			// longest-prefix lookup would otherwise hand them the 4.0 price
			// via the bare "claude-opus-4" key and overcharge by 3x. Sonnet
			// and haiku DO share family-level pricing across their minors,
			// so a single key each is correct there.
			"claude-opus-4-8": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25,
			},
			"claude-opus-4-7": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25,
			},
			"claude-opus-4-6": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25,
			},
			"claude-opus-4-5": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25,
			},
			// Opus 4.0 / 4.1 — the pre-4.5 price point. Dated ids like
			// "claude-opus-4-1-20250805" inherit via prefix lookup.
			"claude-opus-4": {
				InputCostPer1MTokens:      15.00,
				OutputCostPer1MTokens:     75.00,
				CachedCostPer1MTokens:     1.50,  // 90% discount
				CacheWriteCostPer1MTokens: 18.75, // 1.25x input
			},
			"claude-sonnet-4": {
				InputCostPer1MTokens:      3.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.30,
				CacheWriteCostPer1MTokens: 3.75, // 1.25x input
			},
			"claude-haiku-4": {
				InputCostPer1MTokens:      1.00,
				OutputCostPer1MTokens:     5.00,
				CachedCostPer1MTokens:     0.10,
				CacheWriteCostPer1MTokens: 1.25, // 1.25x input
			},
			// Legacy 3.x.
			"claude-3.5-sonnet": {
				InputCostPer1MTokens:      3.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.30,
				CacheWriteCostPer1MTokens: 3.75, // 1.25x input
			},
			"claude-3.5-haiku": {
				InputCostPer1MTokens:      0.80,
				OutputCostPer1MTokens:     4.00,
				CachedCostPer1MTokens:     0.08,
				CacheWriteCostPer1MTokens: 1.00, // 1.25x input
			},
			"claude-3-opus": {
				InputCostPer1MTokens:      15.00,
				OutputCostPer1MTokens:     75.00,
				CachedCostPer1MTokens:     1.50,
				CacheWriteCostPer1MTokens: 18.75, // 1.25x input
			},
		},
		"google": {
			// Gemini's id shape is "gemini-{ver}-{tier}", so the version sits
			// between the brand and the tier. Family-prefix lookup can't bridge
			// across that — list each (major.minor, tier) pair explicitly. New
			// minor releases that keep the same price reuse the same numbers.
			// A "-lite" tier is a longer prefix of its base tier
			// ("gemini-2.5-flash" is a family prefix of
			// "gemini-2.5-flash-lite"), so every lite tier needs its own
			// entry or it silently inherits the pricier base rate.
			//
			// Tiered models (pro tiers bill more above a 200k-token prompt)
			// are registered at their ≤200k rate; register the long-context
			// rate via UpdateCost if you routinely exceed it.
			"gemini-2.5-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.125, // 90% discount
			},
			"gemini-2.5-flash": {
				InputCostPer1MTokens:  0.30,
				OutputCostPer1MTokens: 2.50,
				CachedCostPer1MTokens: 0.03,
			},
			"gemini-2.5-flash-lite": {
				InputCostPer1MTokens:  0.10,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.01,
			},
			// gemini-3-pro / gemini-3-flash are no longer listed on Google's
			// public pricing page, so these rates are UNVERIFIED carry-overs
			// from the 2.5 tiers. Left in place so existing callers keep a
			// non-zero estimate; override with UpdateCost if you still route
			// to them and know the contracted rate.
			"gemini-3-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125,
			},
			"gemini-3-flash": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.0375,
			},
			"gemini-3.1-pro": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 12.00,
				CachedCostPer1MTokens: 0.20,
			},
			"gemini-3.1-flash-lite": {
				InputCostPer1MTokens:  0.25,
				OutputCostPer1MTokens: 1.50,
				CachedCostPer1MTokens: 0.025,
			},
			// gemini-3.5-flash — released May 2026; agent-tuned Flash priced
			// well above prior Flash tiers.
			"gemini-3.5-flash": {
				InputCostPer1MTokens:  1.50,
				OutputCostPer1MTokens: 9.00,
				CachedCostPer1MTokens: 0.15,
			},
			"gemini-3.5-flash-lite": {
				InputCostPer1MTokens:  0.30,
				OutputCostPer1MTokens: 2.50,
				CachedCostPer1MTokens: 0.03,
			},
			"gemini-3.6-flash": {
				InputCostPer1MTokens:  1.50,
				OutputCostPer1MTokens: 7.50,
				CachedCostPer1MTokens: 0.15,
			},
		},
	}
}

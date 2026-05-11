package telemetry

// defaultCosts returns the base pricing registry with official model costs.
// Prices are in USD per 1 million tokens.
// Source: official pricing pages as of May 2026.
func defaultCosts() map[string]map[string]CostConfig {
	return map[string]map[string]CostConfig{
		"openai": {
			"gpt-4o": {
				InputCostPer1MTokens:  2.50,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 1.25, // 50% discount
			},
			"gpt-4o-mini": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.075, // 50% discount
			},
			"gpt-4.1": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 8.00,
				CachedCostPer1MTokens: 1.00,
			},
			"gpt-4.1-mini": {
				InputCostPer1MTokens:  0.40,
				OutputCostPer1MTokens: 1.60,
				CachedCostPer1MTokens: 0.20,
			},
			"gpt-4.1-nano": {
				InputCostPer1MTokens:  0.10,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.05,
			},
			"o4-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
		},
		"anthropic": {
			"claude-sonnet-4-20250514": {
				InputCostPer1MTokens:  3.00,
				OutputCostPer1MTokens: 15.00,
				CachedCostPer1MTokens: 0.30, // 90% discount
			},
			"claude-opus-4-20250514": {
				InputCostPer1MTokens:  15.00,
				OutputCostPer1MTokens: 75.00,
				CachedCostPer1MTokens: 1.50, // 90% discount
			},
			"claude-3.5-haiku": {
				InputCostPer1MTokens:  0.80,
				OutputCostPer1MTokens: 4.00,
				CachedCostPer1MTokens: 0.08, // 90% discount
			},
		},
		"google": {
			"gemini-2.5-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125, // 75% discount
			},
			"gemini-2.5-flash": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.0375, // 75% discount
			},
		},
	}
}

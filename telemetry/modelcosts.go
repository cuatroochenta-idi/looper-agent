package telemetry

// Long-context thresholds, in prompt tokens (Usage.InputTokens, cached reads
// and writes included). A call whose prompt is strictly larger bills at the
// model's tier rates for the whole call.
const (
	// OpenAI model pages: "Prompts with more than 272K input tokens are
	// priced at 2x input and cache rates and 1.5x output for the full
	// request."
	openAILongContextAbove = 272_000

	// Gemini pricing page: "$1.25, prompts <= 200k tokens / $2.50, prompts
	// > 200k tokens".
	geminiLongContextAbove = 200_000
)

// defaultCosts returns the base pricing registry with official model costs.
// Prices are in USD per 1 million tokens, standard (non-batch) processing.
//
// Keys are family-level identifiers (e.g. "claude-opus-4", "gpt-5") wherever
// pricing has remained stable across point releases. CostModel.lookup falls
// back from an exact model id to the longest registered family prefix, so a
// dated id like "claude-opus-4-7-20260301" or "gpt-5-2025-08-07" inherits its
// family price without needing an explicit entry. Every sibling whose price
// differs from its family key's — a newer minor, a "-pro", "-mini" or "-lite"
// variant — needs its own entry, or the prefix lookup silently bills it at
// the family rate.
//
// Rates were re-read from each provider's official pricing pages on
// 2026-09-23; the URLs sit above each provider's block. Treat them as a
// reasonable default rather than billing-grade. Not modelled: batch / flex
// (50% off), OpenAI fast mode (2x), Anthropic fast mode, regional or
// data-residency uplifts (+10%), Gemini audio-input rates and context-cache
// storage. Call UpdateCost for those, for contract pricing, or for releases
// the table doesn't know yet.
func defaultCosts() map[string]map[string]CostConfig {
	return map[string]map[string]CostConfig{
		// Sources, read 2026-09-23:
		//   https://developers.openai.com/api/docs/pricing
		//   https://developers.openai.com/api/docs/models/<model id>
		//   https://developers.openai.com/api/docs/guides/prompt-caching
		//
		// Cache writes: GPT-5.6 and later bill them at 1.25x input; earlier
		// models have "no additional cache-write charge", so their
		// cache_write rate is the plain input rate (the Calculate fallback
		// would otherwise charge 1.25x). Models listed without a cached-input
		// price get no cache discount (cached = input).
		"openai": {
			// gpt-6 family. Long context: 2x input and cache rates, 1.5x
			// output — the page lists each long-context rate.
			"gpt-6-astra": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     1.00,
				CacheWriteCostPer1MTokens: 12.50,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      20.00,
					OutputCostPer1MTokens:     75.00,
					CachedCostPer1MTokens:     2.00,
					CacheWriteCostPer1MTokens: 25.00,
				}},
			},
			"gpt-6-sol": {
				InputCostPer1MTokens:      2.00,
				OutputCostPer1MTokens:     10.00,
				CachedCostPer1MTokens:     0.20,
				CacheWriteCostPer1MTokens: 2.50,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      4.00,
					OutputCostPer1MTokens:     15.00,
					CachedCostPer1MTokens:     0.40,
					CacheWriteCostPer1MTokens: 5.00,
				}},
			},
			"gpt-6-luna": {
				InputCostPer1MTokens:      0.10,
				OutputCostPer1MTokens:     0.50,
				CachedCostPer1MTokens:     0.01,
				CacheWriteCostPer1MTokens: 0.125,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      0.20,
					OutputCostPer1MTokens:     0.75,
					CachedCostPer1MTokens:     0.02,
					CacheWriteCostPer1MTokens: 0.25,
				}},
			},
			// gpt-5.6 family. Sol is on promotional pricing "at least through
			// November 21, 2026" (launched at 5.00 / 30.00 / 0.50). The model
			// pages state "2x input and 1.5x output" above 272K; the pricing
			// page's Cyber table lists Sol's long-context row with cached and
			// cache-write rates doubled too, and Terra / Luna follow the same
			// rule.
			"gpt-5.6-sol": {
				InputCostPer1MTokens:      4.00,
				OutputCostPer1MTokens:     20.00,
				CachedCostPer1MTokens:     0.40,
				CacheWriteCostPer1MTokens: 5.00,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      8.00,
					OutputCostPer1MTokens:     30.00,
					CachedCostPer1MTokens:     0.80,
					CacheWriteCostPer1MTokens: 10.00,
				}},
			},
			"gpt-5.6-terra": {
				InputCostPer1MTokens:      2.00,
				OutputCostPer1MTokens:     12.00,
				CachedCostPer1MTokens:     0.20,
				CacheWriteCostPer1MTokens: 2.50,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      4.00,
					OutputCostPer1MTokens:     18.00,
					CachedCostPer1MTokens:     0.40,
					CacheWriteCostPer1MTokens: 5.00,
				}},
			},
			"gpt-5.6-luna": {
				InputCostPer1MTokens:      0.20,
				OutputCostPer1MTokens:     1.20,
				CachedCostPer1MTokens:     0.02,
				CacheWriteCostPer1MTokens: 0.25,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      0.40,
					OutputCostPer1MTokens:     1.80,
					CachedCostPer1MTokens:     0.04,
					CacheWriteCostPer1MTokens: 0.50,
				}},
			},
			// gpt-5.5 / gpt-5.4: "prompts with >272K input tokens are priced
			// at 2x input and 1.5x output". The cached long-context rate
			// applies the same 2x, as every long-context row OpenAI does
			// publish (gpt-6, gpt-5.6-sol) doubles it.
			"gpt-5.5": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     30.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 5.00,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      10.00,
					OutputCostPer1MTokens:     45.00,
					CachedCostPer1MTokens:     1.00,
					CacheWriteCostPer1MTokens: 10.00,
				}},
			},
			// The pricing page labels gpt-5.5-pro "(<272K context length)"
			// but publishes no long-context rate for it, so it stays flat.
			"gpt-5.5-pro": {
				InputCostPer1MTokens:      30.00,
				OutputCostPer1MTokens:     180.00,
				CachedCostPer1MTokens:     30.00,
				CacheWriteCostPer1MTokens: 30.00,
			},
			"gpt-5.4": {
				InputCostPer1MTokens:      2.50,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.25,
				CacheWriteCostPer1MTokens: 2.50,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      5.00,
					OutputCostPer1MTokens:     22.50,
					CachedCostPer1MTokens:     0.50,
					CacheWriteCostPer1MTokens: 5.00,
				}},
			},
			"gpt-5.4-pro": {
				InputCostPer1MTokens:      30.00,
				OutputCostPer1MTokens:     180.00,
				CachedCostPer1MTokens:     30.00,
				CacheWriteCostPer1MTokens: 30.00,
				Tiers: []PriceTier{{
					AboveInputTokens:          openAILongContextAbove,
					InputCostPer1MTokens:      60.00,
					OutputCostPer1MTokens:     270.00,
					CachedCostPer1MTokens:     60.00,
					CacheWriteCostPer1MTokens: 60.00,
				}},
			},
			"gpt-5.4-mini": {
				InputCostPer1MTokens:      0.75,
				OutputCostPer1MTokens:     4.50,
				CachedCostPer1MTokens:     0.075,
				CacheWriteCostPer1MTokens: 0.75,
			},
			"gpt-5.4-nano": {
				InputCostPer1MTokens:      0.20,
				OutputCostPer1MTokens:     1.25,
				CachedCostPer1MTokens:     0.02,
				CacheWriteCostPer1MTokens: 0.20,
			},
			"gpt-5.3-codex": {
				InputCostPer1MTokens:      1.75,
				OutputCostPer1MTokens:     14.00,
				CachedCostPer1MTokens:     0.175,
				CacheWriteCostPer1MTokens: 1.75,
			},
			"gpt-5.2": {
				InputCostPer1MTokens:      1.75,
				OutputCostPer1MTokens:     14.00,
				CachedCostPer1MTokens:     0.175,
				CacheWriteCostPer1MTokens: 1.75,
			},
			"gpt-5.2-pro": {
				InputCostPer1MTokens:      21.00,
				OutputCostPer1MTokens:     168.00,
				CachedCostPer1MTokens:     21.00,
				CacheWriteCostPer1MTokens: 21.00,
			},
			// gpt-5 family. gpt-5.1 lists the same rates and inherits them
			// through the prefix lookup.
			"gpt-5": {
				InputCostPer1MTokens:      1.25,
				OutputCostPer1MTokens:     10.00,
				CachedCostPer1MTokens:     0.125,
				CacheWriteCostPer1MTokens: 1.25,
			},
			"gpt-5-pro": {
				InputCostPer1MTokens:      15.00,
				OutputCostPer1MTokens:     120.00,
				CachedCostPer1MTokens:     15.00,
				CacheWriteCostPer1MTokens: 15.00,
			},
			"gpt-5-mini": {
				InputCostPer1MTokens:      0.25,
				OutputCostPer1MTokens:     2.00,
				CachedCostPer1MTokens:     0.025,
				CacheWriteCostPer1MTokens: 0.25,
			},
			"gpt-5-nano": {
				InputCostPer1MTokens:      0.05,
				OutputCostPer1MTokens:     0.40,
				CachedCostPer1MTokens:     0.005,
				CacheWriteCostPer1MTokens: 0.05,
			},
			// gpt-4.1 family.
			"gpt-4.1": {
				InputCostPer1MTokens:      2.00,
				OutputCostPer1MTokens:     8.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 2.00,
			},
			"gpt-4.1-mini": {
				InputCostPer1MTokens:      0.40,
				OutputCostPer1MTokens:     1.60,
				CachedCostPer1MTokens:     0.10,
				CacheWriteCostPer1MTokens: 0.40,
			},
			"gpt-4.1-nano": {
				InputCostPer1MTokens:      0.10,
				OutputCostPer1MTokens:     0.40,
				CachedCostPer1MTokens:     0.025,
				CacheWriteCostPer1MTokens: 0.10,
			},
			// gpt-4o family. The first gpt-4o snapshot kept its launch price.
			"gpt-4o": {
				InputCostPer1MTokens:      2.50,
				OutputCostPer1MTokens:     10.00,
				CachedCostPer1MTokens:     1.25,
				CacheWriteCostPer1MTokens: 2.50,
			},
			"gpt-4o-2024-05-13": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     5.00,
				CacheWriteCostPer1MTokens: 5.00,
			},
			"gpt-4o-mini": {
				InputCostPer1MTokens:      0.15,
				OutputCostPer1MTokens:     0.60,
				CachedCostPer1MTokens:     0.075,
				CacheWriteCostPer1MTokens: 0.15,
			},
			// o-series reasoning models. o-prefixed ids share `applyMaxTokens`
			// routing in provider/openai; the family-prefix lookup mirrors that.
			//
			// o1-mini is no longer listed on OpenAI's public pricing page — its
			// rate is an UNVERIFIED historical value, kept so existing callers
			// keep a non-zero estimate. Override with UpdateCost if you still
			// route to it.
			"o1": {
				InputCostPer1MTokens:      15.00,
				OutputCostPer1MTokens:     60.00,
				CachedCostPer1MTokens:     7.50,
				CacheWriteCostPer1MTokens: 15.00,
			},
			"o1-pro": {
				InputCostPer1MTokens:      150.00,
				OutputCostPer1MTokens:     600.00,
				CachedCostPer1MTokens:     150.00,
				CacheWriteCostPer1MTokens: 150.00,
			},
			"o1-mini": {
				InputCostPer1MTokens:      1.10,
				OutputCostPer1MTokens:     4.40,
				CachedCostPer1MTokens:     0.55,
				CacheWriteCostPer1MTokens: 1.10,
			},
			"o3": {
				InputCostPer1MTokens:      2.00,
				OutputCostPer1MTokens:     8.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 2.00,
			},
			"o3-pro": {
				InputCostPer1MTokens:      20.00,
				OutputCostPer1MTokens:     80.00,
				CachedCostPer1MTokens:     20.00,
				CacheWriteCostPer1MTokens: 20.00,
			},
			"o3-mini": {
				InputCostPer1MTokens:      1.10,
				OutputCostPer1MTokens:     4.40,
				CachedCostPer1MTokens:     0.55,
				CacheWriteCostPer1MTokens: 1.10,
			},
			"o4-mini": {
				InputCostPer1MTokens:      1.10,
				OutputCostPer1MTokens:     4.40,
				CachedCostPer1MTokens:     0.275,
				CacheWriteCostPer1MTokens: 1.10,
			},
		},
		// Sources, read 2026-09-23:
		//   https://platform.claude.com/docs/en/about-claude/pricing
		//   https://platform.claude.com/docs/en/models/overview
		//
		// No long-context tiers: "Claude 4.6 and later models ... include the
		// full 1M token context window at standard pricing", and every older
		// model tops out at 200K. Cache writes are the 5-minute-TTL rate (see
		// CostConfig.CacheWriteCostPer1MTokens).
		"anthropic": {
			// Claude 5 family. The .1 / .5 releases changed the cache-read
			// multiplier (0.025x on Fable / Mythos 5.1, 0.05x on Opus 5.5) and
			// Opus 5.5's base price, so each needs its own key: the family
			// prefix would otherwise hand "claude-fable-5-1" Fable 5's 4x
			// dearer cache reads and "claude-opus-5-5" Opus 5's rates.
			"claude-fable-5-1": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     0.25,
				CacheWriteCostPer1MTokens: 12.50,
			},
			"claude-mythos-5-1": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     0.25,
				CacheWriteCostPer1MTokens: 12.50,
			},
			"claude-fable-5": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     1.00,
				CacheWriteCostPer1MTokens: 12.50,
			},
			"claude-mythos-5": {
				InputCostPer1MTokens:      10.00,
				OutputCostPer1MTokens:     50.00,
				CachedCostPer1MTokens:     1.00,
				CacheWriteCostPer1MTokens: 12.50,
			},
			"claude-opus-5-5": {
				InputCostPer1MTokens:      4.00,
				OutputCostPer1MTokens:     20.00,
				CachedCostPer1MTokens:     0.20,
				CacheWriteCostPer1MTokens: 5.00,
			},
			"claude-opus-5": {
				InputCostPer1MTokens:      5.00,
				OutputCostPer1MTokens:     25.00,
				CachedCostPer1MTokens:     0.50,
				CacheWriteCostPer1MTokens: 6.25,
			},
			// Sonnet 5's launch price of 2.00 / 10.00 "is now the standard
			// price"; the scheduled rise to 3.00 / 15.00 did not happen.
			"claude-sonnet-5": {
				InputCostPer1MTokens:      2.00,
				OutputCostPer1MTokens:     10.00,
				CachedCostPer1MTokens:     0.20,
				CacheWriteCostPer1MTokens: 2.50,
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
				CachedCostPer1MTokens:     1.50,
				CacheWriteCostPer1MTokens: 18.75,
			},
			"claude-sonnet-4": {
				InputCostPer1MTokens:      3.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.30,
				CacheWriteCostPer1MTokens: 3.75,
			},
			"claude-haiku-4": {
				InputCostPer1MTokens:      1.00,
				OutputCostPer1MTokens:     5.00,
				CachedCostPer1MTokens:     0.10,
				CacheWriteCostPer1MTokens: 1.25,
			},
			// Legacy 3.x. Haiku 3.5 is still listed (retired except on
			// Bedrock and Google Cloud); its API id is "claude-3-5-haiku-…",
			// which the dotted keys below never match. Sonnet 3.5 and Opus 3
			// are no longer listed — UNVERIFIED historical values, kept for
			// callers who registered usage under these keys.
			"claude-3-5-haiku": {
				InputCostPer1MTokens:      0.80,
				OutputCostPer1MTokens:     4.00,
				CachedCostPer1MTokens:     0.08,
				CacheWriteCostPer1MTokens: 1.00,
			},
			"claude-3.5-sonnet": {
				InputCostPer1MTokens:      3.00,
				OutputCostPer1MTokens:     15.00,
				CachedCostPer1MTokens:     0.30,
				CacheWriteCostPer1MTokens: 3.75,
			},
			"claude-3.5-haiku": {
				InputCostPer1MTokens:      0.80,
				OutputCostPer1MTokens:     4.00,
				CachedCostPer1MTokens:     0.08,
				CacheWriteCostPer1MTokens: 1.00,
			},
			"claude-3-opus": {
				InputCostPer1MTokens:      15.00,
				OutputCostPer1MTokens:     75.00,
				CachedCostPer1MTokens:     1.50,
				CacheWriteCostPer1MTokens: 18.75,
			},
		},
		// Source, read 2026-09-23 (paid tier, Standard, text / image / video
		// input): https://ai.google.dev/gemini-api/docs/pricing
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
			// The Pro models bill the whole call at a higher rate once the
			// prompt passes 200K tokens.
			"gemini-2.5-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.125,
				Tiers: []PriceTier{{
					AboveInputTokens:      geminiLongContextAbove,
					InputCostPer1MTokens:  2.50,
					OutputCostPer1MTokens: 15.00,
					CachedCostPer1MTokens: 0.25,
				}},
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
			// gemini-3-pro is no longer listed on Google's public pricing
			// page, so this rate is an UNVERIFIED carry-over from the 2.5 Pro
			// tier. Left in place so existing callers keep a non-zero
			// estimate; override with UpdateCost if you still route to it.
			"gemini-3-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125,
			},
			// Listed as gemini-3-flash-preview.
			"gemini-3-flash": {
				InputCostPer1MTokens:  0.50,
				OutputCostPer1MTokens: 3.00,
				CachedCostPer1MTokens: 0.05,
			},
			// Listed as gemini-3.1-pro-preview.
			"gemini-3.1-pro": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 12.00,
				CachedCostPer1MTokens: 0.20,
				Tiers: []PriceTier{{
					AboveInputTokens:      geminiLongContextAbove,
					InputCostPer1MTokens:  4.00,
					OutputCostPer1MTokens: 18.00,
					CachedCostPer1MTokens: 0.40,
				}},
			},
			"gemini-3.1-flash-lite": {
				InputCostPer1MTokens:  0.25,
				OutputCostPer1MTokens: 1.50,
				CachedCostPer1MTokens: 0.025,
			},
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
			// gemini-3.6 / 3.7 / 3.8 Flash bill 0.75 / 3.75 / 0.075 "through
			// December 31, 2026" and 1.50 / 7.50 / 0.15 "starting January 1,
			// 2027". The table carries the rate in force when it was read;
			// bump these to the 2027 rates in the first release of 2027.
			"gemini-3.6-flash": {
				InputCostPer1MTokens:  0.75,
				OutputCostPer1MTokens: 3.75,
				CachedCostPer1MTokens: 0.075,
			},
			"gemini-3.7-flash": {
				InputCostPer1MTokens:  0.75,
				OutputCostPer1MTokens: 3.75,
				CachedCostPer1MTokens: 0.075,
			},
			"gemini-3.8-flash": {
				InputCostPer1MTokens:  0.75,
				OutputCostPer1MTokens: 3.75,
				CachedCostPer1MTokens: 0.075,
			},
		},
	}
}

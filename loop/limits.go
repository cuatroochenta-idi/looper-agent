package loop

import "github.com/cuatroochenta-idi/looper-agent/provider"

// UsageLimits caps a single run. The framework checks the running totals
// after every LLM call; the FIRST limit hit aborts the loop, the run's
// Status is set to "usage_exceeded", and the last attempted output is
// surfaced so observers can see what the model produced before the cap.
//
// All fields are zero-or-positive; zero means "unlimited" so an empty
// UsageLimits{} disables every cap (current legacy behavior).
type UsageLimits struct {
	// MaxRequests caps the number of LLM calls. Tool calls don't count;
	// only requests sent to the provider.
	MaxRequests int

	// MaxTotalTokens caps the sum of input + output tokens across the
	// run. Cached tokens count once as input (matches Anthropic /
	// OpenAI's billing semantics).
	MaxTotalTokens int

	// MaxUSD caps the accumulated cost in dollars. Requires a CostModel
	// configured on the loop — otherwise the field is ignored.
	MaxUSD float64
}

// WithLoopUsageLimits attaches the budget to the loop. The zero value
// disables every cap.
func WithLoopUsageLimits(u UsageLimits) LoopOption {
	return func(l *AgentLoop) { l.usageLimits = u }
}

// exceeds returns the first cap (if any) that the supplied totals
// already exceed. The returned reason is a short human-readable label
// suitable for the Status field / telemetry.
func (u UsageLimits) exceeds(requests int, totalTokens int, usd float64) (bool, string) {
	if u.MaxRequests > 0 && requests >= u.MaxRequests {
		return true, "max_requests"
	}
	if u.MaxTotalTokens > 0 && totalTokens >= u.MaxTotalTokens {
		return true, "max_total_tokens"
	}
	if u.MaxUSD > 0 && usd >= u.MaxUSD {
		return true, "max_usd"
	}
	return false, ""
}

// tripUsageLimitIfExceeded is the streaming-path counterpart to the
// non-streaming Run loop's inline check. It evaluates the loop's
// UsageLimits against current per-Iterator totals and, when a cap is
// crossed, records the final output, emits a StepFinalResponse with the
// usage_exceeded status, and reports true so the caller can return.
func (it *Iterator) tripUsageLimitIfExceeded(final string, turn int, usage *provider.Usage) bool {
	it.resMu.RLock()
	tokens := it.inputTokens + it.outputTokens
	it.resMu.RUnlock()
	br := it.loop.calculateCost(provider.Usage{Cost: it.apiCost}, it.inputTokens, it.outputTokens, it.cachedTokens)
	if exceeded, _ := it.loop.usageLimits.exceeds(turn+1, tokens, br.TotalUSD); exceeded {
		it.recordFinal(final, turn, "usage_exceeded")
		it.steps <- Step{Type: StepFinalResponse, Content: final, Turn: turn, Usage: usage}
		return true
	}
	return false
}

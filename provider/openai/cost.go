// Cost extraction for the OpenAI-compatible provider.
//
// OpenAI-native responses don't carry a price, but OpenRouter (and some other
// gateways) include a top-level `cost` field inside the `usage` object giving
// the actual USD charged for the call. The openai-go SDK schema doesn't expose
// it because it's not OpenAI-native, so we pull it out of the usage RawJSON —
// the same approach reasoning.go uses for `reasoning_content`.
package openai

import "encoding/json"

// extractCostField looks for a numeric top-level `cost` field in the raw usage
// JSON and returns it as a float64 (USD). Returns 0 on any miss or wrong type
// — never fails.
func extractCostField(raw string) float64 {
	if raw == "" {
		return 0
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return 0
	}
	v, ok := m["cost"]
	if !ok {
		return 0
	}
	var cost float64
	if err := json.Unmarshal(v, &cost); err != nil {
		return 0
	}
	return cost
}

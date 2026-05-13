package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUsesCompletionTokensParam pins the routing table that decides
// whether a model takes max_tokens (legacy) or max_completion_tokens
// (o-series reasoning models + gpt-5 family). Matching is prefix-based
// and case-insensitive so versioned ids (gpt-5.4-mini,
// o1-preview-2024-09-12) all fall on the right side.
func TestUsesCompletionTokensParam(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// New token-param family.
		{"o1-preview", true},
		{"o1-mini-2024-09-12", true},
		{"O3", true},
		{"o4-mini", true},
		{"gpt-5", true},
		{"gpt-5.4-mini", true},
		{"GPT-5-NANO", true},

		// Legacy max_tokens family.
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4.1", false},
		{"gpt-3.5-turbo", false},
		{"chatgpt-4o-latest", false},
		{"", false},
	}
	for _, c := range cases {
		if got := usesCompletionTokensParam(c.model); got != c.want {
			t.Errorf("usesCompletionTokensParam(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// TestApplyMaxTokens_RoutesByModel asserts the wire-shape contract: the
// helper writes to MaxCompletionTokens for gpt-5 / o-series and to
// MaxTokens otherwise, and the resulting JSON carries the correct field
// name.
func TestApplyMaxTokens_RoutesByModel(t *testing.T) {
	for _, c := range []struct {
		model     string
		wantField string
	}{
		{"gpt-4o-mini", `"max_tokens":120`},
		{"gpt-5.4-mini", `"max_completion_tokens":120`},
		{"o1-mini", `"max_completion_tokens":120`},
		{"o4-mini", `"max_completion_tokens":120`},
	} {
		params := newParamsForModel(c.model)
		applyMaxTokens(&params, c.model, 120)
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), c.wantField) {
			t.Errorf("model %q: missing %s in payload\n%s", c.model, c.wantField, raw)
		}
		// And the OTHER field must NOT show up, otherwise OpenAI rejects.
		other := `"max_tokens":`
		if strings.Contains(c.wantField, `max_completion_tokens`) {
			if strings.Contains(string(raw), other) && !strings.Contains(string(raw), `"max_tokens":120`) == false {
				t.Errorf("model %q: legacy max_tokens leaked into payload\n%s", c.model, raw)
			}
		}
	}
}

// TestApplyMaxTokens_ZeroIsNoOp asserts that the legacy "n=0 means
// don't constrain" semantic is preserved.
func TestApplyMaxTokens_ZeroIsNoOp(t *testing.T) {
	for _, model := range []string{"gpt-4o-mini", "gpt-5.4-mini"} {
		params := newParamsForModel(model)
		applyMaxTokens(&params, model, 0)
		raw, _ := json.Marshal(params)
		if strings.Contains(string(raw), `"max_tokens"`) ||
			strings.Contains(string(raw), `"max_completion_tokens"`) {
			t.Errorf("model %q: n=0 should not write any token cap, got %s", model, raw)
		}
	}
}

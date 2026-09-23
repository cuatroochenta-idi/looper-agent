package anthropic

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func TestLookupCaps(t *testing.T) {
	tests := []struct {
		model        string
		wantThinking thinkingMode
		wantSampling bool
		wantEffort   bool
	}{
		// Thinking always on, no sampling.
		{"claude-fable-5-1", thinkingAlwaysOn, false, true},
		{"claude-mythos-5-1", thinkingAlwaysOn, false, true},
		{"claude-fable-5", thinkingAlwaysOn, false, true},
		{"claude-mythos-5", thinkingAlwaysOn, false, true},
		{"claude-mythos-preview", thinkingAlwaysOn, false, true},

		// Adaptive-only, no sampling. Opus 5.5 always thinks but accepts
		// {"type":"adaptive"}, which the adaptive path is the only one to send.
		{"claude-opus-5-5", thinkingAdaptive, false, true},
		{"claude-opus-5", thinkingAdaptive, false, true},
		{"claude-sonnet-5", thinkingAdaptive, false, true},
		{"claude-opus-4-8", thinkingAdaptive, false, true},
		{"claude-opus-4-7", thinkingAdaptive, false, true},
		{"claude-opus-4-7-20260301", thinkingAdaptive, false, true},

		// 4.6 still takes sampling params.
		{"claude-opus-4-6", thinkingAdaptive, true, true},
		{"claude-sonnet-4-6", thinkingAdaptive, true, true},

		// Legacy budget_tokens models.
		{"claude-opus-4-5", thinkingLegacyBudget, true, true},
		{"claude-opus-4-5-20251101", thinkingLegacyBudget, true, true},
		{"claude-opus-4-1-20250805", thinkingLegacyBudget, true, false},
		{"claude-sonnet-4-5-20250929", thinkingLegacyBudget, true, false},
		{"claude-haiku-4-5", thinkingLegacyBudget, true, false},
		{"claude-3-opus-20240229", thinkingLegacyBudget, true, false},

		// Bedrock and Vertex id shapes normalise to the same caps.
		{"anthropic.claude-opus-5", thinkingAdaptive, false, true},
		{"anthropic.claude-opus-5-5", thinkingAdaptive, false, true},
		{"us.anthropic.claude-fable-5-1", thinkingAlwaysOn, false, true},
		{"claude-opus-4-5@20251101", thinkingLegacyBudget, true, true},

		// Unknown ids follow the current contract, not the legacy one.
		{"claude-something-9", thinkingAdaptive, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := lookupCaps(tt.model)
			if got.thinking != tt.wantThinking {
				t.Errorf("thinking = %v, want %v", got.thinking, tt.wantThinking)
			}
			if got.sampling != tt.wantSampling {
				t.Errorf("sampling = %v, want %v", got.sampling, tt.wantSampling)
			}
			if got.effort != tt.wantEffort {
				t.Errorf("effort = %v, want %v", got.effort, tt.wantEffort)
			}
		})
	}
}

// TestFamilyPrefixBoundary guards the same shadowing hazard the cost
// registry has: a shorter family key must not swallow a longer sibling.
func TestFamilyPrefixBoundary(t *testing.T) {
	if familyPrefix("claude-opus-45", "claude-opus-4") {
		t.Error("claude-opus-4 must not match claude-opus-45 (no separator)")
	}
	if !familyPrefix("claude-opus-4-1", "claude-opus-4") {
		t.Error("claude-opus-4 must match claude-opus-4-1")
	}
	// Longest-prefix wins: 4.7 is adaptive even though "claude-opus-4"
	// (legacy) is also a prefix of it.
	if lookupCaps("claude-opus-4-7").thinking != thinkingAdaptive {
		t.Error("claude-opus-4-7 must resolve to its own entry, not claude-opus-4")
	}
}

// TestApplyModelParamsSamplingStripped is the regression guard for the bug
// that made the provider unusable on current models: the translator sets
// Temperature unconditionally, and Opus 4.7+ reject it with a 400.
func TestApplyModelParamsSamplingStripped(t *testing.T) {
	p := NewProvider("k", WithTemperature(0.2))

	for _, model := range []string{"claude-opus-5", "claude-sonnet-5", "claude-opus-4-7", "claude-fable-5"} {
		t.Run(model, func(t *testing.T) {
			params := anthropic.MessageNewParams{
				Model:       anthropic.Model(model),
				Temperature: anthropic.Float(0.2),
				TopP:        anthropic.Float(0.9),
				TopK:        anthropic.Int(40),
			}
			p.applyModelParams(&params, nil)

			if params.Temperature.Valid() {
				t.Errorf("Temperature must be dropped on %s, got %v", model, params.Temperature)
			}
			if params.TopP.Valid() {
				t.Error("TopP must be dropped")
			}
			if params.TopK.Valid() {
				t.Error("TopK must be dropped")
			}
		})
	}

	// Models that still accept sampling params must keep them.
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-4-6", "claude-haiku-4-5"} {
		t.Run(model+"_kept", func(t *testing.T) {
			params := anthropic.MessageNewParams{
				Model:       anthropic.Model(model),
				Temperature: anthropic.Float(0.2),
			}
			p.applyModelParams(&params, nil)
			if !params.Temperature.Valid() {
				t.Errorf("Temperature must be preserved on %s", model)
			}
		})
	}
}

func TestApplyModelParamsThinkingShape(t *testing.T) {
	rc := &provider.ReasoningConfig{Effort: provider.ReasoningEffortHigh}

	t.Run("adaptive model sends adaptive, never budget", func(t *testing.T) {
		p := NewProvider("k")
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, rc)

		if params.Thinking.OfAdaptive == nil {
			t.Fatal("want adaptive thinking config")
		}
		if params.Thinking.OfEnabled != nil {
			t.Error("budget_tokens config is a 400 on this model")
		}
		if params.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
			t.Errorf("effort = %q, want high", params.OutputConfig.Effort)
		}
	})

	t.Run("always-on model sends no thinking field", func(t *testing.T) {
		p := NewProvider("k")
		params := anthropic.MessageNewParams{Model: "claude-fable-5"}
		p.applyModelParams(&params, rc)

		if params.Thinking.OfAdaptive != nil || params.Thinking.OfEnabled != nil {
			t.Error("Fable 5 rejects any explicit thinking config")
		}
		if params.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
			t.Error("effort should still be applied")
		}
	})

	t.Run("legacy model keeps budget_tokens", func(t *testing.T) {
		p := NewProvider("k")
		params := anthropic.MessageNewParams{Model: "claude-sonnet-4-5"}
		p.applyModelParams(&params, rc)

		if params.Thinking.OfEnabled == nil {
			t.Fatal("want enabled/budget_tokens config")
		}
		if got := params.Thinking.OfEnabled.BudgetTokens; got != 16384 {
			t.Errorf("budget = %d, want 16384 (high tier)", got)
		}
		if params.OutputConfig.Effort != "" {
			t.Error("Sonnet 4.5 does not accept output_config.effort")
		}
	})

	t.Run("no reasoning requested sends no thinking", func(t *testing.T) {
		p := NewProvider("k")
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, nil)

		if params.Thinking.OfAdaptive != nil {
			t.Error("thinking must stay unset when the caller did not ask")
		}
	})

	t.Run("budget request maps onto the effort ladder", func(t *testing.T) {
		p := NewProvider("k")
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, &provider.ReasoningConfig{BudgetTokens: 32000})

		if params.Thinking.OfAdaptive == nil {
			t.Fatal("want adaptive thinking")
		}
		if params.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
			t.Errorf("effort = %q, want high", params.OutputConfig.Effort)
		}
	})

	t.Run("include reasoning asks for summaries", func(t *testing.T) {
		p := NewProvider("k", WithIncludeReasoning(true))
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, &provider.ReasoningConfig{
			Effort:          provider.ReasoningEffortMedium,
			IncludeInOutput: true,
		})

		if params.Thinking.OfAdaptive == nil {
			t.Fatal("want adaptive thinking")
		}
		// Default display is "omitted" on 4.7+, which yields empty text.
		if params.Thinking.OfAdaptive.Display != anthropic.ThinkingConfigAdaptiveDisplaySummarized {
			t.Error("want summarized display when reasoning is surfaced")
		}
	})
}

func TestWithEffortAndCompatOverrides(t *testing.T) {
	t.Run("WithEffort supplies levels the neutral enum cannot express", func(t *testing.T) {
		p := NewProvider("k", WithEffort(anthropic.OutputConfigEffortXhigh))
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, nil)

		if params.OutputConfig.Effort != anthropic.OutputConfigEffortXhigh {
			t.Errorf("effort = %q, want xhigh", params.OutputConfig.Effort)
		}
		// WithEffort alone counts as asking for thinking.
		if params.Thinking.OfAdaptive == nil {
			t.Error("want adaptive thinking enabled by WithEffort")
		}
	})

	t.Run("per-request effort beats WithEffort", func(t *testing.T) {
		p := NewProvider("k", WithEffort(anthropic.OutputConfigEffortMax))
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, &provider.ReasoningConfig{Effort: provider.ReasoningEffortLow})

		if params.OutputConfig.Effort != anthropic.OutputConfigEffortLow {
			t.Errorf("effort = %q, want low", params.OutputConfig.Effort)
		}
	})

	t.Run("WithThinkingCompat pins the wire format", func(t *testing.T) {
		p := NewProvider("k", WithThinkingCompat(ThinkingCompatBudget), WithThinkingBudget(2048))
		params := anthropic.MessageNewParams{Model: "claude-opus-5"}
		p.applyModelParams(&params, nil)

		if params.Thinking.OfEnabled == nil {
			t.Fatal("compat override should force the legacy shape")
		}
	})

	t.Run("WithSamplingParams re-enables temperature", func(t *testing.T) {
		p := NewProvider("k", WithSamplingParams(true))
		params := anthropic.MessageNewParams{
			Model:       "claude-opus-5",
			Temperature: anthropic.Float(0.3),
		}
		p.applyModelParams(&params, nil)

		if !params.Temperature.Valid() {
			t.Error("override should keep Temperature on the wire")
		}
	})
}

// TestLookupCapsInferenceProfileIDs guards the id shape Bedrock actually
// requires: current models are only invocable through a cross-region
// inference profile, whose id carries a geo prefix on top of the Bedrock
// "anthropic." one. Without stripping both, an older model behind a
// profile would fall through to defaultCaps and be sent adaptive thinking
// — a hard 400 on a model that only takes budget_tokens.
func TestLookupCapsInferenceProfileIDs(t *testing.T) {
	tests := []struct {
		model        string
		wantThinking thinkingMode
		wantSampling bool
	}{
		{"us.anthropic.claude-opus-4-8", thinkingAdaptive, false},
		{"eu.anthropic.claude-opus-4-8", thinkingAdaptive, false},
		{"apac.anthropic.claude-sonnet-5", thinkingAdaptive, false},
		{"us-gov.anthropic.claude-opus-4-8", thinkingAdaptive, false},
		// The prefix must not upgrade an older model's contract.
		{"us.anthropic.claude-sonnet-4-5", thinkingLegacyBudget, true},
		{"us.anthropic.claude-haiku-4-5", thinkingLegacyBudget, true},
		{"eu.anthropic.claude-opus-4-6", thinkingAdaptive, true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := lookupCaps(tt.model)
			if got.thinking != tt.wantThinking {
				t.Errorf("thinking = %v, want %v", got.thinking, tt.wantThinking)
			}
			if got.sampling != tt.wantSampling {
				t.Errorf("sampling = %v, want %v", got.sampling, tt.wantSampling)
			}
		})
	}
}

// Opus 5.5 and Fable / Mythos 5.1 return 400 for tool_choice "any" and
// "tool" ("tool_choice: type "tool" and "any" are not supported for this
// model"). The provider falls back to auto on exactly those models; every
// other model keeps the forced choice the caller asked for.
func TestApplyModelParamsForcedToolChoice(t *testing.T) {
	forced := map[string]func() anthropic.ToolChoiceUnionParam{
		"any": func() anthropic.ToolChoiceUnionParam { return *buildToolChoiceParams(provider.ToolChoiceRequired()) },
		"tool": func() anthropic.ToolChoiceUnionParam {
			return *buildToolChoiceParams(provider.ToolChoiceSpecific("get_weather"))
		},
	}
	p := NewProvider("k")

	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1", "claude-mythos-5-1", "us.anthropic.claude-opus-5-5"} {
		for kind, choice := range forced {
			t.Run(model+"/"+kind+"_falls_back_to_auto", func(t *testing.T) {
				params := anthropic.MessageNewParams{Model: anthropic.Model(model), ToolChoice: choice()}
				p.applyModelParams(&params, nil)

				if params.ToolChoice.OfAuto == nil || params.ToolChoice.OfAny != nil || params.ToolChoice.OfTool != nil {
					t.Errorf("ToolChoice = %+v, want auto", params.ToolChoice)
				}
			})
		}
		t.Run(model+"/none_is_kept", func(t *testing.T) {
			params := anthropic.MessageNewParams{Model: anthropic.Model(model), ToolChoice: *buildToolChoiceParams(provider.ToolChoiceNone())}
			p.applyModelParams(&params, nil)

			if params.ToolChoice.OfNone == nil {
				t.Errorf("ToolChoice = %+v, want none (supported on %s)", params.ToolChoice, model)
			}
		})
	}

	for _, model := range []string{"claude-opus-5", "claude-fable-5", "claude-sonnet-5", "claude-haiku-4-5"} {
		for kind, choice := range forced {
			t.Run(model+"/"+kind+"_is_kept", func(t *testing.T) {
				params := anthropic.MessageNewParams{Model: anthropic.Model(model), ToolChoice: choice()}
				p.applyModelParams(&params, nil)

				if params.ToolChoice.OfAuto != nil {
					t.Errorf("forced tool_choice %q must reach %s unchanged", kind, model)
				}
			})
		}
	}
}

// Opus 5.5 rejects {"type":"disabled"} and budget_tokens; the provider must
// send adaptive (with effort) when reasoning is requested and nothing at all
// otherwise.
func TestApplyModelParamsOpus55Thinking(t *testing.T) {
	p := NewProvider("k")

	params := anthropic.MessageNewParams{Model: "claude-opus-5-5"}
	p.applyModelParams(&params, &provider.ReasoningConfig{Effort: provider.ReasoningEffortHigh})
	if params.Thinking.OfAdaptive == nil || params.Thinking.OfEnabled != nil || params.Thinking.OfDisabled != nil {
		t.Errorf("Thinking = %+v, want adaptive only", params.Thinking)
	}
	if params.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
		t.Errorf("effort = %q, want high", params.OutputConfig.Effort)
	}

	params = anthropic.MessageNewParams{Model: "claude-opus-5-5"}
	p.applyModelParams(&params, &provider.ReasoningConfig{Effort: provider.ReasoningEffortNone})
	if params.Thinking.OfAdaptive != nil || params.Thinking.OfEnabled != nil || params.Thinking.OfDisabled != nil {
		t.Errorf("Thinking = %+v, want the field omitted", params.Thinking)
	}
}

package anthropic

import "strings"

// thinkingMode says how a Claude model accepts thinking configuration. The
// wire contract changed twice across the 4.x line, and sending the wrong
// shape is a hard 400 rather than a silently-ignored field:
//
//   - Pre-4.6 models take {"type":"enabled","budget_tokens":N}.
//   - 4.6 accepts both that and {"type":"adaptive"} (budget_tokens deprecated).
//   - 4.7 onward REMOVED budget_tokens — only adaptive, with depth controlled
//     by output_config.effort.
//   - Fable 5 / Mythos always think and reject any explicit thinking config,
//     including {"type":"disabled"} — the parameter must be omitted entirely.
type thinkingMode int

const (
	// thinkingLegacyBudget sends {"type":"enabled","budget_tokens":N}.
	thinkingLegacyBudget thinkingMode = iota
	// thinkingAdaptive sends {"type":"adaptive"} plus output_config.effort.
	thinkingAdaptive
	// thinkingAlwaysOn sends no thinking field at all; effort still applies.
	thinkingAlwaysOn
)

// modelCaps records the per-model request-surface differences the provider
// has to respect.
type modelCaps struct {
	thinking thinkingMode

	// sampling reports whether temperature / top_p / top_k are accepted.
	// Opus 4.7 onward, Sonnet 5, Opus 5 and Fable 5 removed them and 400 on
	// a non-default value.
	sampling bool

	// effort reports whether output_config.effort is accepted.
	effort bool
}

// capsTable maps a model-id prefix to its capabilities. Lookup takes the
// LONGEST matching prefix, so "claude-opus-4-7" wins over "claude-opus-4"
// for "claude-opus-4-7-20260301" — the same resolution rule the cost
// registry uses, and for the same reason: the 4.x line is not uniform.
var capsTable = map[string]modelCaps{
	// Thinking always on, no sampling params.
	"claude-fable-5": {thinking: thinkingAlwaysOn, sampling: false, effort: true},
	"claude-mythos":  {thinking: thinkingAlwaysOn, sampling: false, effort: true},

	// Adaptive-only, no sampling params.
	"claude-opus-5":   {thinking: thinkingAdaptive, sampling: false, effort: true},
	"claude-sonnet-5": {thinking: thinkingAdaptive, sampling: false, effort: true},
	"claude-opus-4-8": {thinking: thinkingAdaptive, sampling: false, effort: true},
	"claude-opus-4-7": {thinking: thinkingAdaptive, sampling: false, effort: true},

	// 4.6: adaptive is recommended and effort is GA, but sampling params
	// still work and budget_tokens is merely deprecated.
	"claude-opus-4-6":   {thinking: thinkingAdaptive, sampling: true, effort: true},
	"claude-sonnet-4-6": {thinking: thinkingAdaptive, sampling: true, effort: true},

	// 4.5 Opus: no adaptive thinking, but effort exists (low/medium/high).
	"claude-opus-4-5": {thinking: thinkingLegacyBudget, sampling: true, effort: true},

	// Everything older: budget_tokens only, no effort.
	"claude-opus-4":   {thinking: thinkingLegacyBudget, sampling: true, effort: false},
	"claude-sonnet-4": {thinking: thinkingLegacyBudget, sampling: true, effort: false},
	"claude-haiku-4":  {thinking: thinkingLegacyBudget, sampling: true, effort: false},
	"claude-3":        {thinking: thinkingLegacyBudget, sampling: true, effort: false},
	"claude-2":        {thinking: thinkingLegacyBudget, sampling: true, effort: false},
}

// defaultCaps is what an unrecognised id gets. It follows the CURRENT
// Anthropic contract (adaptive, no sampling params) rather than the legacy
// one, because every model released since Opus 4.7 works that way and the
// legacy shape is a hard 400 on all of them. An unknown id is far more
// likely to be a model newer than this table than one older than it.
//
// Gateways that proxy a non-Claude model behind the Anthropic wire format
// can override the whole decision with WithThinkingCompat.
var defaultCaps = modelCaps{thinking: thinkingAdaptive, sampling: false, effort: true}

// lookupCaps resolves a model id to its capabilities via longest-prefix
// match. Bedrock-style "anthropic."-prefixed ids are normalised first so a
// Bedrock-fronted Claude resolves the same as the first-party id.
func lookupCaps(model string) modelCaps {
	m := strings.TrimPrefix(model, "anthropic.")

	var bestKey string
	var best modelCaps
	for k, c := range capsTable {
		if !familyPrefix(m, k) {
			continue
		}
		if len(k) > len(bestKey) {
			bestKey, best = k, c
		}
	}
	if bestKey == "" {
		return defaultCaps
	}
	return best
}

// familyPrefix reports whether key is a family-level prefix of model. The
// prefix must end at end-of-string or at a `-`/`.`/`@` boundary, so
// "claude-opus-4" does not match "claude-opus-45" while it still matches
// "claude-opus-4-1-20250805" and the Vertex form "claude-opus-4-5@20251101".
func familyPrefix(model, key string) bool {
	if key == "" || !strings.HasPrefix(model, key) {
		return false
	}
	if len(model) == len(key) {
		return true
	}
	switch model[len(key)] {
	case '-', '.', '@':
		return true
	}
	return false
}

package openai

import (
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// applyMaxTokens writes the per-request output cap to either MaxTokens
// (legacy chat models) or MaxCompletionTokens (o-series reasoning
// models + gpt-5 family) on params, based on the model name. OpenAI
// deprecated max_tokens for the newer families: requests that still
// carry it are rejected with a 400.
//
// n <= 0 is a no-op so the legacy "0 means unset" semantic survives.
func applyMaxTokens(params *openai.ChatCompletionNewParams, model string, n int) {
	if n <= 0 {
		return
	}
	if usesCompletionTokensParam(model) {
		params.MaxCompletionTokens = openai.Int(int64(n))
		return
	}
	params.MaxTokens = openai.Int(int64(n))
}

// usesCompletionTokensParam reports whether model belongs to a family
// that rejects max_tokens and requires max_completion_tokens. Match is
// prefix-based and case-insensitive so versioned ids — o1-preview-2024-09-12,
// gpt-5.4-mini, gpt-5-nano-2025-08-07, etc. — all resolve correctly.
//
// Anything not matched falls back to the legacy max_tokens path; if a
// future model gets added to the "completion tokens" family before this
// list is updated, OpenAI will reject the request with a clear error
// message — the symptom is loud, not silent.
func usesCompletionTokensParam(model string) bool {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"),
		strings.HasPrefix(m, "gpt-5"):
		return true
	}
	return false
}

// newParamsForModel is a test-only helper that returns a minimally
// populated params struct. Keeps unit tests independent of the
// translator's wider setup.
func newParamsForModel(model string) openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{Model: shared.ChatModel(model)}
}

// Package provider defines the provider abstraction layer for the Looper Agent
// framework. It unifies streaming, non-streaming, structured output, and tool
// calls under a single LLMProvider interface.
//
// Each provider implementation (openai, anthropic, google, custom) encapsulates
// its own Translator to convert universal messages to its native API format.
// The user never interacts with provider-specific message formats.
package provider

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// LLMRequest is a provider-agnostic request to an LLM.
type LLMRequest struct {
	// SystemPrompt is the resolved system prompt (func(ctx) string evaluated).
	// It is NOT stored in History; the Translator injects it per call.
	SystemPrompt string

	// Messages contains the conversation history in universal format.
	Messages []message.Message

	// Tools available to the LLM for this call.
	Tools []*tool.Tool

	// Stream enables streaming (ChatStream instead of Chat).
	Stream bool

	// Model overrides the default model for this call.
	Model string

	// MaxTokens is the maximum completion tokens.
	MaxTokens int

	// Temperature controls randomness (0.0 to 2.0).
	Temperature float64

	// Reasoning configures extended thinking / reasoning for models that
	// support it (OpenAI o-series, Claude extended thinking, Gemini 2.x).
	// Nil means "use the provider default", which is typically "off". The
	// provider silently ignores this field on models that don't support it.
	Reasoning *ReasoningConfig

	// ToolChoice constrains how the model picks tools on this turn. The
	// zero value (ToolChoice{}) means "auto" — same as ToolChoiceAuto().
	// Each provider translator maps it to its native shape.
	ToolChoice ToolChoice

	// ResponseSchema, when non-nil, asks the provider to constrain the
	// model's output to this JSON Schema. Only providers that implement
	// ResponseFormatCapable honor it (OpenAI / Gemini today). The framework
	// passes the same schema this field carries to those providers; for
	// non-capable providers the agent loop falls back to a final_response
	// tool injection that achieves the same end via tool calls.
	ResponseSchema []byte

	// ResponseSchemaName is a human-readable label some providers require
	// alongside the schema (OpenAI's response_format.json_schema.name).
	// Defaults to "result" when empty.
	ResponseSchemaName string

	// ResponseFormatMode lets callers pick how the OpenAI-compat provider
	// expresses structured-output intent. The zero value preserves
	// historical behavior (json_schema, non-strict). Useful when routing
	// through OpenRouter or talking to providers like DeepSeek that reject
	// json_schema entirely with "This response_format type is unavailable now"
	// and only accept the looser json_object mode.
	//
	// See ResponseFormatMode* constants. Callers that don't care should
	// leave this empty.
	ResponseFormatMode ResponseFormatMode

	// ExtraParams is an escape hatch for provider-specific request fields
	// that the universal LLMRequest doesn't (yet) model. The OpenAI-compat
	// provider serializes these alongside the standard fields via the
	// underlying SDK's SetExtraFields hook.
	//
	// The canonical use case is OpenRouter routing knobs, e.g.:
	//
	//   ExtraParams: map[string]any{
	//       "provider": map[string]any{"require_parameters": true},
	//   }
	//
	// Providers other than openai currently ignore this field. Keep it
	// scoped to truly provider-specific fields; promote common ones to
	// proper LLMRequest fields when they recur across providers.
	ExtraParams map[string]any
}

// ResponseFormatMode tells the OpenAI-compat provider how to wrap
// ResponseSchema into the OpenAI response_format union. The zero value
// (ResponseFormatAuto) reproduces the SDK's historical behavior.
type ResponseFormatMode string

const (
	// ResponseFormatAuto preserves the SDK's default: when ResponseSchema is
	// set, send response_format=json_schema (strict=false). Compatible with
	// OpenAI, Gemini, and any OpenRouter provider that advertises
	// require_parameters support for structured outputs.
	ResponseFormatAuto ResponseFormatMode = ""

	// ResponseFormatJSONSchema explicitly requests strict-style structured
	// output (response_format=json_schema). Same wire shape as Auto today;
	// kept as a distinct enum value so future strict-mode toggles have a
	// place to land without breaking callers that asked for it explicitly.
	ResponseFormatJSONSchema ResponseFormatMode = "json_schema"

	// ResponseFormatJSONObject downgrades to OpenAI's looser json_object
	// mode: the model is asked to emit valid JSON but the SDK does NOT pass
	// the schema body. Pair this with schema-in-prompt + client-side
	// validation when talking to providers like DeepSeek and Qwen, which
	// reject json_schema.
	ResponseFormatJSONObject ResponseFormatMode = "json_object"

	// ResponseFormatNone suppresses response_format entirely even when
	// ResponseSchema is set. Useful when probing a brand-new model whose
	// support for either mode is unknown.
	ResponseFormatNone ResponseFormatMode = "none"
)

// ReasoningEffort is a provider-neutral level of reasoning effort. Each
// provider maps it to its native scale: OpenAI reasoning_effort, Anthropic
// budget_tokens tiers, Gemini thinkingBudget tiers.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = ""
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	// ReasoningEffortMinimal is OpenAI gpt-5 specific; non-OpenAI providers
	// treat it as "low".
	ReasoningEffortMinimal ReasoningEffort = "minimal"
)

// ReasoningConfig controls thinking/reasoning behaviour per request.
type ReasoningConfig struct {
	// Effort hints how hard the model should think. Maps to the provider's
	// native scale. Use ReasoningEffortNone to disable.
	Effort ReasoningEffort

	// BudgetTokens overrides the tiered Effort with a concrete budget where
	// supported (Anthropic budget_tokens, Gemini thinkingBudget). Zero
	// means "use Effort". Ignored by OpenAI (no per-request budget).
	BudgetTokens int

	// IncludeInOutput controls whether reasoning traces are surfaced via
	// StreamChunk.Reasoning / LLMResponse.Reasoning. When false (default),
	// the provider drops reasoning deltas server-side where possible and
	// strips them client-side otherwise.
	IncludeInOutput bool
}

// LLMResponse is a provider-agnostic response from an LLM.
type LLMResponse struct {
	// Content is the text content of the response.
	Content string

	// Reasoning is the model's internal thinking trace, when the request
	// asked for it via ReasoningConfig.IncludeInOutput and the model
	// supports it. Empty otherwise.
	Reasoning string

	// ToolCalls are tool invocations requested by the LLM.
	ToolCalls []message.ToolCall

	// Usage reports token consumption.
	Usage Usage

	// IsFinal indicates this is a final response (not a tool call).
	IsFinal bool

	// ProviderID identifies which provider type actually answered this
	// call ("openai" | "anthropic" | "google"). Base providers set it
	// before returning; multi-provider wrappers (FailoverProvider,
	// KeyRotationProvider) propagate the inner's value transparently.
	// Used by the loop to attribute Usage to the correct cost-table
	// entry — critical when a single run is served by several providers.
	// Empty when the response came from a legacy provider that hasn't
	// been updated to set the field.
	ProviderID string

	// ModelID identifies the model that produced this response (the
	// effective model after request-level overrides, not the provider's
	// default). Same propagation rules as ProviderID. Useful when a
	// chain mixes several models within one run.
	ModelID string

	// Fallback is set by FailoverProvider when this response came from a
	// non-primary inner (i.e. the chain switched away from inner[0]).
	// Always false when the primary answered. Surfaces "did this turn
	// hit the fallback path?" without callers parsing logs.
	Fallback bool

	// APIKeySuffix is the last 4 characters of the API key that actually
	// served this call, prefixed with "****" (e.g. "****a2Fn"). Empty for
	// local / keyless providers (LM Studio, Ollama) and for legacy
	// providers that don't expose the key surface. Surfaced on the trace
	// UI so operators can tell apart which of several rotating keys
	// (KeyRotationProvider) or which of several Failover inners answered.
	// Multi-provider chains propagate the inner's value transparently.
	APIKeySuffix string
}

// Usage reports token consumption for an LLM call.
//
// Normalisation contract (every adapter must follow it so the cost model can
// price uniformly):
//
//   - InputTokens is the TOTAL prompt-side token count, including cached
//     reads and cache writes. Anthropic reports these three buckets
//     disjointly (input + cache_read + cache_creation), so its adapter sums
//     them; OpenAI and Gemini already report inclusive totals.
//   - CachedTokens is the cache-READ subset of InputTokens.
//   - CacheWriteTokens is the cache-WRITE subset of InputTokens (Anthropic
//     cache_creation_input_tokens, billed at a premium). Zero on providers
//     whose cache writes are free/implicit (OpenAI, Gemini implicit cache).
//   - OutputTokens includes reasoning/thinking tokens when the provider
//     bills them as output but reports them separately (Gemini's
//     thoughts_token_count).
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	CacheWriteTokens int

	// Cost is the USD cost reported by the upstream API for this call, when
	// the provider exposes it (e.g. OpenRouter returns usage.cost). Zero when
	// the upstream reports no cost; in that case the telemetry cost model
	// estimates from its pricing tables (custom overrides first, then the
	// built-in matrix). Multi-provider chains (FailoverProvider,
	// KeyRotationProvider) propagate the inner's value.
	Cost float64
}

// StreamChunk represents a chunk of a streaming LLM response.
type StreamChunk struct {
	// Content is the text chunk (partial).
	Content string

	// Reasoning is a chunk of the model's thinking trace, delivered on a
	// separate channel from Content. Only populated when the request set
	// ReasoningConfig.IncludeInOutput=true and the model supports
	// reasoning. Per-chunk: Content and Reasoning are mutually exclusive
	// (a chunk carries one OR the other, never both).
	Reasoning string

	// ToolCalls are partial tool call data (accumulated across chunks).
	ToolCalls []message.ToolCall

	// IsFinal indicates this is the last chunk.
	IsFinal bool

	// Usage is the token usage. Set on the final chunk, and — when the
	// upstream reported any usage before dying — also on error chunks, so
	// a call that fails mid-stream still bills the tokens it consumed.
	// Never set on ordinary delta chunks.
	Usage *Usage

	// Error is set if the stream encountered an error.
	Error error

	// ProviderID / ModelID / Fallback carry the same provenance signals as
	// LLMResponse. Base providers set them on every chunk; FailoverProvider
	// flips Fallback to true on every chunk of the inner it picked when
	// that inner was not the primary. The loop reads them off the final
	// chunk (where Usage lives) to attribute the call's tokens to the
	// correct (provider, model) bucket.
	ProviderID string
	ModelID    string
	Fallback   bool

	// APIKeySuffix is the "****xxxx" suffix of the key that opened this
	// stream. Set on every chunk (not just the final) so per-chunk
	// attribution survives in the trace UI when a KeyRotationProvider
	// or FailoverProvider chain interleaves keys. Empty for keyless
	// providers (LM Studio, Ollama).
	APIKeySuffix string
}

// LLMProvider abstracts any LLM API under a unified interface.
type LLMProvider interface {
	// Model returns the default model identifier.
	Model() string

	// Chat sends a non-streaming request and returns the full response.
	Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error)

	// ChatStream sends a streaming request and returns a channel of chunks.
	ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error)

	// Translator returns the provider's message translator.
	Translator() Translator
}

// Translator converts between universal message format and provider-native
// API format. Each provider implements its own Translator. The user never
// interacts with Translator directly.
type Translator interface {
	// ToNative converts universal messages + system prompt to the
	// provider's native request format. The system prompt is injected
	// here (NOT from History) to prevent duplication on resume.
	ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any

	// FromNative converts the provider's native response to a
	// universal LLMResponse.
	FromNative(response any) (*LLMResponse, error)
}

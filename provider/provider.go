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
}

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
}

// Usage reports token consumption for an LLM call.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
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

	// Usage is the token usage (only set on final chunk).
	Usage *Usage

	// Error is set if the stream encountered an error.
	Error error
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

// Package anthropic implements the Anthropic LLMProvider using the official
// anthropic-sdk-go library.
//
// It supports Anthropic-specific features like cache_control breakpoints
// for prompt caching, which are injected automatically by the provider.
package anthropic

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// Provider implements provider.LLMProvider for Anthropic.
type Provider struct {
	model  string
	apiKey string
	config *provider.CacheConfig

	translator *Translator
}

// Option configures an Anthropic Provider.
type Option func(*Provider)

// WithModel sets the model name (default: "claude-sonnet-4-20250514").
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

// WithCacheBreakpoints enables cache_control breakpoints.
func WithCacheBreakpoints(breakpoints ...string) Option {
	return func(p *Provider) {}
}

// CacheSystemPrompt is a constant for caching the system prompt.
const CacheSystemPrompt = "system"

// CacheTools is a constant for caching tool definitions.
const CacheTools = "tools"

// NewProvider creates an Anthropic provider with the given API key.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:  "claude-sonnet-4-20250514",
		apiKey: apiKey,
		config: &provider.CacheConfig{Strategy: provider.CacheAuto},
	}
	for _, opt := range opts {
		opt(p)
	}
	p.translator = &Translator{model: p.model}
	return p
}

// Model returns the configured model name.
func (p *Provider) Model() string { return p.model }

// Chat sends a non-streaming request.
func (p *Provider) Chat(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	// Placeholder: Phase 4
	_ = ctx
	_ = req
	return nil, nil
}

// ChatStream sends a streaming request.
func (p *Provider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	// Placeholder: Phase 4
	_ = ctx
	_ = req
	return nil, nil
}

// Translator returns the Anthropic message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

// Translator converts between universal messages and Anthropic-native format.
type Translator struct {
	model string
}

// ToNative converts universal messages to Anthropic format.
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	// Placeholder: Phase 4
	_ = systemPrompt
	_ = messages
	_ = tools
	return nil
}

// FromNative converts an Anthropic response to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	// Placeholder: Phase 4
	_ = response
	return nil, nil
}

// Package google implements the Google GenAI LLMProvider using the official
// go-genai library (github.com/googleapis/go-genai).
//
// It supports Google-specific features like Context Caching for long prompts.
package google

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// Provider implements provider.LLMProvider for Google GenAI.
type Provider struct {
	model  string
	apiKey string
	config *provider.CacheConfig

	translator *Translator
}

// Option configures a Google Provider.
type Option func(*Provider)

// WithModel sets the model name (default: "gemini-2.5-pro").
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

// WithCacheTTL sets the context cache TTL.
func WithCacheTTL(ttl string) Option {
	return func(p *Provider) {}
}

// NewProvider creates a Google GenAI provider with the given API key.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:  "gemini-2.5-pro",
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

// Translator returns the Google message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

// Translator converts between universal messages and Google-native format.
type Translator struct {
	model string
}

// ToNative converts universal messages to Google format.
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	// Placeholder: Phase 4
	_ = systemPrompt
	_ = messages
	_ = tools
	return nil
}

// FromNative converts a Google response to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	// Placeholder: Phase 4
	_ = response
	return nil, nil
}

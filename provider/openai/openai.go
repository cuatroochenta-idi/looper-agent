// Package openai implements the OpenAI LLMProvider using the official
// openai-go SDK (github.com/openai/openai-go).
//
// The translator converts universal messages to OpenAI's chat completion
// format. The system prompt is injected as the first message in each request,
// not stored in History.
package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// Provider implements provider.LLMProvider for OpenAI.
type Provider struct {
	model string
	// client is a value type, not a pointer (openai.NewClient returns Client).
	client      openai.Client
	config      *provider.CacheConfig
	translator  *Translator
	maxTokens   int
	temperature float64

	// apiKeys holds multiple keys for rotation.
	apiKeys     []string
	keyIndex    int
}

// Option configures an OpenAI Provider.
type Option func(*Provider)

// WithModel sets the model name (default: "gpt-4o").
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

// WithCacheStrategy sets the cache behavior.
func WithCacheStrategy(strategy provider.CacheStrategy) Option {
	return func(p *Provider) { p.config.Strategy = strategy }
}

// WithCacheMinTokens sets the minimum tokens to trigger caching.
func WithCacheMinTokens(n int) Option {
	return func(p *Provider) { p.config.MinTokens = n }
}

// WithMaxTokens sets the default max completion tokens.
func WithMaxTokens(n int) Option {
	return func(p *Provider) { p.maxTokens = n }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(p *Provider) { p.temperature = t }
}

// WithBaseURL sets a custom base URL (for OpenRouter, Ollama, etc.).
func WithBaseURL(url string) Option {
	return func(p *Provider) {
		p.client = openai.NewClient(
			option.WithAPIKey("custom"),
			option.WithBaseURL(url),
		)
	}
}

// WithAPIKeys sets multiple API keys for round-robin rotation.
func WithAPIKeys(keys ...string) Option {
	return func(p *Provider) {
		p.apiKeys = keys
	}
}

// NewProvider creates an OpenAI provider with the given API key.
func NewProvider(apiKey string, opts ...Option) *Provider {
	cfg := provider.DefaultCacheConfig()
	p := &Provider{
		model:       "gpt-4o",
		config:      &cfg,
		maxTokens:   4096,
		temperature: 0.7,
	}

	for _, opt := range opts {
		opt(p)
	}

	if len(p.apiKeys) == 0 {
		p.apiKeys = []string{apiKey}
	}

	p.client = openai.NewClient(option.WithAPIKey(p.apiKeys[0]))

	p.translator = &Translator{
		model:       p.model,
		maxTokens:   p.maxTokens,
		temperature: p.temperature,
	}
	return p
}

// Model returns the configured model name.
func (p *Provider) Model() string { return p.model }

// Chat sends a non-streaming chat completion request.
func (p *Provider) Chat(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	params := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(openai.ChatCompletionNewParams)

	if req.Model != "" {
		params.Model = shared.ChatModel(req.Model)
	} else {
		params.Model = shared.ChatModel(p.model)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	}
	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}

	chat, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	resp, err := p.translator.FromNative(chat)
	if err != nil {
		return nil, fmt.Errorf("openai translate response: %w", err)
	}
	return resp, nil
}

// ChatStream sends a streaming chat completion request.
func (p *Provider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	params := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(openai.ChatCompletionNewParams)

	if req.Model != "" {
		params.Model = shared.ChatModel(req.Model)
	} else {
		params.Model = shared.ChatModel(p.model)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	}
	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		var toolCallMap map[int]*toolCallAccumulator
		var contentBuilder string

		for stream.Next() {
			chunk := stream.Current()

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta

				// Text content delta
				if delta.Content != "" {
					contentBuilder += delta.Content
					ch <- provider.StreamChunk{
						Content: delta.Content,
					}
				}

				// Tool calls (accumulate across chunks)
				if len(delta.ToolCalls) > 0 {
					if toolCallMap == nil {
						toolCallMap = make(map[int]*toolCallAccumulator)
					}
					for _, tc := range delta.ToolCalls {
						idx := int(tc.Index)
						if _, ok := toolCallMap[idx]; !ok {
							toolCallMap[idx] = &toolCallAccumulator{}
						}
						acc := toolCallMap[idx]
						if tc.ID != "" {
							acc.ID = tc.ID
						}
						if tc.Function.Name != "" {
							acc.Name = tc.Function.Name
						}
						acc.Arguments += tc.Function.Arguments
					}
				}

				// Check finish reason
				if chunk.Choices[0].FinishReason != "" {
					ch <- buildFinalChunk(contentBuilder, toolCallMap, &chunk)
					return
				}
			}
		}

		// Stream ended — emit accumulated content
		ch <- buildFinalChunk(contentBuilder, toolCallMap, nil)
	}()

	return ch, nil
}

// Translator returns the OpenAI message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

// buildFinalChunk assembles the final StreamChunk from accumulated data.
func buildFinalChunk(content string, tcm map[int]*toolCallAccumulator, chunk *openai.ChatCompletionChunk) provider.StreamChunk {
	var tcs []message.ToolCall
	for _, acc := range tcm {
		if acc.Name != "" {
			tcs = append(tcs, message.ToolCall{
				ID:        acc.ID,
				Name:      acc.Name,
				Arguments: json.RawMessage(acc.Arguments),
			})
		}
	}

	sc := provider.StreamChunk{
		Content:   content,
		ToolCalls: tcs,
		IsFinal:   true,
	}

	if chunk != nil {
		sc.Usage = &provider.Usage{
			InputTokens:  int(chunk.Usage.PromptTokens),
			OutputTokens: int(chunk.Usage.CompletionTokens),
		}
	}

	return sc
}

// toolCallAccumulator accumulates partial tool call data across streaming chunks.
type toolCallAccumulator struct {
	ID        string
	Name      string
	Arguments string
}

// Translator converts between universal messages and OpenAI-native format.
type Translator struct {
	model       string
	maxTokens   int
	temperature float64
}

// ToNative converts universal messages to OpenAI's chat completion params.
// The system prompt is injected as the first message (NOT from History).
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	var openaiMessages []openai.ChatCompletionMessageParamUnion

	// System prompt first (never from History)
	if systemPrompt != "" {
		openaiMessages = append(openaiMessages, openai.SystemMessage(systemPrompt))
	}

	// Convert history messages
	for _, msg := range messages {
		switch msg.Type {
		case message.MessageSystem:
			openaiMessages = append(openaiMessages, openai.SystemMessage(msg.Content))
		case message.MessageUser:
			openaiMessages = append(openaiMessages, openai.UserMessage(msg.Content))
		case message.MessageAssistant:
			if len(msg.ToolCalls) > 0 {
				params := openai.ChatCompletionAssistantMessageParam{}
				if msg.Content != "" {
					params.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(msg.Content),
					}
				}
				params.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
				for i, tc := range msg.ToolCalls {
					params.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: string(tc.Arguments),
						},
					}
				}
				openaiMessages = append(openaiMessages, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &params,
				})
			} else {
				openaiMessages = append(openaiMessages, openai.AssistantMessage(msg.Content))
			}
		case message.MessageTool:
			openaiMessages = append(openaiMessages, openai.ToolMessage(msg.Content, msg.ToolID))
		}
	}

	// Convert tools
	var openaiTools []openai.ChatCompletionToolParam
	if len(tools) > 0 {
		openaiTools = make([]openai.ChatCompletionToolParam, len(tools))
		for i, tl := range tools {
			openaiTools[i] = openai.ChatCompletionToolParam{
				Type: "function",
				Function: shared.FunctionDefinitionParam{
					Name:        tl.Name(),
					Description: openai.String(tl.Description()),
					Parameters:  shared.FunctionParameters(tl.SchemaMap()),
				},
			}
		}
	}

	params := openai.ChatCompletionNewParams{
		Messages:    openaiMessages,
		Model:       shared.ChatModel(t.model),
		Temperature: openai.Float(t.temperature),
	}
	if t.maxTokens > 0 {
		params.MaxTokens = openai.Int(int64(t.maxTokens))
	}
	if len(openaiTools) > 0 {
		params.Tools = openaiTools
	}

	return params
}

// FromNative converts an OpenAI ChatCompletion response to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	chat, ok := response.(*openai.ChatCompletion)
	if !ok {
		return nil, fmt.Errorf("expected *openai.ChatCompletion, got %T", response)
	}

	result := &provider.LLMResponse{
		Usage: provider.Usage{
			InputTokens:  int(chat.Usage.PromptTokens),
			OutputTokens: int(chat.Usage.CompletionTokens),
			CachedTokens: int(chat.Usage.PromptTokensDetails.CachedTokens),
		},
	}

	if len(chat.Choices) == 0 {
		return result, nil
	}

	choice := chat.Choices[0]

	// Content
	result.Content = choice.Message.Content

	// Tool calls
	if len(choice.Message.ToolCalls) > 0 {
		result.ToolCalls = make([]message.ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			result.ToolCalls[i] = message.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			}
		}
	}

	if choice.FinishReason == "stop" && len(result.ToolCalls) == 0 {
		result.IsFinal = true
	}

	return result, nil
}

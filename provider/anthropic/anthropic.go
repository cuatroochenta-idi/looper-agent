// Package anthropic implements the Anthropic LLMProvider using the official
// anthropic-sdk-go library.
//
// It supports Anthropic-specific features: top-level System prompt field,
// cache_control breakpoints for prompt caching, and tool use via content blocks.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// Provider implements provider.LLMProvider for Anthropic.
type Provider struct {
	model       string
	client      anthropic.Client
	config      *provider.CacheConfig
	translator  *Translator
	maxTokens   int
	temperature float64
}

// Option configures an Anthropic Provider.
type Option func(*Provider)

// WithModel sets the model name.
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

const CacheSystemPrompt = "system"
const CacheTools = "tools"

// WithCacheBreakpoints enables cache_control breakpoints.
func WithCacheBreakpoints(breakpoints ...string) Option {
	return func(p *Provider) {}
}

// WithMaxTokens sets the default max tokens.
func WithMaxTokens(n int) Option {
	return func(p *Provider) { p.maxTokens = n }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(p *Provider) { p.temperature = t }
}

// NewProvider creates an Anthropic provider.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:       anthropic.ModelClaudeSonnet4_20250514,
		config:      &provider.CacheConfig{Strategy: provider.CacheAuto},
		maxTokens:   4096,
		temperature: 1.0,
	}
	for _, opt := range opts {
		opt(p)
	}
	p.client = anthropic.NewClient(option.WithAPIKey(apiKey))
	p.translator = &Translator{
		model:       p.model,
		maxTokens:   p.maxTokens,
		temperature: p.temperature,
	}
	return p
}

// Model returns the configured model name.
func (p *Provider) Model() string { return p.model }

// Chat sends a non-streaming request.
func (p *Provider) Chat(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	params := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(anthropic.MessageNewParams)

	if req.Model != "" {
		params.Model = anthropic.Model(req.Model)
	} else {
		params.Model = anthropic.Model(p.model)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = int64(req.MaxTokens)
	}
	if req.Temperature != 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	return p.translator.FromNative(resp)
}

// ChatStream sends a streaming request.
func (p *Provider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	params := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(anthropic.MessageNewParams)

	if req.Model != "" {
		params.Model = anthropic.Model(req.Model)
	} else {
		params.Model = anthropic.Model(p.model)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = int64(req.MaxTokens)
	}
	if req.Temperature != 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	stream := p.client.Messages.NewStreaming(ctx, params)
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		var (
			contentBuilder string
			toolUseMap     = make(map[int64]*toolUseAccumulator)
		)

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				if e.ContentBlock.Type == "tool_use" {
					toolUseMap[e.Index] = &toolUseAccumulator{
						id:   e.ContentBlock.ID,
						name: e.ContentBlock.Name,
					}
				}
			case anthropic.ContentBlockDeltaEvent:
				switch d := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					contentBuilder += d.Text
					ch <- provider.StreamChunk{Content: d.Text}
				case anthropic.InputJSONDelta:
					if acc, ok := toolUseMap[e.Index]; ok {
						acc.inputJSON += d.PartialJSON
					}
				}
			case anthropic.MessageStopEvent:
				var tcs []message.ToolCall
				for _, acc := range toolUseMap {
					if acc.name != "" {
						tcs = append(tcs, message.ToolCall{
							ID:        acc.id,
							Name:      acc.name,
							Arguments: json.RawMessage(acc.inputJSON),
						})
					}
				}
				ch <- provider.StreamChunk{
					Content:   contentBuilder,
					ToolCalls: tcs,
					IsFinal:   true,
				}
				return
			}
		}
		ch <- provider.StreamChunk{
			Content: contentBuilder,
			IsFinal: true,
		}
	}()

	return ch, nil
}

// Translator returns the Anthropic message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

type toolUseAccumulator struct {
	id        string
	name      string
	inputJSON string
}

// Translator converts between universal messages and Anthropic format.
type Translator struct {
	model       string
	maxTokens   int
	temperature float64
}

// ToNative converts universal messages to Anthropic format.
// The system prompt is injected into the top-level System field, NOT
// into the messages array. System messages from hooks also go into System.
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	// Build system prompt blocks (TextBlockParam for the System field)
	var systemBlocks []anthropic.TextBlockParam
	if systemPrompt != "" {
		systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: systemPrompt, Type: "text"})
	}

	// Build messages
	var antMessages []anthropic.MessageParam
	for _, msg := range messages {
		switch msg.Type {
		case message.MessageSystem:
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: msg.Content, Type: "text"})
		case message.MessageUser:
			antMessages = append(antMessages, anthropic.NewUserMessage(
				anthropic.NewTextBlock(msg.Content),
			))
		case message.MessageAssistant:
			if len(msg.ToolCalls) > 0 {
				var blocks []anthropic.ContentBlockParamUnion
				if msg.Content != "" {
					blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
				}
				for _, tc := range msg.ToolCalls {
					blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, tc.Arguments, tc.Name))
				}
				antMessages = append(antMessages, anthropic.NewAssistantMessage(blocks...))
			} else {
				antMessages = append(antMessages, anthropic.NewAssistantMessage(
					anthropic.NewTextBlock(msg.Content),
				))
			}
		case message.MessageTool:
			antMessages = append(antMessages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolID, msg.Content, false),
			))
		}
	}

	// Build tools
	var antTools []anthropic.ToolUnionParam
	if len(tools) > 0 {
		antTools = make([]anthropic.ToolUnionParam, len(tools))
		for i, tl := range tools {
			antTools[i] = anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        tl.Name(),
					Description: anthropic.String(tl.Description()),
					InputSchema: anthropic.ToolInputSchemaParam{
						Properties: tl.SchemaMap(),
					},
				},
			}
		}
	}

	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(t.model),
		MaxTokens:   int64(t.maxTokens),
		Temperature: anthropic.Float(t.temperature),
		System:      systemBlocks,
		Messages:    antMessages,
	}
	if len(antTools) > 0 {
		params.Tools = antTools
	}

	return params
}

// FromNative converts an Anthropic Message response to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	msg, ok := response.(*anthropic.Message)
	if !ok {
		return nil, fmt.Errorf("expected *anthropic.Message, got %T", response)
	}

	result := &provider.LLMResponse{
		Usage: provider.Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
			CachedTokens: int(msg.Usage.CacheReadInputTokens),
		},
	}

	var content string
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			content += block.Text
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, message.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}

	result.Content = content

	if msg.StopReason == "end_turn" && len(result.ToolCalls) == 0 {
		result.IsFinal = true
	}

	return result, nil
}

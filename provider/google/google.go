// Package google implements the Google GenAI LLMProvider using the official
// genai SDK (google.golang.org/genai).
//
// It supports Google-specific features: system instruction via Content,
// context caching, and function declarations for tool use.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	genai "google.golang.org/genai"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// Provider implements provider.LLMProvider for Google GenAI.
type Provider struct {
	model       string
	client      *genai.Client
	config      *provider.CacheConfig
	translator  *Translator
	maxTokens   int
	temperature float64
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

// WithMaxTokens sets the default max output tokens.
func WithMaxTokens(n int) Option {
	return func(p *Provider) { p.maxTokens = n }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(p *Provider) { p.temperature = t }
}

// NewProvider creates a Google GenAI provider.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:       "gemini-2.5-pro",
		config:      &provider.CacheConfig{Strategy: provider.CacheAuto},
		maxTokens:   4096,
		temperature: 0.7,
	}
	for _, opt := range opts {
		opt(p)
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		panic(fmt.Sprintf("google genai client: %v", err))
	}
	p.client = client

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
	greq := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(*genaiRequest)
	contents, config := greq.Contents, greq.Config

	if req.Model == "" {
		req.Model = p.model
	}
	if req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.MaxTokens)
	}
	if req.Temperature != 0 {
		t := float32(req.Temperature)
		config.Temperature = &t
	}

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("google generate: %w", err)
	}

	return p.translator.FromNative(resp)
}

// ChatStream sends a streaming request.
func (p *Provider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	greq := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(*genaiRequest)
	contents, config := greq.Contents, greq.Config

	model := req.Model
	if model == "" {
		model = p.model
	}
	if req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.MaxTokens)
	}
	if req.Temperature != 0 {
		t := float32(req.Temperature)
		config.Temperature = &t
	}

	seq := p.client.Models.GenerateContentStream(ctx, model, contents, config)
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		processStream(seq, ch)
	}()

	return ch, nil
}

// Translator returns the Google message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

// processStream consumes the GenAI stream iterator and emits StreamChunks.
func processStream(seq iter.Seq2[*genai.GenerateContentResponse, error], ch chan<- provider.StreamChunk) {
	var contentBuilder string

	for resp, err := range seq {
		if err != nil {
			ch <- provider.StreamChunk{Error: err}
			return
		}
		if resp == nil || len(resp.Candidates) == 0 {
			continue
		}
		candidate := resp.Candidates[0]
		if candidate.Content == nil {
			continue
		}

		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				contentBuilder += part.Text
				ch <- provider.StreamChunk{Content: part.Text}
			}
			if part.FunctionCall != nil {
				// Accumulated tool call — emit at end with full content
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				ch <- provider.StreamChunk{
					Content: contentBuilder,
					ToolCalls: []message.ToolCall{{
						ID:        part.FunctionCall.ID,
						Name:      part.FunctionCall.Name,
						Arguments: argsJSON,
					}},
				}
			}
		}

		// Check for usage in last response
		if resp.UsageMetadata != nil {
			ch <- provider.StreamChunk{
				Content: contentBuilder,
				IsFinal: candidate.FinishReason != "" &&
					candidate.FinishReason != "STOP" || candidate.FinishReason == "STOP",
				Usage: &provider.Usage{
					InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
					OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
					CachedTokens: int(resp.UsageMetadata.CachedContentTokenCount),
				},
			}
			return
		}
	}

	// Final fallback
	ch <- provider.StreamChunk{
		Content: contentBuilder,
		IsFinal: true,
	}
}

// Translator converts between universal messages and Google GenAI format.
type Translator struct {
	model       string
	maxTokens   int
	temperature float64
}

// ToNative converts universal messages to Google GenAI Contents and Config.
// Returns a genaiRequest wrapper to carry both contents and config via any.
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	contents, config := t.buildRequest(systemPrompt, messages, tools)
	return &genaiRequest{Contents: contents, Config: config}
}

// genaiRequest bundles contents and config for the GenAI API.
type genaiRequest struct {
	Contents []*genai.Content
	Config   *genai.GenerateContentConfig
}

// buildRequest builds the GenAI contents and config from universal messages.
func (t *Translator) buildRequest(systemPrompt string, messages []message.Message, tools []*tool.Tool) ([]*genai.Content, *genai.GenerateContentConfig) {
	var contents []*genai.Content

	// Convert history messages to Contents
	for _, msg := range messages {
		var content *genai.Content
		switch msg.Type {
		case message.MessageSystem:
			// System messages from hooks are ignored here — they go in SystemInstruction
			continue
		case message.MessageUser:
			content = &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{{Text: msg.Content}},
			}
		case message.MessageAssistant:
			var parts []*genai.Part
			if msg.Content != "" {
				parts = append(parts, &genai.Part{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				// Tool calls from assistant → FunctionCall parts
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: parseJSONToMap(tc.Arguments),
					},
				})
			}
			content = &genai.Content{
				Role:  "model",
				Parts: parts,
			}
		case message.MessageTool:
			content = &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						Name: msg.Name,
						Response: map[string]any{
							"content": msg.Content,
						},
					},
				}},
			}
		}
		if content != nil {
			contents = append(contents, content)
		}
	}

	// Build GenerateContentConfig
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(t.maxTokens),
	}

	if t.temperature > 0 {
		temp := float32(t.temperature)
		config.Temperature = &temp
	}

	// System instruction
	if systemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		}
	}

	// Tools as FunctionDeclarations
	if len(tools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, len(tools))
		for i, tl := range tools {
			declarations[i] = &genai.FunctionDeclaration{
				Name:        tl.Name(),
				Description: tl.Description(),
				Parameters:  convertSchema(tl.SchemaMap()),
			}
		}
		config.Tools = []*genai.Tool{{
			FunctionDeclarations: declarations,
		}}
	}

	return contents, config
}

// FromNative converts a GenAI GenerateContentResponse to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	resp, ok := response.(*genai.GenerateContentResponse)
	if !ok {
		return nil, fmt.Errorf("expected *genai.GenerateContentResponse, got %T", response)
	}

	result := &provider.LLMResponse{}

	if resp.UsageMetadata != nil {
		result.Usage = provider.Usage{
			InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			CachedTokens: int(resp.UsageMetadata.CachedContentTokenCount),
		}
	}

	if len(resp.Candidates) == 0 {
		return result, nil
	}

	candidate := resp.Candidates[0]
	if candidate.Content == nil {
		return result, nil
	}

	var content string
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			content += part.Text
		}
		if part.FunctionCall != nil {
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			result.ToolCalls = append(result.ToolCalls, message.ToolCall{
				ID:        part.FunctionCall.ID,
				Name:      part.FunctionCall.Name,
				Arguments: argsJSON,
			})
		}
	}

	result.Content = content

	if candidate.FinishReason == genai.FinishReasonStop && len(result.ToolCalls) == 0 {
		result.IsFinal = true
	}

	return result, nil
}

// parseJSONToMap parses raw JSON message arguments into a map.
func parseJSONToMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_raw": string(raw)}
	}
	return m
}

// convertSchema converts our internal JSON schema (map[string]any) to
// the GenAI Schema type for function declaration parameters.
func convertSchema(m map[string]any) *genai.Schema {
	if m == nil {
		return nil
	}

	s := &genai.Schema{}

	if t, ok := m["type"].(string); ok {
		s.Type = genai.Type(t)
	}
	if d, ok := m["description"].(string); ok {
		s.Description = d
	}
	if e, ok := m["enum"]; ok {
		switch enumVals := e.(type) {
		case []interface{}:
			for _, v := range enumVals {
				if str, ok := v.(string); ok {
					s.Enum = append(s.Enum, str)
				}
			}
		case []string:
			s.Enum = enumVals
		}
	}
	if req, ok := m["required"]; ok {
		switch r := req.(type) {
		case []interface{}:
			for _, v := range r {
				if str, ok := v.(string); ok {
					s.Required = append(s.Required, str)
				}
			}
		case []string:
			s.Required = r
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema)
		for name, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				s.Properties[name] = convertSchema(propMap)
			}
		}
	}

	return s
}

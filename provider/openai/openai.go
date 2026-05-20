// Package openai implements the OpenAI LLMProvider using the official
// openai-go SDK (github.com/openai/openai-go).
//
// The translator converts universal messages to OpenAI's chat completion
// format. The system prompt is injected as the first message in each request,
// not stored in History.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

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
	apiKeys  []string
	keyIndex int

	// baseURL points at an OpenAI-compatible endpoint (LM Studio, OpenRouter,
	// Ollama, vLLM…). Empty means the SDK default — api.openai.com.
	baseURL string

	// reasoningEffort is the default reasoning_effort for o-series / gpt-5.
	// Per-request ReasoningConfig overrides this.
	reasoningEffort shared.ReasoningEffort
	// includeReasoning is the default for "surface reasoning deltas in
	// StreamChunk.Reasoning". Per-request overrides win.
	includeReasoning bool
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

// WithBaseURL sets a custom base URL (for OpenRouter, Ollama, LM Studio, vLLM…).
// The client is built once in NewProvider after every option has run, so this
// just stores the URL — earlier we built the client here and the default
// constructor at the end of NewProvider clobbered it.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.baseURL = url }
}

// WithAPIKeys sets multiple API keys for round-robin rotation.
func WithAPIKeys(keys ...string) Option {
	return func(p *Provider) {
		p.apiKeys = keys
	}
}

// WithReasoningEffort sets the default reasoning_effort for o-series and
// gpt-5 models. Maps from provider.ReasoningEffort to the SDK's enum.
// Non-reasoning models silently ignore this.
func WithReasoningEffort(e provider.ReasoningEffort) Option {
	return func(p *Provider) { p.reasoningEffort = toSDKEffort(e) }
}

// WithIncludeReasoning controls whether reasoning_content deltas from
// LM Studio / DeepSeek-R1 / Qwen3 / etc. (and any future first-party
// surface) are emitted on StreamChunk.Reasoning. When false (default)
// reasoning is discarded so the loop sees only the model's final text.
func WithIncludeReasoning(b bool) Option {
	return func(p *Provider) { p.includeReasoning = b }
}

// toSDKEffort maps our provider-neutral enum onto the openai-go SDK's
// shared.ReasoningEffort. Unknown values become the zero value (omitted).
// "minimal" is gpt-5 only — we send it through; older models will 400.
func toSDKEffort(e provider.ReasoningEffort) shared.ReasoningEffort {
	switch e {
	case provider.ReasoningEffortLow:
		return shared.ReasoningEffortLow
	case provider.ReasoningEffortMedium:
		return shared.ReasoningEffortMedium
	case provider.ReasoningEffortHigh:
		return shared.ReasoningEffortHigh
	case provider.ReasoningEffortMinimal:
		return shared.ReasoningEffort("minimal")
	}
	return ""
}

// NewProvider creates an OpenAI provider with the given API key.
func NewProvider(apiKey string, opts ...Option) *Provider {
	cfg := provider.DefaultCacheConfig()
	p := &Provider{
		model:  "gpt-4o",
		config: &cfg,
		// No explicit cap by default. applyMaxTokens skips the param
		// entirely when n <= 0, so OpenAI's per-model completion
		// ceiling applies (e.g. 128k for gpt-5-mini). Callers that
		// need a smaller budget use WithMaxTokens. The previous 4096
		// default silently truncated tool_call arguments on reasoning
		// families; a 200k default was rejected by gpt-5-mini's actual
		// 128k cap — neither extreme is right, so the framework now
		// stays out of the way.
		maxTokens:   0,
		temperature: 0.7,
	}

	for _, opt := range opts {
		opt(p)
	}

	if len(p.apiKeys) == 0 {
		p.apiKeys = []string{apiKey}
	}

	clientOpts := []option.RequestOption{option.WithAPIKey(p.apiKeys[0])}
	if p.baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(p.baseURL))
	}
	p.client = openai.NewClient(clientOpts...)

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

	// Resolve the effective model BEFORE applying request-level config:
	// applyMaxTokens routes by model family (o-series / gpt-5 need
	// max_completion_tokens; legacy chat models keep max_tokens), so it
	// must see the final model name, not the translator's default.
	model := req.Model
	if model == "" {
		model = p.model
	}
	params.Model = shared.ChatModel(model)

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}
	applyMaxTokens(&params, model, maxTokens)

	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if eff := p.resolveEffort(req.Reasoning); eff != "" {
		params.ReasoningEffort = eff
	}
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}
	if rf, err := buildResponseFormatParams(req.ResponseSchema, req.ResponseSchemaName, req.ResponseFormatMode); err != nil {
		return nil, fmt.Errorf("openai response_format: %w", err)
	} else if rf != nil {
		params.ResponseFormat = *rf
	}
	if len(req.ExtraParams) > 0 {
		params.SetExtraFields(req.ExtraParams)
	}

	chat, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat: %w", err)
	}

	resp, err := p.translator.FromNative(chat)
	if err != nil {
		return nil, fmt.Errorf("openai translate response: %w", err)
	}
	// reasoning_content is non-standard but emitted by LM Studio / DeepSeek /
	// Qwen3 — peek into the raw JSON of the first choice's message.
	if p.shouldIncludeReasoning(req.Reasoning) && len(chat.Choices) > 0 {
		if r := extractReasoningField(chat.Choices[0].Message.RawJSON()); r != "" {
			resp.Reasoning = r
		}
	}
	return resp, nil
}

// resolveEffort picks the effort to send: per-request if set, else the
// provider default. Returns "" to omit the field entirely.
func (p *Provider) resolveEffort(rc *provider.ReasoningConfig) shared.ReasoningEffort {
	if rc != nil && rc.Effort != provider.ReasoningEffortNone {
		return toSDKEffort(rc.Effort)
	}
	return p.reasoningEffort
}

// shouldIncludeReasoning is true when the caller (per-request or provider
// default) asked for reasoning traces in the output.
func (p *Provider) shouldIncludeReasoning(rc *provider.ReasoningConfig) bool {
	if rc != nil {
		return rc.IncludeInOutput
	}
	return p.includeReasoning
}

// ChatStream sends a streaming chat completion request.
func (p *Provider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	params := p.translator.ToNative(req.SystemPrompt, req.Messages, req.Tools).(openai.ChatCompletionNewParams)

	model := req.Model
	if model == "" {
		model = p.model
	}
	params.Model = shared.ChatModel(model)

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}
	applyMaxTokens(&params, model, maxTokens)

	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if eff := p.resolveEffort(req.Reasoning); eff != "" {
		params.ReasoningEffort = eff
	}
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}
	if rf, err := buildResponseFormatParams(req.ResponseSchema, req.ResponseSchemaName, req.ResponseFormatMode); err != nil {
		return nil, fmt.Errorf("openai response_format: %w", err)
	} else if rf != nil {
		params.ResponseFormat = *rf
	}
	if len(req.ExtraParams) > 0 {
		params.SetExtraFields(req.ExtraParams)
	}
	// Request token usage in the streaming response — OpenAI omits it by default.
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	includeReasoning := p.shouldIncludeReasoning(req.Reasoning)
	harmony := newHarmonyFilter(includeReasoning)
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		var toolCallMap map[int]*toolCallAccumulator
		var contentBuilder string
		// With stream_options.include_usage=true, OpenAI emits an extra chunk
		// after finish_reason whose Choices slice is empty and whose Usage
		// is populated. We must drain the stream until completion to capture
		// it, rather than returning at finish_reason.
		var finalChunk *openai.ChatCompletionChunk

		for stream.Next() {
			chunk := stream.Current()

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta

				// reasoning_content is a non-standard delta field used by
				// LM Studio, DeepSeek-R1, Qwen3, etc. We read it off the
				// raw JSON since the SDK schema doesn't expose it.
				if r := extractReasoningField(delta.RawJSON()); r != "" {
					if includeReasoning {
						ch <- provider.StreamChunk{Reasoning: r}
					}
					// Either way: skip the content-arm — many local
					// models repeat the same text in both fields.
				}

				// Text content delta — pipe through the Harmony filter,
				// which routes <|channel|>analysis fragments to reasoning
				// and surfaces only the final-channel text as content.
				if delta.Content != "" {
					cText, rText := harmony.feed(delta.Content)
					if cText != "" {
						contentBuilder += cText
						ch <- provider.StreamChunk{Content: cText}
					}
					if rText != "" && includeReasoning {
						ch <- provider.StreamChunk{Reasoning: rText}
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

				if chunk.Choices[0].FinishReason != "" {
					c := chunk
					finalChunk = &c
				}
			}

			// Usage-only chunk arrives after finish_reason; merge it.
			if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 {
				if finalChunk == nil {
					c := chunk
					finalChunk = &c
				} else {
					finalChunk.Usage = chunk.Usage
				}
			}
		}

		final := buildFinalChunk(contentBuilder, toolCallMap, finalChunk)
		// Surface stream errors so callers see HTTP 4xx/5xx, malformed SSE,
		// connection drops, etc. Without this the agent silently sees an
		// empty final chunk and looks "successful but mute."
		if err := stream.Err(); err != nil {
			final.Error = fmt.Errorf("openai stream: %w", err)
		}
		ch <- final
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
			// OpenAI carries prompt_tokens_details on the final usage chunk
			// when stream_options.include_usage is set. Omitting it here
			// was the silent cost-tracking bug: cache hits read as zero,
			// so InputUSD was billed at full rate and SavingsUSD stayed 0.
			CachedTokens: int(chunk.Usage.PromptTokensDetails.CachedTokens),
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
			openaiMessages = append(openaiMessages, buildUserMessage(msg))
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
	// Token cap is set by Chat / ChatStream, not here — they know the
	// effective model after request-level overrides and route to either
	// MaxTokens or MaxCompletionTokens accordingly. Doing it in the
	// translator would re-introduce the v0.0.2 bug where gpt-5 / o-series
	// models received the legacy max_tokens field and got rejected by
	// the API.
	if len(openaiTools) > 0 {
		params.Tools = openaiTools
	}

	return params
}

// buildUserMessage translates a universal user message into an OpenAI
// ChatCompletionMessageParamUnion. Pure-text messages take the legacy
// fast path (plain string content) so we don't regress the wire shape
// users have relied on for billing and trace expectations. Multi-modal
// messages (any non-text Part) emit OpenAI's content array form, with
// inline images rendered as base64 data URLs.
func buildUserMessage(msg message.Message) openai.ChatCompletionMessageParamUnion {
	if isPureText(msg) {
		return openai.UserMessage(textOf(msg))
	}
	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case message.PartText:
			if p.Text != "" {
				parts = append(parts, openai.TextContentPart(p.Text))
			}
		case message.PartImageURL:
			parts = append(parts, openai.ImageContentPart(
				openai.ChatCompletionContentPartImageImageURLParam{URL: p.URL},
			))
		case message.PartImage:
			dataURL := "data:" + p.MimeType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			parts = append(parts, openai.ImageContentPart(
				openai.ChatCompletionContentPartImageImageURLParam{URL: dataURL},
			))
		// PartFile / PartAudio fall through — only newer chat models accept
		// them and the SDK requires extra plumbing; skip silently for now
		// instead of refusing the whole message.
		}
	}
	return openai.UserMessage(parts)
}

// isPureText returns true when a message has no Parts (legacy) or every
// Part is a text Part. Used as the fast-path gate in buildUserMessage.
func isPureText(msg message.Message) bool {
	if len(msg.Parts) == 0 {
		return true
	}
	for _, p := range msg.Parts {
		if p.Type != message.PartText {
			return false
		}
	}
	return true
}

// textOf returns the textual content of a message. Prefers Parts if present
// (multi-modal builds set both Content and Parts) and falls back to the
// legacy Content field for messages built without Parts.
func textOf(msg message.Message) string {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	if msg.Content != "" {
		return msg.Content
	}
	// Last resort: concatenate text parts.
	var b strings.Builder
	for _, p := range msg.Parts {
		if p.Type == message.PartText {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
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

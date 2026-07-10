// Package anthropic implements the Anthropic LLMProvider using the official
// anthropic-sdk-go library.
//
// It supports Anthropic-specific features: top-level System prompt field,
// cache_control breakpoints for prompt caching, and tool use via content blocks.
package anthropic

import (
	"context"
	"encoding/base64"
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

	// providerID is the label stamped on every LLMResponse / StreamChunk.
	// Defaults to "anthropic"; override with WithProviderID to distinguish
	// e.g. a Bedrock-fronted Claude or an AWS-proxied endpoint from the
	// public api.anthropic.com one in the trace UI / cost tables.
	providerID string

	// baseURL overrides the SDK's default api.anthropic.com endpoint. Empty
	// means "use the SDK default". Set via WithBaseURL — see that option for
	// the rationale (testability + gateways).
	baseURL string

	// Default thinking config. Applied when LLMRequest.Reasoning is nil.
	// budgetTokens=0 means "no extended thinking by default".
	defaultBudgetTokens int
	includeReasoning    bool

	// cacheBreakpoints is the set of named places to insert ephemeral
	// cache_control markers. Empty map = no caching (legacy default).
	cacheBreakpoints map[string]bool

	// keySuffix is the "****xxxx" surface of the API key passed to
	// NewProvider, cached so Chat / ChatStream can stamp it on every
	// response and chunk without rederiving it per call. The raw key
	// is intentionally NOT stored — the SDK already owns it and we
	// only need the safe suffix for the trace UI.
	keySuffix string
}

// Option configures an Anthropic Provider.
type Option func(*Provider)

// WithModel sets the model name.
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

const (
	// CacheSystemPrompt marks the last system text block with
	// cache_control:{type:"ephemeral"}. The prefix up to and including the
	// system prompt becomes cacheable on subsequent identical calls.
	CacheSystemPrompt = "system"

	// CacheTools marks the LAST tool definition. The prefix up to and
	// including the tool list becomes cacheable — combine with
	// CacheSystemPrompt to make both reusable.
	CacheTools = "tools"
)

// WithCacheBreakpoints enables cache_control markers at the named
// breakpoints. Pass CacheSystemPrompt and / or CacheTools.
//
// Anthropic caches the prefix UP TO each marker, so the order is:
// system → tools → messages. Marking "system" alone caches just the
// system prompt; marking "tools" alone caches system + tools; marking
// both produces two markers and lets the API report the larger cache hit.
//
// The framework reads CachedTokens off every response so cost / hit-rate
// telemetry works automatically.
func WithCacheBreakpoints(breakpoints ...string) Option {
	return func(p *Provider) {
		if p.cacheBreakpoints == nil {
			p.cacheBreakpoints = make(map[string]bool, len(breakpoints))
		}
		for _, b := range breakpoints {
			p.cacheBreakpoints[b] = true
		}
	}
}

// WithMaxTokens sets the default max tokens.
func WithMaxTokens(n int) Option {
	return func(p *Provider) { p.maxTokens = n }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(p *Provider) { p.temperature = t }
}

// WithThinkingBudget enables Anthropic extended thinking with the given
// token budget. Must be ≥1024 and < MaxTokens per the API contract; values
// below 1024 are clamped to 1024 to avoid a 400.
func WithThinkingBudget(budgetTokens int) Option {
	return func(p *Provider) {
		if budgetTokens > 0 && budgetTokens < 1024 {
			budgetTokens = 1024
		}
		p.defaultBudgetTokens = budgetTokens
	}
}

// WithIncludeReasoning controls whether thinking blocks are surfaced on
// StreamChunk.Reasoning / LLMResponse.Reasoning. When false the deltas
// are still consumed (Anthropic always sends them when thinking is on)
// but discarded before they reach the loop.
func WithIncludeReasoning(b bool) Option {
	return func(p *Provider) { p.includeReasoning = b }
}

// WithProviderID overrides the provider-id label stamped on every response
// and chunk. The default is "anthropic". Useful when proxying Claude via
// AWS Bedrock, GCP Vertex, or an in-house gateway and you want telemetry
// to attribute the call to the gateway rather than collapsing everything
// into "anthropic".
func WithProviderID(id string) Option {
	return func(p *Provider) {
		if id != "" {
			p.providerID = id
		}
	}
}

// WithBaseURL points the SDK client at an alternate endpoint instead of the
// default api.anthropic.com. Two reasons this exists:
//
//   - Testability: tests can spin up an httptest.Server that serves canned
//     SSE / JSON and aim the provider at it, exercising the real SDK decode
//     path without a network call or API key.
//   - Gateways: an in-house proxy, LiteLLM, or a corporate egress gateway that
//     speaks the Anthropic wire format can be targeted without forking the
//     provider. (Pair with WithProviderID to relabel the telemetry.)
//
// An empty url is ignored so callers can pass a config value through
// unconditionally without clobbering the SDK default.
func WithBaseURL(url string) Option {
	return func(p *Provider) {
		if url != "" {
			p.baseURL = url
		}
	}
}

// effortToBudget translates a tiered effort into an Anthropic budget.
// Numbers are conservative defaults; the user can always override with
// BudgetTokens directly.
func effortToBudget(e provider.ReasoningEffort) int {
	switch e {
	case provider.ReasoningEffortLow, provider.ReasoningEffortMinimal:
		return 1024
	case provider.ReasoningEffortMedium:
		return 4096
	case provider.ReasoningEffortHigh:
		return 16384
	}
	return 0
}

// resolveBudget picks the effective thinking budget for this call.
func (p *Provider) resolveBudget(rc *provider.ReasoningConfig) int {
	if rc == nil {
		return p.defaultBudgetTokens
	}
	if rc.BudgetTokens > 0 {
		b := rc.BudgetTokens
		if b < 1024 {
			b = 1024
		}
		return b
	}
	return effortToBudget(rc.Effort)
}

func (p *Provider) shouldIncludeReasoning(rc *provider.ReasoningConfig) bool {
	if rc != nil {
		return rc.IncludeInOutput
	}
	return p.includeReasoning
}

// NewProvider creates an Anthropic provider.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:      anthropic.ModelClaudeSonnet4_20250514,
		providerID: "anthropic",
		config:     &provider.CacheConfig{Strategy: provider.CacheAuto},
		// Anthropic REQUIRES max_tokens on every request — omitting it
		// returns a 400 from the API — so we cannot default to 0 like
		// the OpenAI provider does. 16384 is the historical Anthropic
		// default and fits the standard 8k assistant turn plus a margin
		// for tool-call arguments. Callers with large structured tool
		// payloads (e.g. lanbu's generate_prd) should override via
		// WithMaxTokens up to the model's actual ceiling — 64k for
		// Claude Sonnet 4, 128k for Sonnet 4.5 with the beta header.
		maxTokens:   16384,
		temperature: 1.0,
	}
	for _, opt := range opts {
		opt(p)
	}
	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if p.baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(p.baseURL))
	}
	p.client = anthropic.NewClient(clientOpts...)
	p.keySuffix = provider.APIKeySuffix(apiKey)
	p.translator = &Translator{
		model:            p.model,
		maxTokens:        p.maxTokens,
		temperature:      p.temperature,
		cacheBreakpoints: p.cacheBreakpoints,
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
	if budget := p.resolveBudget(req.Reasoning); budget > 0 {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(budget))
	}
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	out, err := p.translator.FromNative(resp)
	if err != nil {
		return nil, err
	}
	if p.shouldIncludeReasoning(req.Reasoning) {
		out.Reasoning = extractThinking(resp)
	}
	out.ProviderID = p.providerID
	out.ModelID = string(params.Model)
	out.APIKeySuffix = p.keySuffix
	return out, nil
}

// extractThinking concatenates every "thinking" content block in the
// response. Redacted thinking blocks are skipped because their payload
// is opaque; the API expects them to be sent back verbatim, not displayed.
func extractThinking(msg *anthropic.Message) string {
	var b string
	for _, block := range msg.Content {
		if block.Type == "thinking" {
			b += block.Thinking
		}
	}
	return b
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
	if budget := p.resolveBudget(req.Reasoning); budget > 0 {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(budget))
	}
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}

	includeReasoning := p.shouldIncludeReasoning(req.Reasoning)
	stream := p.client.Messages.NewStreaming(ctx, params)
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		var (
			contentBuilder string
			toolUseMap     = make(map[int64]*toolUseAccumulator)
			// usage accumulates across the stream: message_start carries the
			// prompt-side buckets, each message_delta the cumulative output
			// count. Kept as a value so a mid-stream failure still bills
			// whatever the API reported before dying.
			usage    provider.Usage
			sawUsage bool
		)

		// finalUsage normalises to the provider.Usage contract: Anthropic
		// reports input / cache_read / cache_creation disjointly, so the
		// inclusive InputTokens total is their sum.
		finalUsage := func() *provider.Usage {
			if !sawUsage {
				return nil
			}
			u := usage
			u.InputTokens += u.CachedTokens + u.CacheWriteTokens
			return &u
		}

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsAny().(type) {
			case anthropic.MessageStartEvent:
				sawUsage = true
				usage.InputTokens = int(e.Message.Usage.InputTokens)
				usage.CachedTokens = int(e.Message.Usage.CacheReadInputTokens)
				usage.CacheWriteTokens = int(e.Message.Usage.CacheCreationInputTokens)
				usage.OutputTokens = int(e.Message.Usage.OutputTokens)
			case anthropic.MessageDeltaEvent:
				// Cumulative totals — overwrite, never add.
				sawUsage = true
				usage.OutputTokens = int(e.Usage.OutputTokens)
				if e.Usage.InputTokens > 0 {
					usage.InputTokens = int(e.Usage.InputTokens)
				}
				if e.Usage.CacheReadInputTokens > 0 {
					usage.CachedTokens = int(e.Usage.CacheReadInputTokens)
				}
				if e.Usage.CacheCreationInputTokens > 0 {
					usage.CacheWriteTokens = int(e.Usage.CacheCreationInputTokens)
				}
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
					ch <- provider.StreamChunk{Content: d.Text, ProviderID: p.providerID, ModelID: string(params.Model), APIKeySuffix: p.keySuffix}
				case anthropic.ThinkingDelta:
					if includeReasoning {
						ch <- provider.StreamChunk{Reasoning: d.Thinking, ProviderID: p.providerID, ModelID: string(params.Model), APIKeySuffix: p.keySuffix}
					}
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
					Content:      contentBuilder,
					ToolCalls:    tcs,
					IsFinal:      true,
					Usage:        finalUsage(),
					ProviderID:   p.providerID,
					ModelID:      string(params.Model),
					APIKeySuffix: p.keySuffix,
				}
				return
			}
		}
		// The iterator ended without a message_stop: either the stream
		// errored mid-flight or the connection closed early. Either way,
		// surface the error AND the partial usage so the tokens the API
		// already billed are not lost.
		final := provider.StreamChunk{
			Content:      contentBuilder,
			IsFinal:      true,
			Usage:        finalUsage(),
			ProviderID:   p.providerID,
			ModelID:      string(params.Model),
			APIKeySuffix: p.keySuffix,
		}
		if err := stream.Err(); err != nil {
			final.Error = fmt.Errorf("anthropic stream: %w", err)
		}
		ch <- final
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
	model            string
	maxTokens        int
	temperature      float64
	cacheBreakpoints map[string]bool
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
				buildUserBlocks(msg)...,
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

	// Apply cache_control breakpoints if configured. Anthropic caches the
	// prefix up to each marker, so the order is system → tools → messages.
	// We attach the marker to the LAST element in each section so the
	// cache covers the largest stable prefix. NewCacheControlEphemeralParam
	// sets Type="ephemeral" explicitly — a zero-value CacheControl gets
	// stripped by the SDK's omitzero serializer.
	if t.cacheBreakpoints[CacheSystemPrompt] && len(systemBlocks) > 0 {
		systemBlocks[len(systemBlocks)-1].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	if t.cacheBreakpoints[CacheTools] && len(antTools) > 0 {
		last := antTools[len(antTools)-1].OfTool
		if last != nil {
			last.CacheControl = anthropic.NewCacheControlEphemeralParam()
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

// buildUserBlocks maps a universal user message to Anthropic content blocks.
// Pure-text messages produce a single text block; multi-modal messages emit
// text + image blocks in order. Inline images use the Base64 source variant
// (mime-type-aware) and remote URLs use the URL source variant introduced
// in the 2024-10-22 SDK.
func buildUserBlocks(msg message.Message) []anthropic.ContentBlockParamUnion {
	if len(msg.Parts) == 0 {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(msg.Content)}
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case message.PartText:
			if p.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(p.Text))
			}
		case message.PartImageURL:
			blocks = append(blocks, anthropic.NewImageBlock(
				anthropic.URLImageSourceParam{URL: p.URL},
			))
		case message.PartImage:
			blocks = append(blocks, anthropic.NewImageBlockBase64(
				p.MimeType, base64.StdEncoding.EncodeToString(p.Data),
			))
		// PartFile / PartAudio: not supported by Anthropic content blocks
		// today; skip silently rather than reject the whole message.
		}
	}
	if len(blocks) == 0 {
		// Defensive: a message with only unsupported parts still needs
		// *something* on the wire so Anthropic doesn't 400 on empty content.
		blocks = append(blocks, anthropic.NewTextBlock(""))
	}
	return blocks
}

// FromNative converts an Anthropic Message response to universal format.
func (t *Translator) FromNative(response any) (*provider.LLMResponse, error) {
	msg, ok := response.(*anthropic.Message)
	if !ok {
		return nil, fmt.Errorf("expected *anthropic.Message, got %T", response)
	}

	// Anthropic reports input, cache-read, and cache-write token buckets
	// disjointly; the universal contract wants InputTokens as the inclusive
	// prompt total (see provider.Usage).
	result := &provider.LLMResponse{
		Usage: provider.Usage{
			InputTokens: int(msg.Usage.InputTokens +
				msg.Usage.CacheReadInputTokens +
				msg.Usage.CacheCreationInputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CachedTokens:     int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
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

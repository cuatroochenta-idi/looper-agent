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
	"github.com/anthropics/anthropic-sdk-go/packages/param"

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

	// effort is the default output_config.effort for models that accept it
	// (Opus 4.5+, Sonnet 4.6+). Empty means "derive from the thinking
	// budget / ReasoningConfig.Effort".
	effort anthropic.OutputConfigEffort

	// thinkingCompat pins the thinking wire format instead of deriving it
	// from the model id. Empty = auto-detect.
	thinkingCompat ThinkingCompat

	// requestOptions are extra SDK client options, applied after the
	// provider's own. This is the seam for alternate backends — Bedrock
	// SigV4, Vertex ADC — which authenticate with something other than an
	// Anthropic API key. See WithRequestOptions.
	requestOptions []option.RequestOption

	// samplingOverride force-enables (true) or force-disables (false)
	// temperature/top_p/top_k instead of deriving support from the model
	// id. Nil = auto-detect.
	samplingOverride *bool

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

// ThinkingCompat pins which thinking wire format the provider sends,
// bypassing model-id detection. Use it for gateways that proxy a model the
// capability table doesn't recognise, or to hold a model on the legacy
// shape during a migration.
type ThinkingCompat string

const (
	// ThinkingCompatAuto derives the format from the model id. Default.
	ThinkingCompatAuto ThinkingCompat = ""

	// ThinkingCompatBudget always sends {"type":"enabled","budget_tokens":N}.
	// Rejected with a 400 by Opus 4.7 and every model after it.
	ThinkingCompatBudget ThinkingCompat = "budget"

	// ThinkingCompatAdaptive always sends {"type":"adaptive"} and controls
	// depth with output_config.effort.
	ThinkingCompatAdaptive ThinkingCompat = "adaptive"

	// ThinkingCompatAlwaysOn never sends a thinking field. Required by
	// Fable 5 / Mythos, which 400 on any explicit thinking config.
	ThinkingCompatAlwaysOn ThinkingCompat = "always_on"
)

// WithThinkingCompat pins the thinking wire format. Leave unset to detect
// it from the model id, which is correct for every first-party Claude model.
func WithThinkingCompat(c ThinkingCompat) Option {
	return func(p *Provider) { p.thinkingCompat = c }
}

// WithSamplingParams force-enables or force-disables temperature / top_p /
// top_k. Leave unset to derive support from the model id: Opus 4.7 onward,
// Opus 5, Sonnet 5 and Fable 5 removed those parameters and reject a
// non-default value with a 400, so the provider drops them there.
func WithSamplingParams(enabled bool) Option {
	return func(p *Provider) { p.samplingOverride = &enabled }
}

// WithEffort sets the default output_config.effort for models that accept
// it. This is the replacement for a thinking token budget on Opus 4.7+ —
// it accepts the full Anthropic ladder, including the "xhigh" and "max"
// levels that the provider-neutral ReasoningEffort enum can't express.
//
// Per-request ReasoningConfig.Effort overrides this.
func WithEffort(e anthropic.OutputConfigEffort) Option {
	return func(p *Provider) { p.effort = e }
}

// WithRequestOptions passes extra options straight to the underlying SDK
// client. They are applied after the provider's own, so they win on
// conflict (notably the base URL).
//
// This is how you point the provider at a non-first-party backend. For
// Amazon Bedrock, pass an empty API key — auth is SigV4, and an empty
// x-api-key header would invalidate the signature:
//
//	cfg, _ := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
//	anthropic.NewProvider("",
//	    anthropic.WithRequestOptions(
//	        option.WithoutEnvironmentDefaults(),
//	        bedrock.WithConfig(cfg),
//	    ),
//	    anthropic.WithModel("anthropic.claude-opus-4-8"),
//	)
//
// option.WithoutEnvironmentDefaults is required on that path: without it
// the SDK resolves its own credentials first and fails with "no Anthropic
// credentials found" before the Bedrock middleware signs the request.
//
// Bedrock model ids carry an "anthropic." prefix; the provider strips it
// when resolving model capabilities, so thinking and sampling parameters
// are handled the same as for the first-party id.
func WithRequestOptions(opts ...option.RequestOption) Option {
	return func(p *Provider) { p.requestOptions = append(p.requestOptions, opts...) }
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

// budgetToEffort maps a thinking token budget onto the effort ladder. Used
// when a caller asked for a concrete budget but the target model dropped
// budget_tokens — the tier is the closest surviving expression of intent.
// It is the inverse of effortToBudget.
func budgetToEffort(b int) anthropic.OutputConfigEffort {
	switch {
	case b <= 0:
		return ""
	case b <= 1024:
		return anthropic.OutputConfigEffortLow
	case b <= 4096:
		return anthropic.OutputConfigEffortMedium
	default:
		return anthropic.OutputConfigEffortHigh
	}
}

// mapEffort translates the provider-neutral effort enum to Anthropic's.
// "minimal" is OpenAI-specific and collapses to "low" here.
func mapEffort(e provider.ReasoningEffort) anthropic.OutputConfigEffort {
	switch e {
	case provider.ReasoningEffortMinimal, provider.ReasoningEffortLow:
		return anthropic.OutputConfigEffortLow
	case provider.ReasoningEffortMedium:
		return anthropic.OutputConfigEffortMedium
	case provider.ReasoningEffortHigh:
		return anthropic.OutputConfigEffortHigh
	}
	return ""
}

// resolveEffort picks the effective effort level for this call. Explicit
// per-request effort wins, then a per-request budget mapped onto the
// ladder, then the provider-level WithEffort, then the provider-level
// thinking budget mapped onto the ladder.
func (p *Provider) resolveEffort(rc *provider.ReasoningConfig) anthropic.OutputConfigEffort {
	if rc != nil {
		if e := mapEffort(rc.Effort); e != "" {
			return e
		}
		if rc.BudgetTokens > 0 {
			return budgetToEffort(rc.BudgetTokens)
		}
	}
	if p.effort != "" {
		return p.effort
	}
	return budgetToEffort(p.defaultBudgetTokens)
}

// wantsThinking reports whether the caller asked for thinking at all. On
// adaptive models this gates whether the thinking field is sent; models
// that think by default (Opus 5, Fable 5) still think when it is omitted.
func (p *Provider) wantsThinking(rc *provider.ReasoningConfig) bool {
	if rc != nil {
		return rc.Effort != provider.ReasoningEffortNone || rc.BudgetTokens > 0
	}
	return p.defaultBudgetTokens > 0 || p.effort != ""
}

// resolveCaps returns the capabilities for a model, with provider-level
// overrides applied.
func (p *Provider) resolveCaps(model string) modelCaps {
	caps := lookupCaps(model)
	switch p.thinkingCompat {
	case ThinkingCompatBudget:
		caps.thinking = thinkingLegacyBudget
	case ThinkingCompatAdaptive:
		caps.thinking = thinkingAdaptive
	case ThinkingCompatAlwaysOn:
		caps.thinking = thinkingAlwaysOn
	}
	if p.samplingOverride != nil {
		caps.sampling = *p.samplingOverride
	}
	return caps
}

// applyModelParams reconciles the request with what the target model
// actually accepts: the right thinking shape, effort where supported, and
// sampling parameters stripped where they were removed from the API.
//
// Every branch here exists because the alternative is a 400, not a
// silently-ignored field.
func (p *Provider) applyModelParams(params *anthropic.MessageNewParams, rc *provider.ReasoningConfig) {
	caps := p.resolveCaps(string(params.Model))

	// Sampling params were removed on Opus 4.7+, Opus 5, Sonnet 5 and
	// Fable 5. The translator sets Temperature unconditionally, so clear
	// it (and its siblings) rather than letting the request fail.
	if !caps.sampling {
		params.Temperature = param.Opt[float64]{}
		params.TopP = param.Opt[float64]{}
		params.TopK = param.Opt[int64]{}
	}

	if caps.effort {
		if e := p.resolveEffort(rc); e != "" {
			params.OutputConfig.Effort = e
		}
	}

	// Forced tool use ("any" / "tool") is a 400 on Opus 5.5 and Fable /
	// Mythos 5.1. Fall back to auto: the model still sees every tool and
	// picks one when the prompt calls for it.
	if caps.rejectsForcedTools && (params.ToolChoice.OfAny != nil || params.ToolChoice.OfTool != nil) {
		params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	}

	switch caps.thinking {
	case thinkingAlwaysOn:
		// Thinking is always on and not configurable; any explicit
		// thinking field — including {"type":"disabled"} — is a 400.

	case thinkingAdaptive:
		if !p.wantsThinking(rc) {
			return
		}
		adaptive := anthropic.ThinkingConfigAdaptiveParam{}
		// Default display is "omitted" on 4.7+, which streams thinking
		// blocks with empty text. Ask for summaries when the caller
		// actually wants to surface reasoning.
		if p.shouldIncludeReasoning(rc) {
			adaptive.Display = anthropic.ThinkingConfigAdaptiveDisplaySummarized
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}

	case thinkingLegacyBudget:
		if budget := p.resolveBudget(rc); budget > 0 {
			params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(budget))
		}
	}
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
		// claude-sonnet-5 has no typed constant in this SDK version; the
		// Model type is a string alias, so the literal id is the supported
		// form. The previous default (Sonnet 4) is deprecated and retires
		// on 2026-06-15.
		model:      "claude-sonnet-5",
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
	var clientOpts []option.RequestOption
	// An empty key is legitimate when auth comes from somewhere else —
	// Bedrock SigV4, Vertex ADC, or a gateway that injects credentials.
	// Sending an empty x-api-key header would break SigV4 signing.
	if apiKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(apiKey))
	}
	if p.baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(p.baseURL))
	}
	// Caller-supplied options go last so they win over the defaults above —
	// bedrock.WithConfig, for instance, sets its own base URL.
	clientOpts = append(clientOpts, p.requestOptions...)
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
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}
	// Must run after Model / Temperature are final — it keys off the
	// resolved model id and strips params that model would reject.
	p.applyModelParams(&params, req.Reasoning)

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
	if tc := buildToolChoiceParams(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}
	// Must run after Model / Temperature are final — it keys off the
	// resolved model id and strips params that model would reject.
	p.applyModelParams(&params, req.Reasoning)

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
					InputSchema: buildInputSchema(tl.SchemaMap()),
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

	// A turn that yielded neither text nor a tool call is unusable, and the
	// loop would otherwise treat "no tool calls" as a successful final
	// answer and return an empty Output with status "completed" — the
	// failure surfaces far from its cause. The stop reason is the only
	// place that says why, and provider.LLMResponse has nowhere to carry
	// it, so report it as an error here.
	if content == "" && len(result.ToolCalls) == 0 {
		switch msg.StopReason {
		case "max_tokens":
			return nil, fmt.Errorf("anthropic: response hit max_tokens (provider configured %d) before "+
				"producing any text or tool call — with thinking enabled the budget covers thinking AND "+
				"the reply, so raise WithMaxTokens or lower the effort level", t.maxTokens)
		case "refusal":
			return nil, fmt.Errorf("anthropic: request declined by safety classifiers (stop_reason=refusal)")
		}
	}

	if msg.StopReason == "end_turn" && len(result.ToolCalls) == 0 {
		result.IsFinal = true
	}

	return result, nil
}

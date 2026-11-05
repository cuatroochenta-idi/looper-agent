// Package anthropic implements the Anthropic LLMProvider using the official
// anthropic-sdk-go library.
//
// It supports Anthropic-specific features: top-level System prompt field,
// cache_control breakpoints for prompt caching, and tool use via content blocks.
package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

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
	// cache_control:{type:"ephemeral"}. Tools render before the system
	// prompt, so the cached prefix covers tools + system.
	CacheSystemPrompt = "system"

	// CacheTools marks the LAST tool definition. The prefix up to and
	// including the tool list becomes cacheable.
	CacheTools = "tools"

	// CacheMessages caches the conversation: it marks the last block of
	// the last message on every request (a moving breakpoint), so the next
	// request reads the whole history from cache and pays full price only
	// for what was appended. When a single turn appends more blocks than
	// the API's 20-position lookback, it also marks the block the previous
	// request ended with, which that request cached. At most two markers,
	// so with CacheSystemPrompt and CacheTools the request stays within
	// the API's limit of four.
	CacheMessages = "messages"
)

// lookbackPositions is how many content positions the API walks back from
// a breakpoint to find an earlier cache entry. A run of consecutive tool_use
// blocks, or of tool_result blocks, counts as one position.
const lookbackPositions = 20

// maxCacheBreakpoints is the API's limit of cache_control markers per
// request.
const maxCacheBreakpoints = 4

// WithCacheBreakpoints enables cache_control markers at the named
// breakpoints. Pass CacheSystemPrompt, CacheTools and / or CacheMessages.
//
// Anthropic caches the prefix UP TO each marker, and the prefix renders in
// the order tools → system → messages. Marking "tools" caches the tool
// list; marking "system" caches tools + system; marking "messages" caches
// everything up to the end of the conversation. Combining them leaves an
// earlier read point that survives when a later part changes.
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

// WithIncludeReasoning controls whether the thinking text is surfaced on
// StreamChunk.Reasoning / LLMResponse.Reasoning. When false the deltas
// are still consumed (Anthropic always sends them when thinking is on)
// but not surfaced. Either way the signed thinking blocks travel on
// ThinkingBlocks so the next request can send them back.
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

// newParams translates a request into the API's parameters, reconciled with
// what the target model accepts.
func (p *Provider) newParams(req provider.LLMRequest) anthropic.MessageNewParams {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	params := p.translator.build(model, req.SystemPrompt, req.Messages, req.Tools)
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

	return params
}

// retryWithoutRejectedThinking reports whether err is the API refusing a
// replayed thinking block, and if so marks every thinking block of the
// request as rejected so the retry, and every later request of the same
// conversation, leaves them out.
//
// Claude 5 models bind a thinking block to the conversation that produced
// it: once the system prompt, the tool set or an earlier turn changes, the
// block fails the check and, on enforced accounts, the whole request is
// rejected with a 400 before any output. Dropping the blocks is the
// documented recovery; the model answers that turn without them.
func (p *Provider) retryWithoutRejectedThinking(err error, req provider.LLMRequest) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest ||
		!strings.Contains(apiErr.RawJSON(), rejectedThinkingError) {
		return false
	}

	return p.translator.rejected.addAll(req.Messages) > 0
}

// Chat sends a non-streaming request.
func (p *Provider) Chat(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	params := p.newParams(req)

	resp, err := p.client.Messages.New(ctx, params)
	if p.retryWithoutRejectedThinking(err, req) {
		params = p.newParams(req)
		resp, err = p.client.Messages.New(ctx, params)
	}
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
	params := p.newParams(req)

	includeReasoning := p.shouldIncludeReasoning(req.Reasoning)
	// NewStreaming sends the request before returning, so a 400 is already
	// on stream.Err() and nothing has been streamed yet.
	stream := p.client.Messages.NewStreaming(ctx, params)
	if p.retryWithoutRejectedThinking(stream.Err(), req) {
		_ = stream.Close()
		params = p.newParams(req)
		stream = p.client.Messages.NewStreaming(ctx, params)
	}
	ch := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(ch)
		var (
			contentBuilder string
			blocks         = streamBlocks{}
			stopReason     string
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
				if e.Delta.StopReason != "" {
					stopReason = string(e.Delta.StopReason)
				}
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
				blocks.start(e.Index, e.ContentBlock)
			case anthropic.ContentBlockDeltaEvent:
				switch d := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					contentBuilder += d.Text
					ch <- provider.StreamChunk{Content: d.Text, ProviderID: p.providerID, ModelID: string(params.Model), APIKeySuffix: p.keySuffix}
				case anthropic.ThinkingDelta:
					if b := blocks[e.Index]; b != nil {
						b.thinking += d.Thinking
					}
					if includeReasoning {
						ch <- provider.StreamChunk{Reasoning: d.Thinking, ProviderID: p.providerID, ModelID: string(params.Model), APIKeySuffix: p.keySuffix}
					}
				case anthropic.SignatureDelta:
					if b := blocks[e.Index]; b != nil {
						b.signature += d.Signature
					}
				case anthropic.InputJSONDelta:
					if b := blocks[e.Index]; b != nil {
						b.inputJSON += d.PartialJSON
					}
				}
			case anthropic.MessageStopEvent:
				thinking, tcs := blocks.finish(stopReason)
				ch <- provider.StreamChunk{
					Content:        contentBuilder,
					ToolCalls:      tcs,
					ThinkingBlocks: thinking,
					IsFinal:        true,
					Usage:          finalUsage(),
					ProviderID:     p.providerID,
					ModelID:        string(params.Model),
					APIKeySuffix:   p.keySuffix,
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

// emptyToolInput is the input of a tool call that takes no arguments.
const emptyToolInput = "{}"

// toolInput returns the call's arguments, or an empty object when they are
// missing or not valid JSON. Anthropic streams no input_json_delta for a tool
// without parameters, and a stream cut off mid-call leaves a fragment; either
// one cannot be serialised back into the next request and would fail every
// call after it. A cut-off call then reaches the tool as an empty one, which
// the tool's own validation rejects.
func toolInput(raw json.RawMessage) json.RawMessage {
	if !json.Valid(raw) {
		return json.RawMessage(emptyToolInput)
	}

	return raw
}

// cutOffByOutputLimit reports whether a tool call's arguments are incomplete
// because the response ran out of output tokens.
func cutOffByOutputLimit(stopReason string, raw json.RawMessage) bool {
	return stopReason == string(anthropic.StopReasonMaxTokens) && !json.Valid(raw)
}

// Content block types this provider reads and writes.
const (
	blockText             = "text"
	blockThinking         = "thinking"
	blockRedactedThinking = "redacted_thinking"
	blockToolUse          = "tool_use"
	blockToolResult       = "tool_result"
)

// thinkingFormat tags the thinking blocks this provider stores on assistant
// messages. Every adapter built on this translator (the Bedrock one too)
// replays them; any other adapter ignores them.
const thinkingFormat = "anthropic"

// rejectedThinkingError is the part of a 400 that says a replayed thinking
// block failed its signature check.
const rejectedThinkingError = "`signature` in `thinking` block"

// maxRejectedThinking bounds the memory of rejected blocks. Past it the set
// starts over: at worst one more request fails and is retried.
const maxRejectedThinking = 4096

// streamBlocks assembles the thinking and tool_use blocks of a streamed
// response by content index, so they come out in the order the model wrote
// them.
type streamBlocks map[int64]*streamBlock

type streamBlock struct {
	kind      string
	id        string
	name      string
	inputJSON string
	thinking  string
	signature string
	data      string
}

func (s streamBlocks) start(index int64, cb anthropic.ContentBlockStartEventContentBlockUnion) {
	switch cb.Type {
	case blockThinking, blockRedactedThinking, blockToolUse:
		s[index] = &streamBlock{
			kind:      cb.Type,
			id:        cb.ID,
			name:      cb.Name,
			thinking:  cb.Thinking,
			signature: cb.Signature,
			data:      cb.Data,
		}
	}
}

// finish returns the response's thinking blocks and tool calls, each in
// content order.
func (s streamBlocks) finish(stopReason string) ([]message.ThinkingBlock, []message.ToolCall) {
	var (
		thinking []message.ThinkingBlock
		calls    []message.ToolCall
	)
	for _, i := range slices.Sorted(maps.Keys(s)) {
		b := s[i]
		if b.kind != blockToolUse {
			thinking = append(thinking, message.ThinkingBlock{
				Provider:  thinkingFormat,
				Type:      b.kind,
				Thinking:  b.thinking,
				Signature: b.signature,
				Data:      b.data,
			})
			continue
		}
		if b.name == "" {
			continue
		}
		raw := json.RawMessage(b.inputJSON)
		calls = append(calls, message.ToolCall{
			ID:        b.id,
			Name:      b.name,
			Arguments: toolInput(raw),
			Truncated: cutOffByOutputLimit(stopReason, raw),
		})
	}
	return thinking, calls
}

// rejectedThinking remembers the thinking blocks the API refused to take
// back, so later requests of the same conversation leave them out instead
// of failing on them again. Every block of the rejected request is marked,
// and blocks produced later all come after them, so what is left out is
// always a leading run of the conversation's blocks, which keeps the rest
// valid.
type rejectedThinking struct {
	mu   sync.Mutex
	keys map[[sha256.Size]byte]struct{}
}

func rejectedKey(b message.ThinkingBlock) [sha256.Size]byte {
	return sha256.Sum256([]byte(b.Type + "\x00" + b.Signature + "\x00" + b.Data))
}

// addAll marks every thinking block of this provider in msgs and returns
// how many were not marked yet.
func (r *rejectedThinking) addAll(msgs []message.Message) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil || len(r.keys) > maxRejectedThinking {
		r.keys = make(map[[sha256.Size]byte]struct{})
	}
	added := 0
	for _, m := range msgs {
		for _, b := range m.ThinkingBlocks {
			if b.Provider != thinkingFormat {
				continue
			}
			k := rejectedKey(b)
			if _, ok := r.keys[k]; !ok {
				r.keys[k] = struct{}{}
				added++
			}
		}
	}
	return added
}

func (r *rejectedThinking) has(b message.ThinkingBlock) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.keys[rejectedKey(b)]
	return ok
}

// Translator converts between universal messages and Anthropic format.
type Translator struct {
	model            string
	maxTokens        int
	temperature      float64
	cacheBreakpoints map[string]bool
	rejected         rejectedThinking
}

// ToNative converts universal messages to Anthropic format for the
// translator's model. See build.
func (t *Translator) ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any {
	return t.build(t.model, systemPrompt, messages, tools)
}

// build converts universal messages to Anthropic format for model.
//
// The system prompt goes into the top-level System field, NOT into the
// messages array, and so do system messages that precede the first turn.
// Later system messages (harness notices added mid-run) stay where they
// are, as {"role":"system"} messages, on models that accept them: rewriting
// the top-level system would invalidate the cache from the system prompt on
// and every thinking block already in the conversation. On other models
// they go to the top-level System field as before.
func (t *Translator) build(model, systemPrompt string, messages []message.Message, tools []*tool.Tool) anthropic.MessageNewParams {
	// Build system prompt blocks (TextBlockParam for the System field)
	var systemBlocks []anthropic.TextBlockParam
	if systemPrompt != "" {
		systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: systemPrompt, Type: "text"})
	}
	inPlace := lookupCaps(model).midConversationSystem

	// Build messages
	var (
		antMessages []anthropic.MessageParam
		notices     []string // mid-conversation system messages not placed yet
	)
	for _, msg := range messages {
		if len(notices) > 0 && msg.Type != message.MessageSystem && msg.Type != message.MessageTool {
			antMessages = appendNotices(antMessages, notices, msg.Type == message.MessageAssistant)
			notices = nil
		}
		switch msg.Type {
		case message.MessageSystem:
			if inPlace && len(antMessages) > 0 {
				if msg.Content != "" {
					notices = append(notices, msg.Content)
				}
				continue
			}
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: msg.Content, Type: "text"})
		case message.MessageUser:
			antMessages = append(antMessages, anthropic.NewUserMessage(
				buildUserBlocks(msg)...,
			))
		case message.MessageAssistant:
			antMessages = append(antMessages, anthropic.NewAssistantMessage(t.assistantBlocks(msg)...))
		case message.MessageTool:
			result := anthropic.NewToolResultBlock(msg.ToolID, msg.Content, false)
			// The results of one turn's calls go back in a single user
			// message, as the API asks for parallel tool use.
			if n := len(antMessages); n > 0 && onlyToolResults(antMessages[n-1]) {
				antMessages[n-1].Content = append(antMessages[n-1].Content, result)
			} else {
				antMessages = append(antMessages, anthropic.NewUserMessage(result))
			}
		}
	}
	if len(notices) > 0 {
		antMessages = appendNotices(antMessages, notices, true)
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
	// prefix up to each marker, and the prefix renders tools → system →
	// messages. We attach the marker to the LAST element in each section so
	// the cache covers the largest stable prefix. NewCacheControlEphemeralParam
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
	if t.cacheBreakpoints[CacheMessages] {
		markConversationBreakpoints(antMessages)
	}

	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(model),
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

// assistantBlocks rebuilds an assistant turn the way the API returned it:
// its thinking blocks first, in the order received, then the text, then the
// tool calls. The API requires thinking blocks back within a tool-use turn
// and recommends them on every turn, unchanged, empty thinking text
// included.
func (t *Translator) assistantBlocks(msg message.Message) []anthropic.ContentBlockParamUnion {
	var blocks []anthropic.ContentBlockParamUnion
	for _, b := range msg.ThinkingBlocks {
		if block, ok := t.thinkingBlock(b); ok {
			blocks = append(blocks, block)
		}
	}
	if msg.Content != "" {
		blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
	}
	for _, tc := range msg.ToolCalls {
		blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, toolInput(tc.Arguments), tc.Name))
	}
	if len(blocks) == 0 {
		blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
	}
	return blocks
}

// thinkingBlock returns the request form of a stored thinking block, or
// false when it must not be sent: another provider's block, one the API
// already rejected, or one without the signature the API requires.
func (t *Translator) thinkingBlock(b message.ThinkingBlock) (anthropic.ContentBlockParamUnion, bool) {
	if b.Provider != thinkingFormat || t.rejected.has(b) {
		return anthropic.ContentBlockParamUnion{}, false
	}
	switch {
	case b.Type == blockThinking && b.Signature != "":
		return anthropic.NewThinkingBlock(b.Signature, b.Thinking), true
	case b.Type == blockRedactedThinking && b.Data != "":
		return anthropic.NewRedactedThinkingBlock(b.Data), true
	}
	return anthropic.ContentBlockParamUnion{}, false
}

// systemReminder wraps a notice that has to travel as a user turn, so the
// model can tell it from the user's own words.
const systemReminder = "<system-reminder>\n%s\n</system-reminder>"

// appendNotices adds the mid-conversation system messages collected since
// the last turn. The API takes a {"role":"system"} message only right after
// a user turn (tool results included) and right before an assistant turn or
// at the end of the array. Anywhere else, such as after an assistant turn,
// the notices go as a user turn instead, so the request stays valid without
// moving or rewriting anything sent before. The caller holds notices back
// while tool results are still due: none may sit between a tool call and
// its result.
func appendNotices(msgs []anthropic.MessageParam, notices []string, beforeAssistant bool) []anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, len(notices))
	if beforeAssistant && msgs[len(msgs)-1].Role == anthropic.MessageParamRoleUser {
		for i, n := range notices {
			blocks[i] = anthropic.NewTextBlock(n)
		}
		return append(msgs, anthropic.MessageParam{Role: anthropic.MessageParamRoleSystem, Content: blocks})
	}
	for i, n := range notices {
		blocks[i] = anthropic.NewTextBlock(fmt.Sprintf(systemReminder, n))
	}
	return append(msgs, anthropic.NewUserMessage(blocks...))
}

// onlyToolResults reports whether m is a user message made of tool results.
func onlyToolResults(m anthropic.MessageParam) bool {
	if m.Role != anthropic.MessageParamRoleUser || len(m.Content) == 0 {
		return false
	}
	for _, b := range m.Content {
		if b.OfToolResult == nil {
			return false
		}
	}
	return true
}

// blockRef locates one content block of the request and the cache position
// it falls in.
type blockRef struct {
	msg, block int
	position   int
}

// markConversationBreakpoints puts the moving cache breakpoint on the last
// cacheable block of the conversation. When the content since the end of
// the previous request — everything from the last assistant turn on — spans
// more positions than the API looks back, the new breakpoint cannot find the
// previous request's cache entry, so the block that request ended with is
// marked too and read from cache.
func markConversationBreakpoints(msgs []anthropic.MessageParam) {
	var (
		refs          []blockRef
		position      int
		prevKind      string
		lastAssistant = -1
	)
	for i, m := range msgs {
		if m.Role == anthropic.MessageParamRoleAssistant {
			lastAssistant = len(refs)
		}
		for j, b := range m.Content {
			kind := ""
			switch {
			case b.OfToolUse != nil:
				kind = blockToolUse
			case b.OfToolResult != nil:
				kind = blockToolResult
			}
			if kind == "" || kind != prevKind {
				position++
			}
			prevKind = kind
			refs = append(refs, blockRef{msg: i, block: j, position: position})
		}
	}

	last := lastCacheable(msgs, refs, len(refs))
	if last < 0 {
		return
	}
	markCache(msgs, refs[last])
	if lastAssistant < 0 {
		return
	}
	if prev := lastCacheable(msgs, refs, lastAssistant); prev >= 0 &&
		refs[last].position-refs[prev].position >= lookbackPositions {
		markCache(msgs, refs[prev])
	}
}

// lastCacheable returns the index in refs of the last block before end that
// can carry cache_control, or -1. Thinking blocks cannot, and the API
// rejects a marker on an empty text block. System messages are skipped too:
// the docs do not cover markers on them, and the block before one is as good.
func lastCacheable(msgs []anthropic.MessageParam, refs []blockRef, end int) int {
	for i := end - 1; i >= 0; i-- {
		m := msgs[refs[i].msg]
		b := m.Content[refs[i].block]
		if m.Role == anthropic.MessageParamRoleSystem || b.GetCacheControl() == nil ||
			(b.OfText != nil && b.OfText.Text == "") {
			continue
		}
		return i
	}
	return -1
}

func markCache(msgs []anthropic.MessageParam, ref blockRef) {
	*msgs[ref.msg].Content[ref.block].GetCacheControl() = anthropic.NewCacheControlEphemeralParam()
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
		case blockText:
			content += block.Text
		case blockToolUse:
			result.ToolCalls = append(result.ToolCalls, message.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: toolInput(block.Input),
			})
		case blockThinking, blockRedactedThinking:
			result.ThinkingBlocks = append(result.ThinkingBlocks, message.ThinkingBlock{
				Provider:  thinkingFormat,
				Type:      block.Type,
				Thinking:  block.Thinking,
				Signature: block.Signature,
				Data:      block.Data,
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

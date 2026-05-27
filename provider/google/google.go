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

	// providerID is the label stamped on every LLMResponse / StreamChunk.
	// Defaults to "google"; override via WithProviderID when proxying
	// Gemini through Vertex AI or a corporate gateway so the trace UI
	// shows the routing layer instead of collapsing to "google".
	providerID string

	// keySuffix is "****xxxx" for the API key passed to NewProvider —
	// stamped on responses / chunks for trace attribution. The raw key
	// is consumed once by the SDK client and never stored.
	keySuffix string

	// Default thinking config. budget=0 means "disable thinking by
	// default"; -1 means "model-default thinking budget" (Gemini accepts
	// a missing field as auto on supported models, so we leave it out
	// when budget is zero).
	defaultThinkingBudget int32
	defaultIncludeThoughts bool
	includeReasoning      bool
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
//
// Caveat for thinking-capable Gemini models (2.5+ flash, pro): the token
// budget covers both hidden reasoning AND visible text. A tight cap
// (e.g. 50) often returns empty visible output because reasoning consumed
// the budget first. For tight caps, also pass WithThinkingBudget(0) to
// disable hidden reasoning, or raise this limit to 256+ to leave room.
func WithMaxTokens(n int) Option {
	return func(p *Provider) { p.maxTokens = n }
}

// WithTemperature sets the default temperature.
func WithTemperature(t float64) Option {
	return func(p *Provider) { p.temperature = t }
}

// WithThinkingBudget enables Gemini thinking with the given token budget.
// 0 disables. Negative values are clamped to 0.
func WithThinkingBudget(budget int) Option {
	return func(p *Provider) {
		if budget < 0 {
			budget = 0
		}
		p.defaultThinkingBudget = int32(budget)
	}
}

// WithIncludeThoughts toggles the API-level includeThoughts flag. Even
// when this is true, downstream consumers only see reasoning chunks if
// WithIncludeReasoning is also true (or the request's
// ReasoningConfig.IncludeInOutput is true).
func WithIncludeThoughts(include bool) Option {
	return func(p *Provider) { p.defaultIncludeThoughts = include }
}

// WithIncludeReasoning controls whether thought parts are forwarded as
// StreamChunk.Reasoning / LLMResponse.Reasoning.
func WithIncludeReasoning(b bool) Option {
	return func(p *Provider) { p.includeReasoning = b }
}

// WithProviderID overrides the provider-id label stamped on every response
// and chunk. The default is "google". Useful when fronting Gemini with
// Vertex AI or a corporate proxy and you want telemetry to attribute the
// call to the gateway rather than to "google".
func WithProviderID(id string) Option {
	return func(p *Provider) {
		if id != "" {
			p.providerID = id
		}
	}
}

// effortToBudget maps a tiered effort to a Gemini token budget. Numbers
// chosen to roughly match Anthropic's tiers — exact values don't matter
// for portability since each provider scales them differently anyway.
func effortToBudget(e provider.ReasoningEffort) int32 {
	switch e {
	case provider.ReasoningEffortMinimal:
		return 256
	case provider.ReasoningEffortLow:
		return 1024
	case provider.ReasoningEffortMedium:
		return 4096
	case provider.ReasoningEffortHigh:
		return 16384
	}
	return 0
}

func (p *Provider) resolveThinking(rc *provider.ReasoningConfig) (budget int32, include bool, on bool) {
	if rc == nil {
		if p.defaultThinkingBudget > 0 {
			return p.defaultThinkingBudget, p.defaultIncludeThoughts, true
		}
		return 0, false, false
	}
	b := int32(rc.BudgetTokens)
	if b <= 0 {
		b = effortToBudget(rc.Effort)
	}
	if b <= 0 {
		return 0, false, false
	}
	return b, rc.IncludeInOutput, true
}

func (p *Provider) shouldIncludeReasoning(rc *provider.ReasoningConfig) bool {
	if rc != nil {
		return rc.IncludeInOutput
	}
	return p.includeReasoning
}

// NewProvider creates a Google GenAI provider.
func NewProvider(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		model:      "gemini-2.5-pro",
		providerID: "google",
		config:     &provider.CacheConfig{Strategy: provider.CacheAuto},
		// No explicit cap by default — google.go skips MaxOutputTokens
		// when n <= 0, so Gemini's per-model completion ceiling applies.
		// Callers that need a tighter budget use WithMaxTokens.
		maxTokens:   0,
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
	p.keySuffix = provider.APIKeySuffix(apiKey)

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
	if budget, include, on := p.resolveThinking(req.Reasoning); on {
		b := budget
		config.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: include,
			ThinkingBudget:  &b,
		}
	}
	if tc := buildToolConfig(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		config.ToolConfig = tc
	}
	if err := applyResponseSchema(req.ResponseSchema, config); err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, fmt.Errorf("google generate: %w", err)
	}

	out, err := p.translator.FromNative(resp)
	if err != nil {
		return nil, err
	}
	if p.shouldIncludeReasoning(req.Reasoning) {
		out.Reasoning = extractThoughts(resp)
	}
	out.ProviderID = p.providerID
	out.ModelID = req.Model
	out.APIKeySuffix = p.keySuffix
	return out, nil
}

// extractThoughts concatenates parts marked thought=true across the first
// candidate. Returns "" when no thoughts were emitted (e.g. budget=0 or
// model didn't think).
func extractThoughts(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return ""
	}
	var b string
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Thought && part.Text != "" {
			b += part.Text
		}
	}
	return b
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
	if budget, include, on := p.resolveThinking(req.Reasoning); on {
		b := budget
		config.ThinkingConfig = &genai.ThinkingConfig{
			IncludeThoughts: include,
			ThinkingBudget:  &b,
		}
	}
	if tc := buildToolConfig(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		config.ToolConfig = tc
	}
	if err := applyResponseSchema(req.ResponseSchema, config); err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}

	includeReasoning := p.shouldIncludeReasoning(req.Reasoning)
	seq := p.client.Models.GenerateContentStream(ctx, model, contents, config)
	inner := make(chan provider.StreamChunk, 64)
	out := make(chan provider.StreamChunk, 64)

	go func() {
		defer close(inner)
		processStream(seq, inner, includeReasoning)
	}()
	// Stamp provenance on every chunk (not just IsFinal). processStream
	// emits raw deltas without identity because it has no access to the
	// resolved model name / key surface; we decorate at this boundary so
	// per-chunk attribution survives in the trace UI when chains rotate
	// keys or fall back between providers.
	providerID, keySuffix := p.providerID, p.keySuffix
	go func() {
		defer close(out)
		for chunk := range inner {
			chunk.ProviderID = providerID
			chunk.ModelID = model
			chunk.APIKeySuffix = keySuffix
			out <- chunk
		}
	}()

	return out, nil
}

// Translator returns the Google message translator.
func (p *Provider) Translator() provider.Translator { return p.translator }

// processStream consumes the GenAI stream iterator and emits StreamChunks.
// includeReasoning controls whether parts with Thought=true are surfaced
// on StreamChunk.Reasoning. When false, they're silently dropped.
// processStream consumes Gemini's streaming response iterator and turns it
// into provider.StreamChunk events on ch. Invariants:
//
//   - Text parts are forwarded as Content deltas (or Reasoning deltas when
//     part.Thought and includeReasoning are both true). Thought parts with
//     reasoning disabled fall through to Content so thinking-capable models
//     don't drop their visible answer on the floor.
//   - Tool calls (Part.FunctionCall) are accumulated and emitted on the
//     final chunk only — the agent loop only processes ToolCalls when the
//     chunk is marked IsFinal=true.
//   - UsageMetadata appears on every streamed response in practice, so the
//     latest value wins instead of triggering an early return.
//   - Exactly one IsFinal=true chunk is emitted, after the iterator closes
//     or as soon as a candidate carries a non-empty FinishReason.
//
// The earlier implementation short-circuited on first UsageMetadata sight
// and used a malformed boolean for IsFinal, dropping the FinishReason=STOP
// signal and the model's full response with it.
func processStream(seq iter.Seq2[*genai.GenerateContentResponse, error], ch chan<- provider.StreamChunk, includeReasoning bool) {
	var (
		contentBuilder string
		toolCalls      []message.ToolCall
		usage          *provider.Usage
		finishErr      error
	)

	for resp, err := range seq {
		if err != nil {
			ch <- provider.StreamChunk{Error: err}
			return
		}
		if resp == nil {
			continue
		}
		if resp.UsageMetadata != nil {
			usage = &provider.Usage{
				InputTokens:  int(resp.UsageMetadata.PromptTokenCount),
				OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount),
				CachedTokens: int(resp.UsageMetadata.CachedContentTokenCount),
			}
		}
		if len(resp.Candidates) == 0 {
			continue
		}
		candidate := resp.Candidates[0]
		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					if part.Thought && includeReasoning {
						ch <- provider.StreamChunk{Reasoning: part.Text}
						continue
					}
					// Either it's plain content, or it's a thought with
					// reasoning disabled — treat both as content so the
					// loop accumulates them and the answer survives.
					contentBuilder += part.Text
					ch <- provider.StreamChunk{Content: part.Text}
				}
				if part.FunctionCall != nil {
					argsJSON, _ := json.Marshal(part.FunctionCall.Args)
					toolCalls = append(toolCalls, message.ToolCall{
						ID:        part.FunctionCall.ID,
						Name:      part.FunctionCall.Name,
						Arguments: argsJSON,
						// Gemini 3.x emits a thought signature alongside each
						// function call and rejects follow-up requests whose
						// history doesn't echo it; persist alongside the call
						// so ToNative can replay it.
						Signature: part.ThoughtSignature,
					})
				}
			}
			// Warn once per response if any Parts carried payload kinds the
			// universal format cannot surface. Done after the text/tool
			// loop so the warning includes everything in the response, and
			// inside the Content guard so we don't log on empty turns.
			logDroppedParts(candidate.Content.Parts)
		}
		if candidate.FinishReason != "" {
			// Map terminal reasons to a typed error so the caller sees
			// the actual cause (MISSING_THOUGHT_SIGNATURE, safety, etc.)
			// instead of an empty turn. nil for STOP and MAX_TOKENS.
			finishErr = finishReasonError(candidate.FinishReason)
			break
		}
	}

	ch <- provider.StreamChunk{
		Content:   contentBuilder,
		ToolCalls: toolCalls,
		Usage:     usage,
		IsFinal:   true,
		Error:     finishErr,
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
				Parts: buildUserParts(msg),
			}
		case message.MessageAssistant:
			var parts []*genai.Part
			if msg.Content != "" {
				parts = append(parts, &genai.Part{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				// Tool calls from assistant → FunctionCall parts. The
				// ThoughtSignature carries Gemini 3.x's opaque per-call
				// blob — required on echo or the API rejects the request
				// with INVALID_ARGUMENT. Empty for earlier Gemini families.
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: parseJSONToMap(tc.Arguments),
					},
					ThoughtSignature: tc.Signature,
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
			// Merge into the previous Content when roles match. Gemini
			// rejects requests where two Contents share a role back to
			// back ("function call turn comes immediately after a user
			// turn or after a function response turn") whereas OpenAI
			// and Anthropic tolerate the shape. Common triggers:
			//   - assistant text followed by assistant tool calls
			//     persisted as two messages,
			//   - several MessageTool results in a row (one per call in
			//     a parallel-tool turn), each emitting a "user" Content.
			// Both forms are semantically a single turn and Gemini is
			// happy with one Content carrying mixed Parts.
			if n := len(contents); n > 0 && contents[n-1].Role == content.Role {
				contents[n-1].Parts = append(contents[n-1].Parts, content.Parts...)
			} else {
				contents = append(contents, content)
			}
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
				// Gemini 3.x emits a thought signature alongside each function
				// call and rejects follow-up requests whose history doesn't
				// echo it; persist so ToNative can replay it.
				Signature: part.ThoughtSignature,
			})
		}
	}

	result.Content = content

	// Visibility for unrepresentable Parts (image bytes, file refs,
	// code-execution payloads); see diagnostics.go. The text/toolcall
	// payload is still returned — we just announce what we dropped.
	logDroppedParts(candidate.Content.Parts)

	if candidate.FinishReason == genai.FinishReasonStop && len(result.ToolCalls) == 0 {
		result.IsFinal = true
	}

	// Map terminal finish reasons that imply an incomplete or invalid
	// response to a typed error so callers can react. STOP and
	// MAX_TOKENS are not errors — STOP is the happy path and
	// MAX_TOKENS still yields a usable (truncated) string the caller
	// may decide to retry. Returning the partial result alongside the
	// error preserves anything already generated.
	if err := finishReasonError(candidate.FinishReason); err != nil {
		return result, err
	}

	return result, nil
}

// buildUserParts maps a universal user message to a slice of genai.Parts.
// Pure-text messages produce a single text Part (matching legacy behavior).
// Multi-modal messages emit InlineData for ImagePart / FilePart and FileData
// for ImageURLPart — the latter assumes the Gemini model can fetch the URL
// (Files API upload happens client-side, outside the framework).
func buildUserParts(msg message.Message) []*genai.Part {
	if len(msg.Parts) == 0 {
		return []*genai.Part{{Text: msg.Content}}
	}
	parts := make([]*genai.Part, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case message.PartText:
			if p.Text != "" {
				parts = append(parts, &genai.Part{Text: p.Text})
			}
		case message.PartImageURL:
			parts = append(parts, &genai.Part{
				FileData: &genai.FileData{FileURI: p.URL, MIMEType: p.MimeType},
			})
		case message.PartImage, message.PartAudio:
			parts = append(parts, &genai.Part{
				InlineData: &genai.Blob{Data: p.Data, MIMEType: p.MimeType},
			})
		case message.PartFile:
			parts = append(parts, &genai.Part{
				InlineData: &genai.Blob{Data: p.Data, MIMEType: p.MimeType, DisplayName: p.Name},
			})
		}
	}
	if len(parts) == 0 {
		// Gemini rejects empty Contents; emit a placeholder text Part.
		parts = append(parts, &genai.Part{Text: ""})
	}
	return parts
}

// parseJSONToMap parses raw JSON message arguments into a map.
func parseJSONToMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_raw": string(raw)}
	}
	return m
}

// convertSchema lives in schema.go. It turns an invopop JSON Schema
// (with $ref/$defs, possibly recursive) into a Gemini-compatible
// *genai.Schema by inlining refs and breaking cycles.

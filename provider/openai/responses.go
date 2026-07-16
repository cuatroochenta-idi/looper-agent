// Responses API support for the OpenAI provider.
//
// OpenAI rejects function tools + reasoning_effort != none on
// /v1/chat/completions for the newer reasoning families (gpt-5.4, gpt-5.6)
// with a 400 pointing at /v1/responses. This file implements that path:
// request mapping (provider.LLMRequest → responses.ResponseNewParams),
// response mapping back to provider.LLMResponse, and the stateless
// reasoning-item replay that multi-turn tool loops require.
//
// Statelessness contract: looper replays the full history on every call,
// so the server-side conversation store stays off (store:false). With
// store:false, OpenAI requires a replayed function_call input item to be
// preceded by the reasoning item that produced it; `encrypted_content`
// (requested via include: reasoning.encrypted_content) is how a stateless
// client round-trips that reasoning item. We persist it — together with
// the sibling output items — as a JSON blob on the first ToolCall's
// Signature, the same opaque-blob channel Gemini's thoughtSignature uses.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// Output item type discriminators shared by the response walk, the
// signature blob, and the input replay. These are the only item kinds
// relevant for stateless replay; everything else is ignored.
const (
	outputItemTypeMessage      = "message"
	outputItemTypeFunctionCall = "function_call"
	outputItemTypeReasoning    = "reasoning"
)

// apiFor picks the API surface for one call: an explicit WithAPI always
// wins; APIAuto routes to Responses only when an effort is resolved for
// this request AND baseURL is unset (the real api.openai.com —
// OpenAI-compatible endpoints often lack /v1/responses entirely).
func (p *Provider) apiFor(eff shared.ReasoningEffort) API {
	if p.api != APIAuto {
		return p.api
	}
	if eff != "" && p.baseURL == "" {
		return APIResponses
	}
	return APIChatCompletions
}

// chatResponses is the non-streaming /v1/responses counterpart of Chat.
func (p *Provider) chatResponses(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	params, model, err := p.buildResponsesParams(req)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai responses: %w", err)
	}

	out := walkResponsesOutput(resp, p.shouldIncludeReasoning(req.Reasoning))
	result := &provider.LLMResponse{
		Content:   out.Content,
		Reasoning: out.Reasoning,
		ToolCalls: out.ToolCalls,
		Usage:     usageFromResponses(resp.Usage),
		// Mirror of the chat translator's rule: a reply without tool calls
		// is final. The responses API has no finish_reason; a completed
		// Response with no function calls is the terminal case.
		IsFinal:      len(out.ToolCalls) == 0,
		ProviderID:   p.providerID,
		ModelID:      model,
		APIKeySuffix: provider.APIKeySuffix(p.apiKeys[0]),
	}
	return result, nil
}

// buildResponsesParams maps a universal LLMRequest onto ResponseNewParams
// and returns the effective model. It deliberately does NOT go through the
// chat translator — the wire shapes share nothing beyond the model name.
func (p *Provider) buildResponsesParams(req provider.LLMRequest) (responses.ResponseNewParams, string, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
		// Looper is stateless: the full history is replayed on every call,
		// so the server-side conversation store must stay off.
		Store: openai.Bool(false),
		// Always request encrypted reasoning content — it is the only way
		// a stateless client can replay reasoning items on the next turn
		// of a tool loop (see the package comment).
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Input:   responses.ResponseNewParamsInputUnion{OfInputItemList: buildResponsesInput(req.Messages)},
	}

	if req.SystemPrompt != "" {
		params.Instructions = openai.String(req.SystemPrompt)
	}

	// Same "0 means unset" semantic as applyMaxTokens on the chat path:
	// omit the cap entirely and let the per-model ceiling apply.
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = p.maxTokens
	}
	if maxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(maxTokens))
	}

	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}

	if eff := p.resolveEffort(req.Reasoning); eff != "" {
		params.Reasoning = shared.ReasoningParam{Effort: eff}
	}
	// Reasoning summaries are the only reasoning text the responses API
	// surfaces; only ask for them when the caller wants reasoning output.
	if p.shouldIncludeReasoning(req.Reasoning) {
		params.Reasoning.Summary = shared.ReasoningSummaryAuto
	}

	if len(req.Tools) > 0 {
		params.Tools = make([]responses.ToolUnionParam, len(req.Tools))
		for i, tl := range req.Tools {
			// Responses function tools are FLAT (name/parameters at the
			// top level) — no nested "function" object like chat. Strict
			// is a required field on this shape; false matches the chat
			// path's non-strict philosophy (our schema generator emits
			// properties strict mode rejects).
			params.Tools[i] = responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
				Name:        tl.Name(),
				Description: openai.String(tl.Description()),
				Parameters:  tl.SchemaMap(),
				Strict:      openai.Bool(false),
			}}
		}
	}
	if tc := buildResponsesToolChoice(req.ToolChoice); tc != nil && len(req.Tools) > 0 {
		params.ToolChoice = *tc
	}

	if tf, err := buildTextFormatParams(req.ResponseSchema, req.ResponseSchemaName, req.ResponseFormatMode); err != nil {
		return params, model, fmt.Errorf("openai response_format: %w", err)
	} else if tf != nil {
		params.Text = *tf
	}

	if len(req.ExtraParams) > 0 {
		params.SetExtraFields(req.ExtraParams)
	}

	return params, model, nil
}

// buildResponsesToolChoice maps a universal ToolChoice onto the responses
// tool_choice union. Same contract as buildToolChoiceParams on the chat
// path: nil means "no toolchoice configured", the zero ToolChoice maps to
// "auto".
func buildResponsesToolChoice(c provider.ToolChoice) *responses.ResponseNewParamsToolChoiceUnion {
	switch c.Kind {
	case provider.ToolChoiceKindAuto:
		u := responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto)}
		return &u
	case provider.ToolChoiceKindRequired:
		u := responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsRequired)}
		return &u
	case provider.ToolChoiceKindNone:
		u := responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsNone)}
		return &u
	case provider.ToolChoiceKindSpecific:
		u := responses.ResponseNewParamsToolChoiceUnion{OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: c.Name}}
		return &u
	}
	return nil
}

// buildTextFormatParams mirrors buildResponseFormatParams (chat path) onto
// the responses `text.format` union, honoring the caller's mode:
//
//   - "" / "json_schema": json_schema with the schema body, name defaulting
//     to "result", strict omitted (non-strict) — same wire semantics the
//     chat path sends.
//   - "json_object": the looser JSON mode, schema body NOT sent.
//   - "none": no text block at all, even when a schema is provided.
func buildTextFormatParams(schema []byte, name string, mode provider.ResponseFormatMode) (*responses.ResponseTextConfigParam, error) {
	if mode == provider.ResponseFormatNone {
		return nil, nil
	}
	if mode == provider.ResponseFormatJSONObject {
		return &responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
			},
		}, nil
	}
	// Auto / JSONSchema → schema body required.
	if len(schema) == 0 {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return nil, err
	}
	if name == "" {
		name = "result"
	}
	return &responses.ResponseTextConfigParam{
		Format: responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
				Name:   name,
				Schema: raw,
			},
		},
	}, nil
}

// buildResponsesInput converts universal history messages into responses
// input items, in order. Assistant turns recorded by this path replay
// verbatim from their signature blob; everything else is synthesized from
// the universal fields.
func buildResponsesInput(messages []message.Message) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Type {
		case message.MessageSystem:
			// Hook/middleware system messages keep the "system" role, in
			// parity with the chat translator.
			items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem))
		case message.MessageUser:
			items = append(items, buildResponsesUserMessage(msg))
		case message.MessageAssistant:
			items = appendResponsesAssistantItems(items, msg)
		case message.MessageTool:
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolID, msg.Content))
		}
	}
	return items
}

// buildResponsesUserMessage translates a universal user message into a
// responses input item. Pure-text messages take the fast path (plain
// string content); multi-modal messages emit a content list. Part support
// mirrors the chat translator exactly: text and images only, PartFile /
// PartAudio are skipped silently rather than refusing the whole message.
func buildResponsesUserMessage(msg message.Message) responses.ResponseInputItemUnionParam {
	if isPureText(msg) {
		return responses.ResponseInputItemParamOfMessage(textOf(msg), responses.EasyInputMessageRoleUser)
	}
	parts := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Parts))
	for _, pt := range msg.Parts {
		switch pt.Type {
		case message.PartText:
			if pt.Text != "" {
				parts = append(parts, responses.ResponseInputContentParamOfInputText(pt.Text))
			}
		case message.PartImageURL:
			parts = append(parts, responsesImagePart(pt.URL))
		case message.PartImage:
			dataURL := "data:" + pt.MimeType + ";base64," + base64.StdEncoding.EncodeToString(pt.Data)
			parts = append(parts, responsesImagePart(dataURL))
			// PartFile / PartAudio fall through — parity with the chat
			// translator's buildUserMessage, which skips them silently.
		}
	}
	return responses.ResponseInputItemParamOfMessage(parts, responses.EasyInputMessageRoleUser)
}

// responsesImagePart builds an input_image content part from a URL (remote
// or data:). Detail "auto" matches the chat path, which never sets one.
func responsesImagePart(url string) responses.ResponseInputContentUnionParam {
	img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
	img.OfInputImage.ImageURL = openai.String(url)
	return img
}

// appendResponsesAssistantItems converts one assistant history turn. When
// the first ToolCall carries a looper responses signature blob, the blob
// is the full authoritative replay of that turn (reasoning items included)
// — nothing else is emitted for it. Otherwise (chat-path history, foreign
// signatures like Gemini's thoughtSignature, plain text turns) the turn is
// synthesized from Content + ToolCalls, with no reasoning items.
func appendResponsesAssistantItems(items responses.ResponseInputParam, msg message.Message) responses.ResponseInputParam {
	if len(msg.ToolCalls) > 0 {
		if blob, ok := decodeResponsesSignature(msg.ToolCalls[0].Signature); ok {
			return append(items, replayResponsesSignature(blob)...)
		}
	}
	if msg.Content != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
	}
	for _, tc := range msg.ToolCalls {
		items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(tc.Arguments), tc.ID, tc.Name))
	}
	return items
}

// responsesSignatureMarker is the JSON key that discriminates a looper
// responses blob from foreign Signature payloads (Gemini thoughtSignatures
// are raw opaque bytes, not JSON carrying this key).
const responsesSignatureMarker = "looper_openai_responses"

// responsesSignatureVersion is the blob format version stored under the
// marker key. Bump only on incompatible blob changes.
const responsesSignatureVersion = 1

// responsesSignature is the serialized replay of one assistant turn's
// output items, stored on the first ToolCall's Signature. Items keep their
// original output order — OpenAI validates that a replayed function_call
// is preceded by the reasoning item that produced it.
type responsesSignature struct {
	Marker int                      `json:"looper_openai_responses"`
	Items  []responsesSignatureItem `json:"items"`
}

// responsesSignatureItem is one captured output item. Type is one of the
// outputItemType* constants; the other fields are populated per type:
// reasoning → ID/EncryptedContent/Summary, message → ID/Text,
// function_call → ID/CallID/Name/Arguments.
type responsesSignatureItem struct {
	Type             string   `json:"type"`
	ID               string   `json:"id,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	Text             string   `json:"text,omitempty"`
	CallID           string   `json:"call_id,omitempty"`
	Name             string   `json:"name,omitempty"`
	Arguments        string   `json:"arguments,omitempty"`
}

// decodeResponsesSignature parses a Signature blob defensively: anything
// that is not JSON, or is JSON without the looper marker key, is foreign
// (e.g. a Gemini thoughtSignature) and reports ok=false so the caller
// falls back to synthesis.
func decodeResponsesSignature(sig []byte) (*responsesSignature, bool) {
	if len(sig) == 0 {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(sig, &probe); err != nil {
		return nil, false
	}
	if _, ok := probe[responsesSignatureMarker]; !ok {
		return nil, false
	}
	var s responsesSignature
	if err := json.Unmarshal(sig, &s); err != nil {
		return nil, false
	}
	return &s, true
}

// replayResponsesSignature rebuilds typed input items from a decoded blob,
// preserving the original output order. Unknown item types are skipped —
// forward compatibility over failure.
func replayResponsesSignature(blob *responsesSignature) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(blob.Items))
	for _, it := range blob.Items {
		switch it.Type {
		case outputItemTypeReasoning:
			// Summary must be present (possibly empty) on replayed
			// reasoning items, hence the non-nil slice.
			summary := make([]responses.ResponseReasoningItemSummaryParam, 0, len(it.Summary))
			for _, s := range it.Summary {
				summary = append(summary, responses.ResponseReasoningItemSummaryParam{Text: s})
			}
			item := responses.ResponseInputItemParamOfReasoning(it.ID, summary)
			if it.EncryptedContent != "" {
				item.OfReasoning.EncryptedContent = openai.String(it.EncryptedContent)
			}
			items = append(items, item)
		case outputItemTypeMessage:
			items = append(items, responses.ResponseInputItemParamOfOutputMessage(
				[]responses.ResponseOutputMessageContentUnionParam{{
					OfOutputText: &responses.ResponseOutputTextParam{
						Text:        it.Text,
						Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
					},
				}},
				it.ID,
				responses.ResponseOutputMessageStatusCompleted,
			))
		case outputItemTypeFunctionCall:
			item := responses.ResponseInputItemParamOfFunctionCall(it.Arguments, it.CallID, it.Name)
			if it.ID != "" {
				item.OfFunctionCall.ID = openai.String(it.ID)
			}
			items = append(items, item)
		}
	}
	return items
}

// responsesOutcome is the distilled result of walking a Response's Output:
// what the universal LLMResponse / final StreamChunk need.
type responsesOutcome struct {
	Content   string
	Reasoning string // joined summary texts; only when requested
	ToolCalls []message.ToolCall
}

// walkResponsesOutput maps a completed Response's output items to the
// universal shape. When the reply contains function calls, ALL replay-
// relevant items (reasoning/message/function_call, original order) are
// serialized into a signature blob on the FIRST ToolCall so the next turn
// can replay them statelessly. Text-only replies get no blob — there is
// no ToolCall to attach it to, and completed turns need no reasoning
// replay.
func walkResponsesOutput(resp *responses.Response, includeReasoning bool) responsesOutcome {
	var out responsesOutcome
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	captured := make([]responsesSignatureItem, 0, len(resp.Output))

	for _, item := range resp.Output {
		switch item.Type {
		case outputItemTypeMessage:
			var text strings.Builder
			for _, c := range item.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
			contentBuilder.WriteString(text.String())
			captured = append(captured, responsesSignatureItem{
				Type: outputItemTypeMessage,
				ID:   item.ID,
				Text: text.String(),
			})
		case outputItemTypeFunctionCall:
			out.ToolCalls = append(out.ToolCalls, message.ToolCall{
				// The loop echoes this back as function_call_output's
				// call_id — it must be CallID, NOT the item id.
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
			captured = append(captured, responsesSignatureItem{
				Type:      outputItemTypeFunctionCall,
				ID:        item.ID,
				CallID:    item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		case outputItemTypeReasoning:
			summaries := make([]string, 0, len(item.Summary))
			for _, s := range item.Summary {
				summaries = append(summaries, s.Text)
			}
			if includeReasoning {
				for _, s := range summaries {
					if reasoningBuilder.Len() > 0 {
						reasoningBuilder.WriteByte('\n')
					}
					reasoningBuilder.WriteString(s)
				}
			}
			captured = append(captured, responsesSignatureItem{
				Type:             outputItemTypeReasoning,
				ID:               item.ID,
				EncryptedContent: item.EncryptedContent,
				Summary:          summaries,
			})
		}
	}

	out.Content = contentBuilder.String()
	out.Reasoning = reasoningBuilder.String()

	if len(out.ToolCalls) > 0 {
		blob, err := json.Marshal(responsesSignature{
			Marker: responsesSignatureVersion,
			Items:  captured,
		})
		// Marshal of this plain struct cannot realistically fail; if it
		// ever does, a missing signature only degrades the next turn to
		// fallback synthesis — never fail the current reply for it.
		if err == nil {
			out.ToolCalls[0].Signature = blob
		}
	}

	return out
}

// usageFromResponses maps responses usage to the universal, inclusive
// normalization: input_tokens already includes cached reads; cost comes
// from the raw JSON for OpenRouter-style gateways (absent on OpenAI).
func usageFromResponses(u responses.ResponseUsage) provider.Usage {
	return provider.Usage{
		InputTokens:  int(u.InputTokens),
		OutputTokens: int(u.OutputTokens),
		CachedTokens: int(u.InputTokensDetails.CachedTokens),
		Cost:         extractCostField(u.RawJSON()),
	}
}

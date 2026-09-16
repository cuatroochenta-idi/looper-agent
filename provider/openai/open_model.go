// Open-weights reasoning support for the OpenAI-compatible chat path.
//
// Closed providers keep a model's thinking server-side (OpenAI's Responses
// API) or hand back signed blocks (Anthropic). An open-weights model behind
// an OpenAI-compatible endpoint has neither: unless the client replays the
// previous turns' thinking, the model re-derives its whole plan on every
// tool step. This file is the client half of that contract — how to ask for
// thinking, how to capture it, and how to echo it back.
package openai

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// ReasoningWire names how an OpenAI-compatible endpoint speaks about
// thinking. The two dialects differ in every direction — request field,
// response field, and what has to be echoed back — so the provider needs
// to be told which one it is talking to.
type ReasoningWire string

const (
	// ReasoningWireOpenRouter is OpenRouter's unified dialect: the request
	// carries a `reasoning` object ({effort|max_tokens|enabled}); the
	// response carries `message.reasoning` (text) plus
	// `message.reasoning_details` (an array echoed back verbatim on
	// assistant turns); streaming deltas carry `delta.reasoning` and
	// partial `delta.reasoning_details` entries merged by `index`.
	ReasoningWireOpenRouter ReasoningWire = "openrouter"

	// ReasoningWireReasoningContent is the DeepSeek-native dialect, also
	// spoken by LM Studio and vLLM: the request carries `reasoning_effort`;
	// both the response and the echo-back field are `reasoning_content`, a
	// plain string on assistant turns. DeepSeek rejects a tool-calling
	// request whose history drops it.
	ReasoningWireReasoningContent ReasoningWire = "reasoning_content"
)

// OpenModelReasoning configures thinking for an OpenAI-compatible endpoint
// serving an open-weights model. It is inert on the Responses API path,
// which has its own first-class reasoning surface.
//
// Precedence against the older knobs (WithReasoningEffort,
// WithIncludeReasoning, per-request provider.ReasoningConfig):
//
//   - Effort: this one wins when set; when empty the older knobs resolve
//     the effort exactly as before. On the OpenRouter wire the effort
//     always travels inside the `reasoning` object and the legacy
//     top-level `reasoning_effort` is suppressed, so the two can never
//     disagree on the wire.
//   - Trace: only ever turns surfacing ON. Leave it false to let
//     WithIncludeReasoning / ReasoningConfig.IncludeInOutput decide.
//   - PassBack and MaxTokens/Enabled have no older equivalent.
type OpenModelReasoning struct {
	// Wire selects the dialect. Required — the zero value configures
	// nothing at all.
	Wire ReasoningWire

	// Effort hints how hard the model should think. Empty omits the field.
	// The scale a model actually honours is its own: DeepSeek reads only
	// low / high / max (medium collapses to high) and thinks at high when
	// nothing is said.
	Effort provider.ReasoningEffort

	// MaxTokens is OpenRouter's `reasoning.max_tokens` thinking budget.
	// Zero omits it. Ignored on the reasoning_content wire, which has no
	// budget parameter. OpenRouter accepts either effort or max_tokens,
	// not both — when both are set here, max_tokens is the one sent.
	//
	// The budget only reaches Gemini- and Anthropic-style models. DeepSeek
	// ignores it, so there Effort is the only real lever.
	MaxTokens int

	// Enabled maps to OpenRouter's `reasoning.enabled`. Nil leaves the
	// provider default in place; false switches thinking off on a model
	// that would otherwise think by default.
	Enabled *bool

	// PassBack echoes the previous assistant turns' thinking back on every
	// request (`reasoning_details` or `reasoning_content`, per the wire).
	// This is the interleaved-thinking fix: without it the model re-derives
	// its plan on every tool step, and DeepSeek answers 400 outright.
	//
	// It applies to EVERY stored assistant turn, tool-calling or not — a
	// plain turn whose thinking is dropped is the same 400. A turn that
	// carried no thinking sends no field: the wire has no meaning for an
	// empty one.
	PassBack bool

	// Trace surfaces the thinking to the framework — StreamChunk.Reasoning
	// and LLMResponse.Reasoning get populated, and the loop carries the
	// text onto the turn's trace step. Off by default: a 30k-token think
	// is worth storing only when someone means to read it.
	Trace bool
}

// WithOpenModelReasoning configures thinking for an OpenAI-compatible
// endpoint serving an open-weights model. Only the chat-completions path
// honours it; the Responses API path ignores it entirely.
func WithOpenModelReasoning(cfg OpenModelReasoning) Option {
	return func(p *Provider) { p.openModel = &cfg }
}

// captures reports whether the provider must read thinking off the wire.
// Echoing it back needs the bytes just as much as tracing does, so either
// flag arms the capture.
func (c *OpenModelReasoning) captures() bool {
	return c != nil && (c.PassBack || c.Trace)
}

// usesOpenRouterWire reports whether the configured dialect is OpenRouter's.
func (c *OpenModelReasoning) usesOpenRouterWire() bool {
	return c != nil && c.Wire == ReasoningWireOpenRouter
}

// reasoningObject builds OpenRouter's `reasoning` request object from the
// config plus the effort already resolved from the older knobs. Returns nil
// when the wire is not OpenRouter or when there is nothing to say.
func (c *OpenModelReasoning) reasoningObject(fallbackEffort string) map[string]any {
	if !c.usesOpenRouterWire() {
		return nil
	}

	obj := map[string]any{}
	switch {
	case c.MaxTokens > 0:
		obj["max_tokens"] = c.MaxTokens
	case c.Effort != provider.ReasoningEffortNone:
		obj["effort"] = string(c.Effort)
	case fallbackEffort != "":
		obj["effort"] = fallbackEffort
	}
	if c.Enabled != nil {
		obj["enabled"] = *c.Enabled
	}

	if len(obj) == 0 {
		return nil
	}
	return obj
}

// effectiveEffort resolves the effort to send on the reasoning_content
// wire: this config's when set, the older knobs' otherwise.
func (c *OpenModelReasoning) effectiveEffort(fallback string) string {
	if c != nil && c.Effort != provider.ReasoningEffortNone {
		return string(c.Effort)
	}
	return fallback
}

// echoFields returns the extra JSON fields that replay one assistant
// message's thinking on the wire. Nil when PassBack is off or the message
// carries nothing to replay.
func (c *OpenModelReasoning) echoFields(msg message.Message) map[string]any {
	if c == nil || !c.PassBack {
		return nil
	}

	if c.usesOpenRouterWire() {
		if len(msg.ReasoningDetails) == 0 {
			return nil
		}
		// Verbatim: entries can carry signatures computed over these exact
		// bytes, and OpenRouter refuses a resequenced or re-encoded array.
		return map[string]any{"reasoning_details": msg.ReasoningDetails}
	}

	if msg.Reasoning == "" {
		return nil
	}
	return map[string]any{"reasoning_content": msg.Reasoning}
}

// extractReasoningDetails pulls the raw `reasoning_details` array out of a
// JSON object, unchanged. Returns nil on any miss — an endpoint that only
// reports thinking as text is normal, not an error.
func extractReasoningDetails(raw string) json.RawMessage {
	if raw == "" {
		return nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	v, ok := m["reasoning_details"]
	if !ok || len(v) == 0 || string(v) == "null" {
		return nil
	}
	return append(json.RawMessage(nil), v...)
}

// detailsMerger reassembles a streamed `reasoning_details` array. OpenRouter
// splits each entry across chunks and keys the fragments by `index`: the
// prose fields (`text`, `summary`) concatenate, everything else is a
// property of the whole entry and the latest value wins.
type detailsMerger struct {
	byIndex map[int]*mergedDetail
	order   []int
}

type mergedDetail struct {
	fields  map[string]json.RawMessage
	text    strings.Builder
	summary strings.Builder
	hasText bool
	hasSumm bool
}

// concatenated names the fields that accumulate rather than replace.
var concatenated = map[string]bool{"text": true, "summary": true}

// feed merges one delta's `reasoning_details` array. Entries without an
// `index` fall back to their position in the delta, which is what a server
// that omits the field implies.
func (m *detailsMerger) feed(raw string) {
	arr := extractReasoningDetails(raw)
	if arr == nil {
		return
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(arr, &items); err != nil {
		return
	}

	for pos, item := range items {
		idx := pos
		if v, ok := item["index"]; ok {
			var n int
			if err := json.Unmarshal(v, &n); err == nil {
				idx = n
			}
		}

		if m.byIndex == nil {
			m.byIndex = make(map[int]*mergedDetail)
		}
		d, ok := m.byIndex[idx]
		if !ok {
			d = &mergedDetail{fields: make(map[string]json.RawMessage)}
			m.byIndex[idx] = d
			m.order = append(m.order, idx)
		}

		for k, v := range item {
			if !concatenated[k] {
				d.fields[k] = append(json.RawMessage(nil), v...)
				continue
			}
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				continue
			}
			if k == "text" {
				d.text.WriteString(s)
				d.hasText = true
			} else {
				d.summary.WriteString(s)
				d.hasSumm = true
			}
		}
	}
}

// result marshals the merged entries, ordered by index — the upstream
// validates the sequence against what it generated and refuses a reordered
// array. Nil when no delta ever carried reasoning_details.
func (m *detailsMerger) result() json.RawMessage {
	if len(m.order) == 0 {
		return nil
	}

	idx := append([]int(nil), m.order...)
	sort.Ints(idx)

	out := make([]map[string]any, 0, len(idx))
	for _, i := range idx {
		d := m.byIndex[i]
		entry := make(map[string]any, len(d.fields)+2)
		for k, v := range d.fields {
			entry[k] = v
		}
		if d.hasText {
			entry["text"] = d.text.String()
		}
		if d.hasSumm {
			entry["summary"] = d.summary.String()
		}
		out = append(out, entry)
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

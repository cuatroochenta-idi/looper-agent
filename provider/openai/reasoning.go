// Reasoning support for the OpenAI-compatible provider.
//
// This file holds the two pieces the streaming loop needs:
//
//  1. extractReasoningField — pulls `reasoning_content` (and `reasoning`) out
//     of the raw delta JSON. The openai-go SDK schema doesn't expose them
//     because they're not OpenAI-native, but LM Studio, DeepSeek-R1, Qwen3
//     and gpt-oss compatible servers emit them.
//
//  2. harmonyFilter — a stateful parser that splits a stream containing
//     Harmony channel markers (`<|channel|>analysis<|message|>...<|end|>`,
//     `<|channel|>final<|message|>...<|end|>`) into separate content and
//     reasoning text. Many local models embed Harmony in the `content` field
//     rather than emitting `reasoning_content`, and the markers leak into
//     the UI if we don't strip them.
package openai

import (
	"encoding/json"
	"strings"
)

// extractReasoningField looks for `reasoning_content` or `reasoning` at the
// top level of a JSON object and returns its string value. Returns "" on
// any miss — never fails. Cheap enough to call once per delta.
func extractReasoningField(raw string) string {
	if raw == "" {
		return ""
	}
	// json.Unmarshal into a small map is the simplest correct path; the
	// delta payloads are tiny (sub-kilobyte typically).
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	for _, key := range []string{"reasoning_content", "reasoning"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		// Try string first.
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return s
		}
		// Some servers wrap reasoning in an object with `text` / `summary`.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err == nil {
			for _, k := range []string{"text", "summary", "content"} {
				if inner, ok := obj[k]; ok {
					var is string
					if err := json.Unmarshal(inner, &is); err == nil && is != "" {
						return is
					}
				}
			}
		}
	}
	return ""
}

// harmonyFilter consumes streamed content (potentially split mid-marker)
// and emits two strands of text: visible content and reasoning. It honours
// Harmony channel markers when present and passes everything through
// unchanged when not.
//
// Recognised tokens (handled as opaque substrings — no token IDs):
//
//	<|start|>assistant
//	<|channel|>analysis     ← reasoning starts here
//	<|channel|>commentary   ← also reasoning (tool prelude)
//	<|channel|>final        ← visible content starts here
//	<|message|>             ← begins the body for the active channel
//	<|end|>                 ← closes the active channel
//	<|return|>              ← Harmony end-of-turn
//
// Some servers emit malformed/partial markers (e.g. <|channel> without the
// closing bar, or <channel|>). We accept those too with a forgiving match.
type harmonyFilter struct {
	// includeReasoning is captured at construction so the loop can avoid
	// allocating reasoning output it won't use — but the parser keeps the
	// state machine running so the splitter still strips markers from
	// content.
	includeReasoning bool
	// buf carries text we haven't decided about yet because it might be
	// the prefix of a marker (e.g. we saw "<|cha").
	buf string
	// channel is "", "analysis", "commentary", "final", or "raw" (no
	// markers seen — everything is content).
	channel string
	// inMessage is true when we're past <|message|> in the current channel.
	inMessage bool
}

func newHarmonyFilter(includeReasoning bool) *harmonyFilter {
	return &harmonyFilter{includeReasoning: includeReasoning, channel: "raw"}
}

// feed accepts the next chunk of streamed text and returns the
// (contentDelta, reasoningDelta) to forward downstream. Either may be
// empty. The filter buffers up to a short marker-prefix internally and
// will release it on a later call once the disambiguation is possible.
func (h *harmonyFilter) feed(in string) (string, string) {
	h.buf += in
	var content, reasoning strings.Builder

	for h.buf != "" {
		// Find the next interesting marker prefix `<|` or `<` (the model
		// sometimes drops the leading pipe under bad templating).
		idx := strings.IndexByte(h.buf, '<')
		if idx == -1 {
			// No marker on the horizon — flush the whole buffer.
			h.emit(h.buf, &content, &reasoning)
			h.buf = ""
			break
		}
		if idx > 0 {
			h.emit(h.buf[:idx], &content, &reasoning)
			h.buf = h.buf[idx:]
		}

		// At this point h.buf starts with '<'. Try to parse a marker.
		token, rest, ok, partial := parseHarmonyToken(h.buf)
		if partial {
			// We saw the start of a marker but the buffer ends before we
			// know which one — keep it buffered for the next call.
			return content.String(), reasoning.String()
		}
		if !ok {
			// Not a known marker — emit the '<' and move on.
			h.emit("<", &content, &reasoning)
			h.buf = h.buf[1:]
			continue
		}
		// Known marker: drop the marker bytes and update state.
		h.applyToken(token)
		h.buf = rest
	}

	return content.String(), reasoning.String()
}

// emit routes a fragment to the right output strand based on current state.
func (h *harmonyFilter) emit(s string, content, reasoning *strings.Builder) {
	if s == "" {
		return
	}
	switch h.channel {
	case "raw", "final":
		// Default channel and explicit "final" channel are both visible.
		if !h.inMessage && h.channel == "final" {
			// We're in "final" but haven't seen <|message|> yet — drop
			// whitespace prelude, keep anything substantive.
			t := strings.TrimLeft(s, " \t\n\r")
			content.WriteString(t)
			return
		}
		content.WriteString(s)
	case "analysis", "commentary":
		if h.includeReasoning {
			reasoning.WriteString(s)
		}
	}
}

// applyToken transitions the state machine when we successfully recognise
// a Harmony control token.
func (h *harmonyFilter) applyToken(tok string) {
	switch tok {
	case "<|start|>", "<|start|>assistant":
		// Reset to "raw" until a channel is declared.
		h.channel = "raw"
		h.inMessage = false
	case "<|channel|>analysis", "<channel|>analysis", "<|channel>analysis":
		h.channel = "analysis"
		h.inMessage = false
	case "<|channel|>commentary", "<channel|>commentary", "<|channel>commentary":
		h.channel = "commentary"
		h.inMessage = false
	case "<|channel|>final", "<channel|>final", "<|channel>final":
		h.channel = "final"
		h.inMessage = false
	case "<|channel|>thought", "<channel|>thought", "<|channel>thought":
		// Some templates use "thought" as an alias for analysis.
		h.channel = "analysis"
		h.inMessage = false
	case "<|message|>":
		h.inMessage = true
	case "<|end|>", "<|return|>":
		h.channel = "raw"
		h.inMessage = false
	}
}

// parseHarmonyToken inspects buf (which must start with '<') and tries to
// match one of the known Harmony control tokens. Returns:
//
//	token   — the matched marker (possibly with its inline channel name)
//	rest    — buf with the marker stripped
//	ok      — true on a match
//	partial — true when buf is a strict prefix of a known marker and the
//	          caller should wait for more bytes before deciding.
func parseHarmonyToken(buf string) (token, rest string, ok, partial bool) {
	for _, m := range harmonyMarkers {
		if strings.HasPrefix(buf, m) {
			return m, buf[len(m):], true, false
		}
		if len(buf) < len(m) && strings.HasPrefix(m, buf) {
			partial = true
		}
	}
	return "", buf, false, partial
}

// harmonyMarkers is the ordered list of recognisable tokens. Order matters:
// longer prefixes first so "<|channel|>analysis" wins over "<|channel|>".
var harmonyMarkers = []string{
	"<|channel|>analysis",
	"<|channel|>commentary",
	"<|channel|>final",
	"<|channel|>thought",
	"<channel|>analysis",
	"<channel|>commentary",
	"<channel|>final",
	"<channel|>thought",
	"<|channel>analysis",
	"<|channel>commentary",
	"<|channel>final",
	"<|channel>thought",
	"<|start|>assistant",
	"<|start|>",
	"<|message|>",
	"<|return|>",
	"<|end|>",
}

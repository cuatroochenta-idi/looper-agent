// Diagnostics for the Google translator: turn silent failure modes
// into actionable signals.
//
// Two cases motivate this file:
//
//  1. Gemini can stop a response with finish reasons that the SDK either
//     does not yet expose as constants (e.g. MISSING_THOUGHT_SIGNATURE
//     in the 3.x family) or that the previous translator ignored. The
//     bare `if candidate.FinishReason != "" { break }` treated every
//     non-empty reason as a clean stop, producing empty turns the agent
//     loop could not diagnose.
//
//  2. The Gemini response model lets a single Part carry rich payloads
//     (inline image bytes, executable code, code execution results)
//     that the universal LLMResponse/StreamChunk cannot represent. The
//     translator currently reads `part.Text` and `part.FunctionCall`
//     and drops everything else; if the model ever emits those Parts
//     the data disappears with no signal.
//
// finishReasonError maps reasons to typed errors the caller propagates.
// logDroppedParts emits a single warning per response that carries
// unrepresentable Parts so the failure is visible in logs and tests.
package google

import (
	"fmt"
	"log/slog"

	genai "google.golang.org/genai"
)

// finishReasonError returns a non-nil error when the finish reason
// indicates an incomplete or invalid response. STOP and MAX_TOKENS are
// not errors — STOP is the happy path; MAX_TOKENS yields a usable
// truncated string that the caller may choose to retry against a
// larger budget. Every other terminal reason is surfaced verbatim so
// observability records the actual cause instead of "empty turn".
//
// Includes wire-only strings the SDK constant set has not caught up
// with (notably MISSING_THOUGHT_SIGNATURE on Gemini 3.x). Unknown
// reasons return a generic error rather than silently passing so new
// API states stay visible until the mapping is updated.
func finishReasonError(r genai.FinishReason) error {
	switch r {
	case "", genai.FinishReasonUnspecified, genai.FinishReasonStop, genai.FinishReasonMaxTokens:
		return nil
	case "MISSING_THOUGHT_SIGNATURE":
		return fmt.Errorf("gemini stopped: %s — conversation history is missing a thoughtSignature on a prior function call; ensure message.ToolCall.Signature round-trips", r)
	case genai.FinishReasonMalformedFunctionCall:
		return fmt.Errorf("gemini stopped: %s — model emitted a tool call the SDK could not parse", r)
	case genai.FinishReasonUnexpectedToolCall:
		return fmt.Errorf("gemini stopped: %s — model called a tool not declared in this request", r)
	case genai.FinishReasonSafety,
		genai.FinishReasonProhibitedContent,
		genai.FinishReasonBlocklist,
		genai.FinishReasonSPII,
		genai.FinishReasonRecitation,
		genai.FinishReasonLanguage:
		return fmt.Errorf("gemini blocked the response: %s", r)
	case genai.FinishReasonImageSafety,
		genai.FinishReasonImageProhibitedContent,
		genai.FinishReasonImageRecitation,
		genai.FinishReasonImageOther,
		genai.FinishReasonNoImage:
		return fmt.Errorf("gemini blocked image output: %s", r)
	case genai.FinishReasonOther:
		return fmt.Errorf("gemini stopped: %s", r)
	}
	return fmt.Errorf("gemini stopped: %s (unrecognised by this version of looper-agent — update finishReasonError)", r)
}

// logDroppedParts emits one warning per response when any Part carries
// payload kinds the universal format cannot surface (inline media,
// remote file refs, code execution payloads). The Parts are still
// dropped — extending the universal types would break the other
// providers — but the warning makes the loss visible so a future
// integration that needs them is straightforward to scope.
//
// Logs at Warn so the message appears in production telemetry without
// being mistaken for a transient debug line. A response with zero
// unrepresentable Parts is a no-op.
func logDroppedParts(parts []*genai.Part) {
	var kinds []string
	for _, p := range parts {
		if p == nil {
			continue
		}
		if p.InlineData != nil {
			kinds = append(kinds, "inlineData")
		}
		if p.FileData != nil {
			kinds = append(kinds, "fileData")
		}
		if p.CodeExecutionResult != nil {
			kinds = append(kinds, "codeExecutionResult")
		}
		if p.ExecutableCode != nil {
			kinds = append(kinds, "executableCode")
		}
	}
	if len(kinds) == 0 {
		return
	}
	slog.Warn("google: response carried part kinds the universal format cannot represent — dropping",
		"kinds", dedupe(kinds),
		"count", len(kinds),
	)
}

// dedupe preserves order while removing repeated strings. Used to keep
// the dropped-parts warning concise when the same kind appears in
// multiple Parts of the same response.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

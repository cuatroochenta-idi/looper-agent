package tool

import "context"

// finalResponseKey is the unexported context key used by SetFinalResponse
// and the matching WithFinalResponse helper. Using a named type prevents
// collisions with any other package's context keys.
type finalResponseKey struct{}

// finalResponseSignal is a heap-allocated holder so SetFinalResponse can
// mutate it through the context without a sync.Mutex (single-goroutine
// tool bodies are the normal case; parallel tool bodies each receive
// their own context, mirroring the Halt signal design).
type finalResponseSignal struct{ text string }

func (s *finalResponseSignal) set(v string) { s.text = v }

// SetFinalResponse marks the current tool call as the source of the
// canonical user-facing wrap-up for this turn. The loop surfaces the
// supplied text on StepFinalResponse.Content when the run halts —
// useful when the model produced no streaming chunks (e.g. Gemini in
// thinking mode emits the entire answer inside a tool call's arguments)
// so consumers don't need provider-specific glue to recover the text.
//
// Pattern:
//
//	func finalResponseTool(ctx context.Context, in Input) (string, error) {
//	    // ... gates / validation ...
//	    tool.SetFinalResponse(ctx, in.Message)
//	    tool.SetHalt(ctx)
//	    return `{"ok":true}`, nil
//	}
//
// Notes:
//   - SetFinalResponse is a hint, not an error; the tool may still
//     return a non-empty Content string (which goes to the model as the
//     tool-result message had the run continued).
//   - SetFinalResponse is meaningful only on a halting turn — without
//     SetHalt the loop will keep iterating and the registered text is
//     just metadata on the ToolResult. Setting both together is the
//     canonical close pattern.
//   - When multiple tools register FinalResponse in the same turn, the
//     first non-empty value wins (matches the Halt "first halting
//     result determines status" rule).
//   - SetFinalResponse has no effect outside of a tool body (the
//     context key is absent).
func SetFinalResponse(ctx context.Context, text string) {
	if s, ok := ctx.Value(finalResponseKey{}).(*finalResponseSignal); ok {
		s.set(text)
	}
}

// WithFinalResponse returns a child context that carries a fresh
// final-response signal and a reader function. The loop executor calls
// this before Execute and reads the value after Execute returns to
// propagate it into message.ToolResult.FinalResponse.
//
// This is an internal helper used by loop.executeSingleTool and is not
// part of the public tool API.
func WithFinalResponse(ctx context.Context) (context.Context, func() string) {
	sig := &finalResponseSignal{}
	return context.WithValue(ctx, finalResponseKey{}, sig), func() string { return sig.text }
}

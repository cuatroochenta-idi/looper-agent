package tool

import "context"

// haltKey is the unexported context key used by SetHalt / IsHalted.
// Using a named type prevents collisions with any other package's context keys.
type haltKey struct{}

// haltSignal is a heap-allocated flag so SetHalt can mutate it through the
// context without a sync.Mutex (single-goroutine tool bodies are the normal
// case; parallel tool bodies each receive their own context).
type haltSignal struct{ v bool }

func (s *haltSignal) set() { s.v = true }

// SetHalt marks the current tool call as requesting a clean termination of the
// run. Call this inside a tool body to signal that no further LLM turns should
// follow. The loop stops with RunResult.Status == "halted_by_tool" after
// recording all results for the current turn.
//
// Pattern:
//
//	func myTool(ctx context.Context, in Input) (string, error) {
//	    tool.SetHalt(ctx)
//	    return "Pausing run — awaiting user input.", nil
//	}
//
// Notes:
//   - SetHalt is a hint, not an error; the tool may still return a non-empty
//     content string that the LLM would have seen had the run continued.
//   - If multiple tools in the same turn call SetHalt, the run still halts
//     once — all results are recorded first.
//   - SetHalt has no effect outside of a tool body (the context key is absent).
func SetHalt(ctx context.Context) {
	if h, ok := ctx.Value(haltKey{}).(*haltSignal); ok {
		h.set()
	}
}

// WithHaltSignal returns a child context that carries a fresh halt signal and
// an IsHalted query function. The loop executor calls this before Execute and
// reads IsHalted() after Execute returns to propagate the halt flag into
// message.ToolResult.Halt.
//
// This is an internal helper used by loop.executeSingleTool and is not part of
// the public tool API.
func WithHaltSignal(ctx context.Context) (context.Context, func() bool) {
	sig := &haltSignal{}
	return context.WithValue(ctx, haltKey{}, sig), func() bool { return sig.v }
}

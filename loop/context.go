package loop

import "context"

// parentToolCallIDKey is the context key used by executeSingleTool to stamp
// the in-flight tool's call_id on ctx before invoking the tool function.
// Sub-agents spawned from inside the tool (via subAgent.Run / Iterate) read
// it through ParentToolCallIDFromContext so the panel can correlate the
// tool call in the parent's trace with the child run it produced.
type parentToolCallIDKey struct{}

// ParentToolCallIDFromContext returns the call_id of the tool call that
// owns the goroutine running this ctx, if any. Sub-agent tracers read this
// to populate ParentToolCallID on their TraceEvents.
func ParentToolCallIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(parentToolCallIDKey{}).(string); ok {
		return v
	}
	return ""
}

// ContextWithToolCallID stamps the active tool's call_id onto ctx. Called
// by the loop right before handing ctx to the tool function. Exported so
// the root looper package can read it from the same ctx via the public
// helper above.
func ContextWithToolCallID(ctx context.Context, callID string) context.Context {
	return context.WithValue(ctx, parentToolCallIDKey{}, callID)
}

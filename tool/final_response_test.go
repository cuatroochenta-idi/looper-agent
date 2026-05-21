package tool

import (
	"context"
	"testing"
)

// TestSetFinalResponse_RoundTrip is the core API contract: a tool body
// stamps the canonical wrap-up text via SetFinalResponse(ctx, text),
// and the loop reads it back through the reader returned by
// WithFinalResponse. This is the per-tool-call propagation channel that
// lets message.ToolResult.FinalResponse carry the value out.
func TestSetFinalResponse_RoundTrip(t *testing.T) {
	const text = "Listo. Publiqué la app y cerré el PRD."
	ctx, read := WithFinalResponse(context.Background())
	SetFinalResponse(ctx, text)
	if got := read(); got != text {
		t.Errorf("read() = %q, want %q", got, text)
	}
}

// TestSetFinalResponse_NoOpOutsideToolBody guards the safety contract:
// calling SetFinalResponse on a context that was NOT decorated with
// WithFinalResponse must be a no-op, not a panic. This mirrors the
// SetHalt contract — tool primitives stay safe to call from any code
// path so callers don't need provider-specific guards.
func TestSetFinalResponse_NoOpOutsideToolBody(t *testing.T) {
	// No panic, no error — just silently dropped.
	SetFinalResponse(context.Background(), "anything")
}

// TestSetFinalResponse_EmptyOnNoCall guards the default case: if the
// tool body never calls SetFinalResponse, the reader returns the empty
// string. The loop interprets empty as "no opinion" and falls back to
// the streamed assistant content — the legacy halting-tool behaviour.
func TestSetFinalResponse_EmptyOnNoCall(t *testing.T) {
	_, read := WithFinalResponse(context.Background())
	if got := read(); got != "" {
		t.Errorf("read() = %q, want empty string for an un-set signal", got)
	}
}

// TestSetFinalResponse_LastWriteWins documents the within-one-call
// semantics: if a tool body invokes SetFinalResponse twice (e.g.
// during a retry inside a single execution), the LAST write is the
// one the loop sees. This matches what callers expect — they treat
// SetFinalResponse like assigning a result variable.
//
// Note: precedence ACROSS tool calls in a parallel turn is a different
// question handled by loop.pickHaltFinalText (first non-empty wins).
// This test is strictly the per-call signal.
func TestSetFinalResponse_LastWriteWins(t *testing.T) {
	ctx, read := WithFinalResponse(context.Background())
	SetFinalResponse(ctx, "draft")
	SetFinalResponse(ctx, "final")
	if got := read(); got != "final" {
		t.Errorf("read() = %q, want final (last SetFinalResponse must win)", got)
	}
}

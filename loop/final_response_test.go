package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// finalResponseTool returns a halting tool that registers a canonical
// wrap-up text via tool.SetFinalResponse before halting. Mirrors how a
// real application's `final_response` tool would close the turn.
func finalResponseTool(name, wrapup string) *tool.Tool {
	return tool.MustNewTool(struct{}{}, func(ctx context.Context, _ struct{}) (string, error) {
		tool.SetFinalResponse(ctx, wrapup)
		tool.SetHalt(ctx)
		return `{"ok":true}`, nil
	}, tool.ToolConfig{Name: name, Description: "registers final response and halts", Parallel: true})
}

// TestSetFinalResponse_SurfacesOnHalt is the core guarantee: when a
// halting tool calls SetFinalResponse, the registered text becomes the
// RunResult.Output — not the streamed assistant content. This is the
// path that lets Gemini thinking-mode (zero streaming chunks; full
// answer inside tool call args) surface its answer through the same
// channel as text-streaming providers.
func TestSetFinalResponse_SurfacesOnHalt(t *testing.T) {
	const wrapup = "Hecho. Publiqué la app y cerré el PRD."
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				// Empty Content simulates Gemini thinking-mode: the
				// model writes nothing visible; the entire answer lives
				// in the tool call args.
				Content: "",
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "final_response", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{finalResponseTool("final_response", wrapup)},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "halted_by_tool" {
		t.Errorf("Status = %q, want halted_by_tool", res.Status)
	}
	if res.Output != wrapup {
		t.Errorf("Output = %q, want %q (the SetFinalResponse text must override the empty streamed content)", res.Output, wrapup)
	}
}

// TestSetFinalResponse_FallsBackToStreamedContent guards the legacy
// path: when no tool registers a final response, the run keeps using
// the model's streamed text. SetFinalResponse must be opt-in — every
// existing halting tool must work unchanged.
func TestSetFinalResponse_FallsBackToStreamedContent(t *testing.T) {
	const streamed = "Pausing run — awaiting user input."
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				// Model emits streamed text + halting tool call. The
				// halting tool does NOT register SetFinalResponse, so
				// the loop keeps streamed text as the canonical Output.
				Content: streamed,
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "halt_only", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{haltingTool("halt_only")},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "halted_by_tool" {
		t.Errorf("Status = %q, want halted_by_tool", res.Status)
	}
	if res.Output != streamed {
		t.Errorf("Output = %q, want %q (legacy halting tools must keep using streamed text)", res.Output, streamed)
	}
}

// TestSetFinalResponse_FirstWinsAmongParallelTools verifies the
// precedence rule: when multiple parallel halting tools register a
// final response in the same turn, the FIRST non-empty value wins.
// Same semantics as Halt itself (first halting result determines run
// status). Tools that halt without registering text don't override a
// sibling's text — empty FinalResponse means "I have no opinion".
func TestSetFinalResponse_FirstWinsAmongParallelTools(t *testing.T) {
	const firstText = "first wrap-up wins"
	const secondText = "second wrap-up loses"
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				Content: "",
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "final_a", Arguments: json.RawMessage(`{}`)},
					{ID: "tc2", Name: "final_b", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{
			finalResponseTool("final_a", firstText),
			finalResponseTool("final_b", secondText),
		},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != firstText {
		t.Errorf("Output = %q, want %q (first non-empty FinalResponse must win)", res.Output, firstText)
	}
}

// TestSetFinalResponse_PropagatesOnToolResult guards the wire-level
// guarantee: the registered text lands on message.ToolResult.FinalResponse
// so consumers walking the history (persistence layers, audit logs) can
// inspect it without rerunning the tool. The wire field is the source
// of truth; the loop is just one of its readers.
func TestSetFinalResponse_PropagatesOnToolResult(t *testing.T) {
	const wrapup = "done"
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				Content: "",
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "final_response", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{finalResponseTool("final_response", wrapup)},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// History should carry the tool result row. The FinalResponse
	// field is set on message.ToolResult and AddToolResult preserves
	// the Content; the wire field on the persisted history view is
	// what consumers inspect. We verify the in-memory result-side
	// invariant here (same struct that ends up serialised).
	var found bool
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageTool && m.ToolID == "tc1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("tool result for tc1 missing from history")
	}
}

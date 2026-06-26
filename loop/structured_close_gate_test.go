package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// finalResponseClose builds a provider response that closes the run via the
// framework-injected final_response tool (the structured-output terminator),
// carrying `output` as its answer.
func finalResponseClose(output string) *provider.LLMResponse {
	args, _ := json.Marshal(map[string]string{"output": output})
	return &provider.LLMResponse{
		IsFinal: true,
		ToolCalls: []message.ToolCall{
			{ID: "fr1", Name: "final_response", Arguments: args},
		},
		Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
	}
}

// TestStructuredClose_ValidatorGatesPrematureClose is the regression for the
// bug where a final_response emitted via WithStructuredOutput short-circuited
// the loop and BYPASSED the TurnValidator entirely. The validator rejects the
// first close and accepts the second; the run must re-prompt rather than
// commit the first close, and the second close must be the committed answer.
func TestStructuredClose_ValidatorGatesPrematureClose(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			finalResponseClose("premature"), // turn 0 — rejected
			finalResponseClose("done"),      // turn 1 — accepted
		},
	}

	// Reject the close on turn 0, accept it from turn 1 onward.
	validator := TurnValidatorFunc(func(_ context.Context, snap TurnSnapshot) Outcome {
		if snap.Turn == 0 {
			return Outcome{OK: false, Reason: "too_soon", Hint: "do work before finishing"}
		}
		return Outcome{OK: true}
	})

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		nil,
		WithLoopStructuredOutput(json.RawMessage(`{"type":"object"}`)),
		WithLoopTurnValidator(validator, 3),
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("Status = %q, want completed", res.Status)
	}
	if res.Output != `"done"` {
		t.Errorf("Output = %q, want \"done\" (the first close must have been refused)", res.Output)
	}
	if res.Turns < 2 {
		t.Errorf("Turns = %d, want >= 2 (the rejected close must have re-prompted)", res.Turns)
	}
	// The refused close must have answered its dangling final_response call
	// with a result carrying the rejection reason — otherwise the re-prompt
	// request would carry an assistant tool_calls message with no matching
	// tool result and providers would 400.
	var sawRejection bool
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageTool && m.ToolID == "fr1" &&
			strings.Contains(m.Content, "too_soon") {
			sawRejection = true
			break
		}
	}
	if !sawRejection {
		t.Error("refused close did not answer its final_response tool call with the rejection reason")
	}
}

// TestStructuredClose_NoValidatorUnchanged guards the default path: with no
// turn validator, a structured-output close commits immediately on turn 0,
// exactly as before the gate was added.
func TestStructuredClose_NoValidatorUnchanged(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{finalResponseClose("immediate")},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		nil,
		WithLoopStructuredOutput(json.RawMessage(`{"type":"object"}`)),
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != `"immediate"` || res.Turns != 1 {
		t.Errorf("got Output=%q Turns=%d, want \"immediate\"/1 (validator-less close must be unchanged)", res.Output, res.Turns)
	}
}

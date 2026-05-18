package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// toolCallResponse returns a mock LLM response that calls the named tool.
func toolCallResponse(toolName, callID string) *provider.LLMResponse {
	return &provider.LLMResponse{
		ToolCalls: []message.ToolCall{
			{ID: callID, Name: toolName, Arguments: json.RawMessage(`{}`)},
		},
		Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
	}
}

// nopTool builds a simple tool that returns "ok" and does nothing else.
func nopTool(name string) *tool.Tool {
	return tool.MustNewTool(struct{}{}, func(_ context.Context, _ struct{}) (string, error) {
		return "ok", nil
	}, tool.ToolConfig{Name: name, Description: "no-op", Parallel: true})
}

// TestValidator_SkipBudget_DoesNotExhaust verifies that a validator that
// returns OK=false, SkipBudget=true on every tool-call turn never exhausts
// the retry budget. The loop must finish with max_turns_exceeded (or
// completed), never with validation_exhausted.
func TestValidator_SkipBudget_DoesNotExhaust(t *testing.T) {
	const maxTurns = 5
	const validatorRetries = 3

	// Every response is a tool call so the loop keeps iterating until maxTurns.
	responses := make([]*provider.LLMResponse, maxTurns)
	for i := range responses {
		responses[i] = toolCallResponse("nop", "tc"+string(rune('0'+i)))
	}

	prov := &mockProvider{model: "mock", responses: responses}
	nop := nopTool("nop")

	alwaysSkip := TurnValidatorFunc(func(_ context.Context, snap TurnSnapshot) Outcome {
		return Outcome{
			OK:         false,
			SkipBudget: true,
			Hint:       "still working",
		}
	})

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "sys" }, []*tool.Tool{nop},
		WithLoopMaxTurns(maxTurns),
		WithLoopTurnValidator(alwaysSkip, validatorRetries),
	)

	_, err := lp.Run(context.Background(), "go")
	// max_turns_exceeded is returned as an error from Run (not RunResult.Status).
	// That is the expected outcome — validation_exhausted must NOT appear.
	if err == nil {
		t.Fatal("expected max-turns error, got nil")
	}
	// Ensure the error is about max turns, not validation.
	if err.Error() == "validation_exhausted" {
		t.Errorf("got validation_exhausted but expected max_turns exceeded: %v", err)
	}
}

// TestValidator_SkipBudget_ResetsOnOK checks that alternating
// reject(SkipBudget=true) / accept turns never tick the budget counter.
// After the sequence the run completes successfully.
func TestValidator_SkipBudget_ResetsOnOK(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			toolCallResponse("nop", "tc1"),     // turn 0: tool call
			{Content: "done", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}}, // turn 1: text
		},
	}
	nop := nopTool("nop")

	callIdx := 0
	v := TurnValidatorFunc(func(_ context.Context, snap TurnSnapshot) Outcome {
		defer func() { callIdx++ }()
		if callIdx == 0 {
			// Tool-call turn: skip budget.
			return Outcome{OK: false, SkipBudget: true, Hint: "steer"}
		}
		// Text turn: accept.
		return Outcome{OK: true}
	})

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "sys" }, []*tool.Tool{nop},
		WithLoopMaxTurns(5),
		WithLoopTurnValidator(v, 1), // budget of 1; should never be consumed
	)

	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("expected completed, got %q", res.Status)
	}
	if res.Output != "done" {
		t.Errorf("expected output=done, got %q", res.Output)
	}
}

// TestValidator_RegularReject_StillExhausts is a regression guard: a regular
// rejection (OK=false, SkipBudget=false) must still exhaust the budget and
// abort with validation_exhausted.
func TestValidator_RegularReject_StillExhausts(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "bad 1", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}},
			{Content: "bad 2", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}},
			{Content: "bad 3", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}},
			{Content: "bad 4", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}},
		},
	}

	// Always reject without SkipBudget.
	alwaysBad := TurnValidatorFunc(func(_ context.Context, _ TurnSnapshot) Outcome {
		return Outcome{OK: false, SkipBudget: false, Reason: "nope", Hint: "try harder"}
	})

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "sys" }, nil,
		WithLoopMaxTurns(10),
		WithLoopTurnValidator(alwaysBad, 3), // 3 retries → 4 calls then exhausted
	)

	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "validation_exhausted" {
		t.Errorf("expected validation_exhausted, got %q", res.Status)
	}
}

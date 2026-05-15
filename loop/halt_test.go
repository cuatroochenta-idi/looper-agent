package loop

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// haltingTool returns a tool that calls tool.SetHalt(ctx) on every
// invocation, signalling the loop should terminate after this turn.
func haltingTool(name string) *tool.Tool {
	return tool.MustNewTool(struct{}{}, func(ctx context.Context, _ struct{}) (string, error) {
		tool.SetHalt(ctx)
		return "halt-requested", nil
	}, tool.ToolConfig{Name: name, Description: "halts the run", Parallel: true})
}

// countingTool increments a counter on every invocation; used to assert
// that no extra LLM calls happened after a Halt was raised.
func countingTool(name string, counter *atomic.Int32) *tool.Tool {
	return tool.MustNewTool(struct{}{}, func(_ context.Context, _ struct{}) (string, error) {
		counter.Add(1)
		return "ran", nil
	}, tool.ToolConfig{Name: name, Description: "increments counter", Parallel: true})
}

// TestToolResult_Halt_TerminatesRun: a tool that calls tool.SetHalt(ctx)
// must end the run with Status="halted_by_tool" in the SAME turn — the
// model is not called again, even when more turns remain in the budget.
func TestToolResult_Halt_TerminatesRun(t *testing.T) {
	// The first response calls the halting tool. The second response would
	// add another turn, but the halt must stop us before that.
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "halt_run", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{Content: "should not be reached", IsFinal: true, Usage: provider.Usage{OutputTokens: 1}},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{haltingTool("halt_run")},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "halted_by_tool" {
		t.Errorf("Status = %q, want halted_by_tool", res.Status)
	}
	// Only the first response was consumed — no second Chat call after halt.
	if prov.callCount != 1 {
		t.Errorf("provider.callCount = %d, want 1 (no extra LLM call after halt)", prov.callCount)
	}
}

// TestToolResult_Halt_ParallelCalls: among multiple parallel tool calls in
// one turn, one calls Halt. ALL results must still appear in history (no
// silent drops) and the run halts after the turn.
func TestToolResult_Halt_ParallelCalls(t *testing.T) {
	var ranCount atomic.Int32

	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "counter", Arguments: json.RawMessage(`{}`)},
					{ID: "tc2", Name: "halt_run", Arguments: json.RawMessage(`{}`)},
					{ID: "tc3", Name: "counter", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{Content: "should not be reached", IsFinal: true},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{
			haltingTool("halt_run"),
			countingTool("counter", &ranCount),
		},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "halted_by_tool" {
		t.Errorf("Status = %q, want halted_by_tool", res.Status)
	}
	// Both non-halting tools must have run — the loop did NOT drop sibling
	// results when one of them halted.
	if ranCount.Load() != 2 {
		t.Errorf("counter tool ran %d times, want 2 (both siblings must execute before halt takes effect)", ranCount.Load())
	}
	// Three tool results recorded in history (one per call).
	toolResultMsgs := 0
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageTool {
			toolResultMsgs++
		}
	}
	if toolResultMsgs != 3 {
		t.Errorf("history has %d tool-result messages, want 3 (all parallel calls preserved)", toolResultMsgs)
	}
	// Only the first provider response was consumed.
	if prov.callCount != 1 {
		t.Errorf("provider.callCount = %d, want 1", prov.callCount)
	}
}

// TestToolResult_Halt_NotSet_NormalCompletion: sanity check — a regular
// tool that does NOT call SetHalt must NOT terminate the run early.
func TestToolResult_Halt_NotSet_NormalCompletion(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "nop", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{Content: "all done", IsFinal: true, Usage: provider.Usage{OutputTokens: 2}},
		},
	}

	lp := NewAgentLoop(
		prov,
		func(_ context.Context) string { return "sys" },
		[]*tool.Tool{nopTool("nop")},
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want completed (no halt was requested)", res.Status)
	}
}

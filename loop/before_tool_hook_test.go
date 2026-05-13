package loop

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// fastTool is a tool whose Execute body increments an external counter so
// the test can tell whether the hook truly suppressed an invocation.
func fastTool(name string, counter *atomic.Int32) *tool.Tool {
	return tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) {
			counter.Add(1)
			return name + " ran", nil
		},
		tool.ToolConfig{Name: name, Description: name + " probe"},
	)
}

// streamingMockProvider drives a single tool-call response then a final
// response so executeToolCallsStreaming is exercised end-to-end.
func toolThenFinal(toolCalls []message.ToolCall, finalText string) *mockProvider {
	return &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{ToolCalls: toolCalls},
			{Content: finalText, IsFinal: true},
		},
	}
}

// TestBeforeToolHook_NoHook_RunsAllCalls asserts the legacy path is
// preserved when no hook is registered.
func TestBeforeToolHook_NoHook_RunsAllCalls(t *testing.T) {
	var aCount, bCount atomic.Int32
	a := fastTool("a", &aCount)
	b := fastTool("b", &bCount)

	prov := toolThenFinal(
		[]message.ToolCall{
			{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)},
			{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)},
		},
		"done",
	)
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" },
		[]*tool.Tool{a, b})

	if _, err := lp.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if aCount.Load() != 1 || bCount.Load() != 1 {
		t.Errorf("expected both tools to run once, got a=%d b=%d", aCount.Load(), bCount.Load())
	}
}

// TestBeforeToolHook_CancelSuppressesExecution asserts that a hook calling
// Cancel(callID, reason) prevents the tool function from running and
// inserts an error tool_result the LLM can read.
func TestBeforeToolHook_CancelSuppressesExecution(t *testing.T) {
	var aCount, bCount atomic.Int32
	a := fastTool("a", &aCount)
	b := fastTool("b", &bCount)

	prov := toolThenFinal(
		[]message.ToolCall{
			{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)},
			{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)},
		},
		"done",
	)

	hookCalls := 0
	hook := func(_ context.Context, params *ToolExecutionParams) error {
		hookCalls++
		params.Cancel("1", "rate limited")
		return nil
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" },
		[]*tool.Tool{a, b})
	lp.HookManager().OnBeforeToolExecution(hook)

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if hookCalls != 1 {
		t.Errorf("expected hook called once, got %d", hookCalls)
	}
	if aCount.Load() != 0 {
		t.Errorf("tool 'a' should have been cancelled, got %d executions", aCount.Load())
	}
	if bCount.Load() != 1 {
		t.Errorf("tool 'b' should have run, got %d executions", bCount.Load())
	}

	// The history must contain an error tool_result for call 1 with the
	// reason so the model can self-correct.
	found := false
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageTool && m.ToolID == "1" {
			if !strings.Contains(m.Content, "rate limited") {
				t.Errorf("cancelled tool_result should carry the reason, got %q", m.Content)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected a tool_result for the cancelled call in history")
	}
}

// TestBeforeToolHook_ReplaceSwapsCall asserts that Replace(callID, newCall)
// substitutes the tool that actually runs, while keeping the original
// callID so the model's bookkeeping stays consistent.
func TestBeforeToolHook_ReplaceSwapsCall(t *testing.T) {
	var aCount, bCount atomic.Int32
	a := fastTool("a", &aCount)
	b := fastTool("b", &bCount)

	prov := toolThenFinal(
		[]message.ToolCall{{ID: "X", Name: "a", Arguments: json.RawMessage(`{}`)}},
		"done",
	)

	hook := func(_ context.Context, params *ToolExecutionParams) error {
		params.Replace("X", message.ToolCall{
			ID:        "X", // preserved so the LLM's reference stays valid
			Name:      "b",
			Arguments: json.RawMessage(`{}`),
		})
		return nil
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" },
		[]*tool.Tool{a, b})
	lp.HookManager().OnBeforeToolExecution(hook)

	if _, err := lp.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if aCount.Load() != 0 {
		t.Errorf("original 'a' should not run, got %d", aCount.Load())
	}
	if bCount.Load() != 1 {
		t.Errorf("replacement 'b' should run once, got %d", bCount.Load())
	}
}

// TestBeforeToolHook_HooksComposeSequentially asserts that when multiple
// hooks are registered, each sees the mutations the previous one made.
// This lets a logger hook record decisions made by a guard hook.
func TestBeforeToolHook_HooksComposeSequentially(t *testing.T) {
	var aCount atomic.Int32
	a := fastTool("a", &aCount)

	prov := toolThenFinal(
		[]message.ToolCall{{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}},
		"done",
	)

	var mu sync.Mutex
	var seenByLogger string

	guard := func(_ context.Context, p *ToolExecutionParams) error {
		p.Cancel("1", "policy violation")
		return nil
	}
	logger := func(_ context.Context, p *ToolExecutionParams) error {
		mu.Lock()
		defer mu.Unlock()
		if reason, ok := p.Cancellations()["1"]; ok {
			seenByLogger = reason
		}
		return nil
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" },
		[]*tool.Tool{a})
	lp.HookManager().OnBeforeToolExecution(guard)
	lp.HookManager().OnBeforeToolExecution(logger)

	if _, err := lp.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if aCount.Load() != 0 {
		t.Errorf("expected guard cancellation to win, got %d executions", aCount.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if seenByLogger != "policy violation" {
		t.Errorf("logger should observe the guard's cancellation reason, got %q", seenByLogger)
	}
}

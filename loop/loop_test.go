package loop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// mockProvider implements provider.LLMProvider for testing.
type mockProvider struct {
	mu        sync.Mutex
	model     string
	responses []*provider.LLMResponse
	callCount int
}

func (m *mockProvider) Model() string { return m.model }

func (m *mockProvider) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callCount >= len(m.responses) {
		return &provider.LLMResponse{Content: "done", IsFinal: true, Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := m.Chat(ctx, req)
		ch <- provider.StreamChunk{
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
			IsFinal:   resp.IsFinal,
			Usage:     &resp.Usage,
		}
	}()
	return ch, nil
}

func (m *mockProvider) Translator() provider.Translator { return nil }

// --- Tests ---

func TestAgentLoopSimpleRun(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "Hello! How can I help?", IsFinal: true, Usage: provider.Usage{InputTokens: 5, OutputTokens: 5}},
		},
	}

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test prompt" }, nil)

	result, err := loop.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "Hello! How can I help?" {
		t.Errorf("expected output, got %q", result.Output)
	}
	if result.Turns != 1 {
		t.Errorf("expected 1 turn, got %d", result.Turns)
	}
}

func TestAgentLoopWithTools(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "add", Arguments: json.RawMessage(`{"a":1,"b":2}`)},
				},
				Usage: provider.Usage{InputTokens: 10, OutputTokens: 5},
			},
			{
				Content: "The result is 3",
				IsFinal: true,
				Usage:   provider.Usage{InputTokens: 15, OutputTokens: 5},
			},
		},
	}

	addTool := tool.NewTool(struct {
		A int `json:"a" jsonschema:"required"`
		B int `json:"b" jsonschema:"required"`
	}{}, func(ctx context.Context, input struct {
		A int `json:"a" jsonschema:"required"`
		B int `json:"b" jsonschema:"required"`
	}) (string, error) {
		return "3", nil
	}, tool.ToolConfig{
		Name:        "add",
		Description: "Adds two numbers",
		Parallel:    true,
	})

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "math helper" }, []*tool.Tool{addTool})

	result, err := loop.Run(context.Background(), "1+2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "The result is 3" {
		t.Errorf("expected output, got %q", result.Output)
	}
	if result.Turns != 2 {
		t.Errorf("expected 2 turns, got %d", result.Turns)
	}
}

func TestAgentLoopToolErrorFeedback(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "risky_op", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{
				Content: "The operation failed, let me try another approach.",
				IsFinal: true,
				Usage:   provider.Usage{InputTokens: 10, OutputTokens: 10},
			},
		},
	}

	riskyTool := tool.NewTool(struct{}{}, func(ctx context.Context, input struct{}) (string, error) {
		return "", context.DeadlineExceeded
	}, tool.ToolConfig{
		Name:        "risky_op",
		Description: "A risky operation",
		Parallel:    false,
		Retries:     0,
	})

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "helper" }, []*tool.Tool{riskyTool})

	result, err := loop.Run(context.Background(), "do risky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History should contain the error feedback
	msgs := result.History.Messages()
	foundError := false
	for _, msg := range msgs {
		if msg.Type == message.MessageTool && msg.Content != "" {
			t.Logf("tool result: %s", msg.Content)
			foundError = true
		}
	}
	if !foundError {
		t.Error("expected error feedback in tool results")
	}
}

func TestAgentLoopHookExecution(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "final", IsFinal: true, Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test" }, nil)

	beforeCalled := false
	afterCalled := false

	loop.HookManager().On(HookBeforeCall, func(ctx context.Context, params *CallParams) error {
		beforeCalled = true
		return nil
	})
	loop.HookManager().On(HookAfterCall, func(ctx context.Context, params *CallParams) error {
		afterCalled = true
		return nil
	})

	_, err := loop.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !beforeCalled {
		t.Error("BeforeCall hook was not called")
	}
	if !afterCalled {
		t.Error("AfterCall hook was not called")
	}
}

func TestAgentLoopHookAbort(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "final", IsFinal: true, Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test" }, nil)

	loop.HookManager().On(HookBeforeCall, func(ctx context.Context, params *CallParams) error {
		return &testError{"abort by hook"}
	})

	_, err := loop.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error from hook abort")
	}
}

func TestAgentLoopMaxTurns(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{ToolCalls: []message.ToolCall{{ID: "tc1", Name: "echo", Arguments: json.RawMessage(`{"msg":"1"}`)}}, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{ToolCalls: []message.ToolCall{{ID: "tc2", Name: "echo", Arguments: json.RawMessage(`{"msg":"2"}`)}}, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
			{ToolCalls: []message.ToolCall{{ID: "tc3", Name: "echo", Arguments: json.RawMessage(`{"msg":"3"}`)}}, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}

	echoTool := tool.NewTool(struct {
		Msg string `json:"msg" jsonschema:"required"`
	}{}, func(ctx context.Context, input struct {
		Msg string `json:"msg" jsonschema:"required"`
	}) (string, error) {
		return input.Msg, nil
	}, tool.ToolConfig{Name: "echo", Description: "Echo", Parallel: true})

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test" }, []*tool.Tool{echoTool},
		WithLoopMaxTurns(2),
	)

	_, err := loop.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected max turns exceeded error")
	}
	t.Logf("expected error: %v", err)
}

func TestAgentLoopParallelToolExecution(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "fast", Arguments: json.RawMessage(`{}`)},
					{ID: "tc2", Name: "fast", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{Content: "done", IsFinal: true, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}},
		},
	}

	fastTool := tool.NewTool(struct{}{}, func(ctx context.Context, input struct{}) (string, error) {
		return "fast-result", nil
	}, tool.ToolConfig{Name: "fast", Description: "Fast tool", Parallel: true})

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test" }, []*tool.Tool{fastTool})

	result, err := loop.Run(context.Background(), "do parallel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Turns != 2 {
		t.Errorf("expected 2 turns, got %d", result.Turns)
	}
}

func TestAgentLoopUnknownTool(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				ToolCalls: []message.ToolCall{
					{ID: "tc1", Name: "nonexistent", Arguments: json.RawMessage(`{}`)},
				},
				Usage: provider.Usage{InputTokens: 5, OutputTokens: 3},
			},
			{Content: "I cannot use that tool", IsFinal: true, Usage: provider.Usage{InputTokens: 5, OutputTokens: 5}},
		},
	}

	loop := NewAgentLoop(prov, func(ctx context.Context) string { return "test" }, nil)

	result, err := loop.Run(context.Background(), "use unknown tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History should contain error feedback for unknown tool
	msgs := result.History.Messages()
	foundUnknown := false
	for _, msg := range msgs {
		if msg.Type == message.MessageTool {
			t.Logf("tool result content: %s", msg.Content)
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Error("expected error feedback for unknown tool")
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

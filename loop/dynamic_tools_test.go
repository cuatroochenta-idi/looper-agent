package loop

import (
	"context"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// recordingMockProvider captures the LLMRequest of every Chat / ChatStream
// call so dynamic-tools tests can assert what tool slice the loop actually
// sent to the model on each turn.
type recordingMockProvider struct {
	mockProvider
	requests []provider.LLMRequest
}

func (m *recordingMockProvider) Chat(ctx context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	m.requests = append(m.requests, req)
	return m.mockProvider.Chat(ctx, req)
}

func (m *recordingMockProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	m.requests = append(m.requests, req)
	return m.mockProvider.ChatStream(ctx, req)
}

func newProbeTool(name string) *tool.Tool {
	return tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "ok", nil },
		tool.ToolConfig{Name: name, Description: name + " probe"},
	)
}

// TestDynamicTools_FiltersPerTurn asserts that WithLoopDynamicTools is
// consulted on each turn and its returned list overrides the static tools.
// The model receives only what the function returns, so allowlists keyed
// on conversation state can hide tools the model shouldn't see yet.
func TestDynamicTools_FiltersPerTurn(t *testing.T) {
	prov := &recordingMockProvider{
		mockProvider: mockProvider{
			model: "mock",
			responses: []*provider.LLMResponse{
				{Content: "first", IsFinal: true},
			},
		},
	}

	staticTools := []*tool.Tool{newProbeTool("a"), newProbeTool("b"), newProbeTool("c")}

	dyn := func(_ context.Context, _ *message.History) []*tool.Tool {
		// Hide tool "c" — discovery-phase allowlist.
		return []*tool.Tool{staticTools[0], staticTools[1]}
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, staticTools,
		WithLoopDynamicTools(dyn))

	if _, err := lp.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(prov.requests))
	}
	sent := prov.requests[0].Tools
	if len(sent) != 2 {
		t.Errorf("expected dynamic filter to drop tool 'c', got %d tools sent", len(sent))
	}
	for _, tl := range sent {
		if tl.Name() == "c" {
			t.Errorf("tool 'c' should have been filtered out, got it in the request")
		}
	}
}

// TestDynamicTools_NotConfigured_UsesStaticList asserts the legacy path is
// unchanged when no dynamic function is registered.
func TestDynamicTools_NotConfigured_UsesStaticList(t *testing.T) {
	prov := &recordingMockProvider{
		mockProvider: mockProvider{
			model:     "mock",
			responses: []*provider.LLMResponse{{Content: "ok", IsFinal: true}},
		},
	}
	staticTools := []*tool.Tool{newProbeTool("alpha"), newProbeTool("beta")}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, staticTools)

	if _, err := lp.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prov.requests[0].Tools) != 2 {
		t.Errorf("expected static list of 2 tools, got %d", len(prov.requests[0].Tools))
	}
}

// TestDynamicTools_ChangesAcrossTurns asserts the function is re-invoked
// on each turn so it can react to history changes (the "phase" use case).
func TestDynamicTools_ChangesAcrossTurns(t *testing.T) {
	prov := &recordingMockProvider{
		mockProvider: mockProvider{
			model: "mock",
			responses: []*provider.LLMResponse{
				{Content: "", ToolCalls: []message.ToolCall{{ID: "t1", Name: "alpha", Arguments: []byte(`{}`)}}},
				{Content: "done", IsFinal: true},
			},
		},
	}

	staticTools := []*tool.Tool{newProbeTool("alpha"), newProbeTool("beta")}

	turnCalled := 0
	dyn := func(_ context.Context, _ *message.History) []*tool.Tool {
		turnCalled++
		// First turn: both tools available. Second turn: lock down to beta only.
		if turnCalled == 1 {
			return staticTools
		}
		return staticTools[1:]
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, staticTools,
		WithLoopDynamicTools(dyn))

	if _, err := lp.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if turnCalled != 2 {
		t.Errorf("expected dynamic-tools func to be called once per turn, got %d", turnCalled)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(prov.requests))
	}
	if len(prov.requests[0].Tools) != 2 {
		t.Errorf("turn 0: expected 2 tools, got %d", len(prov.requests[0].Tools))
	}
	if len(prov.requests[1].Tools) != 1 || prov.requests[1].Tools[0].Name() != "beta" {
		t.Errorf("turn 1: expected only 'beta', got %d tools (first=%v)",
			len(prov.requests[1].Tools),
			func() string {
				if len(prov.requests[1].Tools) == 0 {
					return "<empty>"
				}
				return prov.requests[1].Tools[0].Name()
			}())
	}
}

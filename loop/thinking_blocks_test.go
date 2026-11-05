package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

var signedThinking = []message.ThinkingBlock{
	{Provider: "anthropic", Type: "thinking", Signature: "sig-1"},
	{Provider: "anthropic", Type: "redacted_thinking", Data: "opaque"},
}

func thinkingToolTurn() *mockProvider {
	return &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{
				IsFinal:        true,
				ThinkingBlocks: signedThinking,
				ToolCalls:      []message.ToolCall{{ID: "tc1", Name: "noop", Arguments: json.RawMessage(`{}`)}},
			},
			{Content: "done", IsFinal: true},
		},
	}
}

func noopTool() *tool.Tool {
	return tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "ok", nil },
		tool.ToolConfig{Name: "noop", Description: "does nothing"},
	)
}

// assertThinkingKept checks that the tool-call turn kept its thinking blocks
// both in the prompt history and in the run's append journal.
func assertThinkingKept(t *testing.T, history *message.History, journal []message.Message) {
	t.Helper()

	for name, msgs := range map[string][]message.Message{"history": history.Messages(), "NewMessages": journal} {
		var found bool
		for _, m := range msgs {
			if m.Type != message.MessageAssistant || len(m.ToolCalls) == 0 {
				continue
			}
			found = true
			if len(m.ThinkingBlocks) != len(signedThinking) || m.ThinkingBlocks[0] != signedThinking[0] || m.ThinkingBlocks[1] != signedThinking[1] {
				t.Errorf("%s: assistant ThinkingBlocks = %+v, want %+v", name, m.ThinkingBlocks, signedThinking)
			}
		}
		if !found {
			t.Errorf("%s: no assistant tool-call turn recorded", name)
		}
	}
}

func TestRun_KeepsThinkingBlocksOnTheAssistantTurn(t *testing.T) {
	lp := NewAgentLoop(thinkingToolTurn(), func(_ context.Context) string { return "sys" }, []*tool.Tool{noopTool()})

	res, err := lp.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertThinkingKept(t, res.History, res.NewMessages)
}

func TestIterate_KeepsThinkingBlocksOnTheAssistantTurn(t *testing.T) {
	lp := NewAgentLoop(thinkingToolTurn(), func(_ context.Context) string { return "sys" }, []*tool.Tool{noopTool()})

	it := lp.Iterate(context.Background(), "go")
	for range it.Next() {
	}
	res := it.Result()
	assertThinkingKept(t, res.History, res.NewMessages)
}

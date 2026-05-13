package loop

import (
	"context"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// TestRun_EmptyInputWithHistory_DoesNotAppendPhantomUserMsg asserts that
// callers can pre-populate History (e.g. with a multi-modal Parts message)
// and run the agent with input="" without the loop injecting a phantom
// empty user message at the end of the conversation.
func TestRun_EmptyInputWithHistory_DoesNotAppendPhantomUserMsg(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{{Content: "ok", IsFinal: true}},
	}
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil)

	hist := message.NewHistory()
	hist.AddUserMessageParts(
		message.TextPart("what's in this image?"),
		message.ImageURLPart("https://x/y.png"),
	)

	res, err := lp.Run(context.Background(), "", WithHistory(hist))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count user messages: should be exactly the one we added, not two.
	userMsgs := 0
	var trailing message.Message
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageUser {
			userMsgs++
			trailing = m
		}
	}
	if userMsgs != 1 {
		t.Errorf("expected 1 user message, got %d (loop injected a phantom)", userMsgs)
	}
	if len(trailing.Parts) != 2 {
		t.Errorf("trailing user message should preserve the original Parts (text+image), got %d Parts", len(trailing.Parts))
	}
}

// TestIterate_EmptyInputWithHistory_DoesNotAppendPhantomUserMsg pins the
// same invariant on the streaming Iterator path, which agent.Run uses.
func TestIterate_EmptyInputWithHistory_DoesNotAppendPhantomUserMsg(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{{Content: "ok", IsFinal: true}},
	}
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil)

	hist := message.NewHistory()
	hist.AddUserMessageParts(message.TextPart("describe"), message.ImageURLPart("https://x/y.png"))

	it := lp.Iterate(context.Background(), "", WithHistory(hist))
	for range it.Next() { //nolint:revive // drain only
	}
	res := it.Result()

	userMsgs := 0
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageUser {
			userMsgs++
		}
	}
	if userMsgs != 1 {
		t.Errorf("streaming path: expected 1 user message, got %d", userMsgs)
	}
}

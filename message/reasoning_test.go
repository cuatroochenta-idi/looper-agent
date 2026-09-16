package message

import (
	"encoding/json"
	"testing"
)

func TestNewAssistantMessageWithReasoning(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"think","signature":"sha256:abc"}]`)
	calls := []ToolCall{{ID: "c1", Name: "search", Arguments: json.RawMessage(`{"q":"x"}`)}}

	m := NewAssistantMessageWithReasoning("answer", calls, "think", details)

	if m.Type != MessageAssistant {
		t.Errorf("Type = %s, want assistant", m.Type)
	}
	if m.Content != "answer" {
		t.Errorf("Content = %q", m.Content)
	}
	if m.Reasoning != "think" {
		t.Errorf("Reasoning = %q", m.Reasoning)
	}
	if string(m.ReasoningDetails) != string(details) {
		t.Errorf("ReasoningDetails = %s, want %s", m.ReasoningDetails, details)
	}
	if len(m.ToolCalls) != 1 {
		t.Errorf("ToolCalls = %v", m.ToolCalls)
	}
}

// TestExistingConstructorsLeaveReasoningEmpty pins the additive contract:
// callers that never asked for reasoning keep serializing exactly as before.
func TestExistingConstructorsLeaveReasoningEmpty(t *testing.T) {
	m := NewAssistantMessage("answer", nil)
	if m.Reasoning != "" || m.ReasoningDetails != nil {
		t.Errorf("NewAssistantMessage populated reasoning: %q / %s", m.Reasoning, m.ReasoningDetails)
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"reasoning", "reasoning_details"} {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := decoded[key]; present {
			t.Errorf("empty message serialized %q: %s", key, raw)
		}
	}
}

// TestReasoningDetailsRoundTripPreservesBytes is the one that matters for
// echo-back: OpenRouter refuses a re-encoded or resequenced array, so the
// stored bytes must survive a persistence round trip untouched.
func TestReasoningDetailsRoundTripPreservesBytes(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"a\nb","signature":"sha256:AAA=","id":"r-1","format":"anthropic-claude-v1","index":0},{"type":"reasoning.encrypted","data":"Zm9vYmFy","index":1}]`)
	m := NewAssistantMessageWithReasoning("", nil, "a\nb", details)

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(back.ReasoningDetails) != string(details) {
		t.Errorf("details changed across the round trip:\n got: %s\nwant: %s", back.ReasoningDetails, details)
	}
	if back.Reasoning != "a\nb" {
		t.Errorf("Reasoning = %q", back.Reasoning)
	}
}

func TestHistoryAddAssistantMessageWithReasoning(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"t"}]`)
	h := NewHistory()
	h.AddAssistantMessageWithReasoning("answer", nil, "t", details)

	msgs := h.Messages()
	if len(msgs) != 1 {
		t.Fatalf("history has %d messages, want 1", len(msgs))
	}
	if msgs[0].Reasoning != "t" || string(msgs[0].ReasoningDetails) != string(details) {
		t.Errorf("stored message lost its reasoning: %q / %s", msgs[0].Reasoning, msgs[0].ReasoningDetails)
	}
}

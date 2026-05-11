package message

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage(MessageUser, "hello")
	if msg.Type != MessageUser {
		t.Errorf("expected type %s, got %s", MessageUser, msg.Type)
	}
	if msg.Content != "hello" {
		t.Errorf("expected content 'hello', got %q", msg.Content)
	}
	if msg.ID == "" {
		t.Error("expected non-empty ID")
	}
	if msg.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestNewUserMessage(t *testing.T) {
	msg := NewUserMessage("hi")
	if msg.Type != MessageUser {
		t.Errorf("expected MessageUser, got %s", msg.Type)
	}
}

func TestNewAssistantMessage(t *testing.T) {
	tcs := []ToolCall{{ID: "tc1", Name: "search", Arguments: json.RawMessage(`{"q":"x"}`)}}
	msg := NewAssistantMessage("thinking", tcs)
	if msg.Type != MessageAssistant {
		t.Errorf("expected MessageAssistant, got %s", msg.Type)
	}
	if len(msg.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
}

func TestNewToolResult(t *testing.T) {
	msg := NewToolResult("call-1", "search", "found it", false)
	if msg.Type != MessageTool {
		t.Errorf("expected MessageTool, got %s", msg.Type)
	}
	if msg.ToolID != "call-1" {
		t.Errorf("expected ToolID call-1, got %s", msg.ToolID)
	}
	if msg.Name != "search" {
		t.Errorf("expected Name search, got %s", msg.Name)
	}
}

func TestNewToolResultError(t *testing.T) {
	msg := NewToolResult("call-1", "search", "rate limit", true)
	if msg.Type != MessageTool {
		t.Errorf("expected MessageTool, got %s", msg.Type)
	}
}

func TestNewSystemMessage(t *testing.T) {
	msg := NewSystemMessage("context")
	if msg.Type != MessageSystem {
		t.Errorf("expected MessageSystem, got %s", msg.Type)
	}
}

func TestHistoryNew(t *testing.T) {
	h := NewHistory()
	if h.Len() != 0 {
		t.Errorf("expected empty history, got %d messages", h.Len())
	}
	if h.LastMessage() != nil {
		t.Error("expected nil LastMessage")
	}
}

func TestHistoryAddMessages(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("hello")
	h.AddAssistantMessage("hi there", nil)
	h.AddSystemMessage("debug info")

	if h.Len() != 3 {
		t.Errorf("expected 3 messages, got %d", h.Len())
	}
	if h.TurnCount() != 1 {
		t.Errorf("expected 1 turn, got %d", h.TurnCount())
	}
}

func TestHistoryAddToolResult(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("search for Go")
	h.AddAssistantMessage("", []ToolCall{
		{ID: "tc1", Name: "search", Arguments: json.RawMessage(`{}`)},
	})
	h.AddToolResult("tc1", "search", "found 3 results", false)

	if h.Len() != 3 {
		t.Errorf("expected 3 messages, got %d", h.Len())
	}
	last := h.LastMessage()
	if last.Type != MessageTool {
		t.Errorf("expected MessageTool, got %s", last.Type)
	}
}

func TestHistoryUpdate(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("my email is john@example.com")

	h.Update(0, func(m *Message) {
		m.Content = "my email is [REDACTED]"
	})

	msgs := h.Messages()
	if msgs[0].Content != "my email is [REDACTED]" {
		t.Errorf("expected redacted, got %q", msgs[0].Content)
	}
}

func TestHistoryRemove(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("a")
	h.AddUserMessage("b")
	h.AddUserMessage("c")

	h.Remove(1)

	if h.Len() != 2 {
		t.Errorf("expected 2 messages after remove, got %d", h.Len())
	}
	msgs := h.Messages()
	if msgs[1].Content != "c" {
		t.Errorf("expected 'c' at index 1, got %q", msgs[1].Content)
	}
}

func TestHistoryRemoveOutOfBounds(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("a")
	h.Remove(5)  // no-op
	h.Remove(-1) // no-op
	if h.Len() != 1 {
		t.Errorf("expected 1 message, got %d", h.Len())
	}
}

func TestHistoryInsertBefore(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("a")
	h.AddUserMessage("c")

	h.InsertBefore(1, NewSystemMessage("b"))

	msgs := h.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[1].Content != "b" || msgs[1].Type != MessageSystem {
		t.Errorf("inserted message wrong: %+v", msgs[1])
	}
}

func TestHistoryTruncate(t *testing.T) {
	h := NewHistory()
	for i := 0; i < 10; i++ {
		h.AddUserMessage("msg")
	}
	h.Truncate(3)
	if h.Len() != 3 {
		t.Errorf("expected 3 messages, got %d", h.Len())
	}
}

func TestHistoryTruncateZero(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("a")
	h.Truncate(0)
	if h.Len() != 0 {
		t.Errorf("expected 0 messages, got %d", h.Len())
	}
}

func TestHistoryTurnCount(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("q1")
	h.AddAssistantMessage("a1", nil)
	h.AddUserMessage("q2")
	h.AddAssistantMessage("a2", nil)
	h.AddSystemMessage("ctx")

	if h.TurnCount() != 2 {
		t.Errorf("expected 2 turns, got %d", h.TurnCount())
	}
}

func TestHistoryMarshalRoundtrip(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("hello")
	h.AddAssistantMessage("hi", []ToolCall{
		{ID: "tc1", Name: "test", Arguments: json.RawMessage(`{"key":"val"}`)},
	})
	h.AddToolResult("tc1", "test", "done", false)

	data, err := h.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	restored, err := UnmarshalHistory(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if restored.Len() != h.Len() {
		t.Errorf("length mismatch: %d vs %d", restored.Len(), h.Len())
	}

	orig := h.Messages()
	rest := restored.Messages()
	for i := range orig {
		if orig[i].ID != rest[i].ID {
			t.Errorf("message %d: ID mismatch", i)
		}
		if orig[i].Type != rest[i].Type {
			t.Errorf("message %d: type mismatch", i)
		}
		if orig[i].Content != rest[i].Content {
			t.Errorf("message %d: content mismatch", i)
		}
		if len(orig[i].ToolCalls) != len(rest[i].ToolCalls) {
			t.Errorf("message %d: tool calls length mismatch", i)
		}
	}
}

func TestHistoryExport(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("a")
	h.AddUserMessage("b")

	export := h.Export()
	if len(export) != 2 {
		t.Errorf("expected 2 messages in export, got %d", len(export))
	}

	// Export should be a copy
	export[0].Content = "modified"
	msgs := h.Messages()
	if msgs[0].Content == "modified" {
		t.Error("export modification affected original history")
	}
}

func TestHistoryMessagesIsCopy(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("original")

	msgs := h.Messages()
	msgs[0].Content = "modified"

	msgs2 := h.Messages()
	if msgs2[0].Content != "original" {
		t.Error("Messages() should return a copy")
	}
}

func TestHistoryConcurrency(t *testing.T) {
	h := NewHistory()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.AddUserMessage("msg")
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Messages()
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.TurnCount()
		}()
	}

	wg.Wait()

	if h.Len() != 100 {
		t.Errorf("expected 100 messages, got %d", h.Len())
	}
}

func TestMessageMetadata(t *testing.T) {
	msg := Message{
		ID:        "test-id",
		Type:      MessageUser,
		Content:   "hello",
		CreatedAt: time.Now(),
		Metadata: map[string]any{
			"user_id":    "abc123",
			"session_id": "sess-456",
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if restored.Metadata["user_id"] != "abc123" {
		t.Error("metadata not preserved")
	}
}

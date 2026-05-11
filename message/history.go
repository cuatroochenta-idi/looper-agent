package message

import (
	"encoding/json"
	"fmt"
	"sync"
)

// History manages the ordered sequence of messages in a conversation.
// It is thread-safe and directly serializable to JSON for persistence
// in any storage backend (SQL, NoSQL, Redis, filesystem).
//
// Important: the agent's base system prompt is NOT stored in History.
// It is resolved per call and injected at translation time by each provider.
// Only system messages added by hooks or middleware are persisted.
type History struct {
	mu       sync.RWMutex
	messages []Message
}

// NewHistory creates an empty conversation history.
func NewHistory() *History {
	return &History{
		messages: make([]Message, 0),
	}
}

// Messages returns a copy of all messages. Safe to read, not to modify.
// Use Update, Remove, or InsertBefore for mutations.
func (h *History) Messages() []Message {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make([]Message, len(h.messages))
	copy(cp, h.messages)
	return cp
}

// Len returns the number of messages.
func (h *History) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.messages)
}

// LastMessage returns a pointer to the last message, or nil if empty.
func (h *History) LastMessage() *Message {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.messages) == 0 {
		return nil
	}
	cp := h.messages[len(h.messages)-1]
	return &cp
}

// AddUserMessage appends a user message.
func (h *History) AddUserMessage(content string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, NewUserMessage(content))
}

// AddAssistantMessage appends an assistant message with optional tool calls.
func (h *History) AddAssistantMessage(content string, toolCalls []ToolCall) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, NewAssistantMessage(content, toolCalls))
}

// AddToolResult appends a tool result message.
func (h *History) AddToolResult(callID, name, content string, isError bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, NewToolResult(callID, name, content, isError))
}

// AddSystemMessage appends a system message from hooks or middleware.
// This is distinct from the agent's base system prompt, which lives outside History.
func (h *History) AddSystemMessage(content string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, NewSystemMessage(content))
}

// AddMessage appends a pre-built message.
func (h *History) AddMessage(msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, msg)
}

// Update applies a mutation function to the message at the given index.
// Used by hooks and middleware (e.g., redact PII).
func (h *History) Update(index int, fn func(m *Message)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index >= 0 && index < len(h.messages) {
		fn(&h.messages[index])
	}
}

// Remove deletes the message at the given index.
func (h *History) Remove(index int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index < 0 || index >= len(h.messages) {
		return
	}
	h.messages = append(h.messages[:index], h.messages[index+1:]...)
}

// InsertBefore inserts a message before the given index.
func (h *History) InsertBefore(index int, msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index < 0 || index > len(h.messages) {
		return
	}
	h.messages = append(h.messages[:index], append([]Message{msg}, h.messages[index:]...)...)
}

// Truncate keeps only the last maxMessages.
func (h *History) Truncate(maxMessages int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if maxMessages <= 0 {
		h.messages = nil
		return
	}
	if len(h.messages) > maxMessages {
		h.messages = h.messages[len(h.messages)-maxMessages:]
	}
}

// TurnCount returns the number of user turns (user messages).
func (h *History) TurnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	turns := 0
	for _, m := range h.messages {
		if m.Type == MessageUser {
			turns++
		}
	}
	return turns
}

// MarshalJSON serializes history to JSON.
func (h *History) MarshalJSON() ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return json.Marshal(h.messages)
}

// UnmarshalJSON restores history from JSON.
func (h *History) UnmarshalJSON(data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return json.Unmarshal(data, &h.messages)
}

// UnmarshalHistory restores a History from serialized JSON.
func UnmarshalHistory(data []byte) (*History, error) {
	h := NewHistory()
	if err := h.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("unmarshal history: %w", err)
	}
	return h, nil
}

// Export returns a copy of all messages for debugging or audit.
func (h *History) Export() []Message {
	return h.Messages()
}

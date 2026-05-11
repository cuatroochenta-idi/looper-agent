// Package message provides provider-agnostic message types and conversation
// history management for the Looper Agent framework.
//
// Messages are defined once in a universal format and each provider's
// Translator converts them to its native API format. The user never
// interacts with provider-specific message types.
//
// Important: the agent's base system prompt is NOT stored in History.
// It is resolved per call and injected at translation time by each provider.
// Only system messages added by hooks or middleware are persisted.
package message

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MessageType identifies the role of a message in the conversation.
type MessageType string

const (
	// MessageSystem represents a system message injected by hooks or middleware.
	// The agent's base system prompt lives outside History and is injected
	// at provider translation time.
	MessageSystem MessageType = "system"

	// MessageUser represents a message from the end user.
	MessageUser MessageType = "user"

	// MessageAssistant represents a response from the LLM, possibly
	// containing tool calls.
	MessageAssistant MessageType = "assistant"

	// MessageTool represents the result of a tool execution.
	MessageTool MessageType = "tool"
)

// Message is the universal, provider-agnostic representation of a
// conversation message. It serializes directly to JSON for persistence
// in any storage backend (SQL, NoSQL, Redis, filesystem).
type Message struct {
	ID        string         `json:"id"`
	Type      MessageType    `json:"type"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []ToolCall     `json:"tool_calls,omitempty"`
	ToolID    string         `json:"tool_id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// NewMessage creates a new message with a unique ID and current timestamp.
func NewMessage(msgType MessageType, content string) Message {
	return Message{
		ID:        uuid.New().String(),
		Type:      msgType,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
}

// NewUserMessage creates a new user message.
func NewUserMessage(content string) Message {
	return NewMessage(MessageUser, content)
}

// NewAssistantMessage creates a new assistant message with optional tool calls.
func NewAssistantMessage(content string, toolCalls []ToolCall) Message {
	m := NewMessage(MessageAssistant, content)
	m.ToolCalls = toolCalls
	return m
}

// NewToolResult creates a new tool result message.
func NewToolResult(callID, name, content string, isError bool) Message {
	m := NewMessage(MessageTool, content)
	m.ToolID = callID
	m.Name = name
	return m
}

// NewSystemMessage creates a new system message (from hooks/middleware).
func NewSystemMessage(content string) Message {
	return NewMessage(MessageSystem, content)
}

// ToolCall represents a tool invocation requested by the assistant.
type ToolCall struct {
	// ID uniquely identifies this tool call within a turn.
	ID string `json:"id"`

	// Name is the name of the tool to invoke.
	Name string `json:"name"`

	// Arguments is the JSON-encoded input to the tool.
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult represents the output of a tool execution.
type ToolResult struct {
	// ToolCallID matches the ToolCall.ID that this result is for.
	ToolCallID string `json:"tool_call_id"`

	// Content is the string output from the tool.
	Content string `json:"content"`

	// IsError indicates whether the tool execution resulted in an error.
	// When true, the agent interprets this as feedback for self-correction
	// rather than a fatal exception.
	IsError bool `json:"is_error"`
}

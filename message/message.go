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
//
// Parts is the source of truth for content: every constructor populates it,
// and every Translator reads it. Content is a derived, plain-text view kept
// for backward compatibility with callers that hash or log the textual
// portion of a message (e.g. legacy memory strategies). When the message is
// multi-modal, Content carries only the concatenated text Parts.
type Message struct {
	ID        string         `json:"id"`
	Type      MessageType    `json:"type"`
	Content   string         `json:"content,omitempty"`
	Parts     []Part         `json:"parts,omitempty"`
	ToolCalls []ToolCall     `json:"tool_calls,omitempty"`
	ToolID    string         `json:"tool_id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// NewMessage creates a new text-only message with a unique ID and current
// timestamp. Parts is synthesized from Content so every consumer can read
// from Parts uniformly without checking which constructor was used.
func NewMessage(msgType MessageType, content string) Message {
	m := Message{
		ID:        uuid.New().String(),
		Type:      msgType,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if content != "" {
		m.Parts = []Part{TextPart(content)}
	}
	return m
}

// NewMessageWithParts creates a new multi-modal message from an explicit
// list of Parts. Content is derived as the newline-joined concatenation of
// the text parts so legacy consumers that read Content keep working.
func NewMessageWithParts(msgType MessageType, parts ...Part) Message {
	return Message{
		ID:        uuid.New().String(),
		Type:      msgType,
		Parts:     append([]Part(nil), parts...),
		Content:   joinTextParts(parts),
		CreatedAt: time.Now().UTC(),
	}
}

// NewUserMessage creates a new user message from a plain string.
func NewUserMessage(content string) Message {
	return NewMessage(MessageUser, content)
}

// NewUserMessageWithParts creates a multi-modal user message.
func NewUserMessageWithParts(parts ...Part) Message {
	return NewMessageWithParts(MessageUser, parts...)
}

// NewAssistantMessage creates a new assistant message with optional tool calls.
func NewAssistantMessage(content string, toolCalls []ToolCall) Message {
	m := NewMessage(MessageAssistant, content)
	m.ToolCalls = toolCalls
	return m
}

// NewAssistantMessageWithParts creates a multi-modal assistant message.
// Tool calls remain a separate field — they are not Parts.
func NewAssistantMessageWithParts(parts []Part, toolCalls []ToolCall) Message {
	m := NewMessageWithParts(MessageAssistant, parts...)
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

// joinTextParts collects the text payload of every text Part into a single
// newline-joined string. Non-text parts are skipped — Content is meant as a
// textual digest, not a complete rendering of the message.
func joinTextParts(parts []Part) string {
	var b []byte
	first := true
	for _, p := range parts {
		if p.Type != PartText || p.Text == "" {
			continue
		}
		if !first {
			b = append(b, '\n')
		}
		b = append(b, p.Text...)
		first = false
	}
	return string(b)
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

	// Halt signals that the tool wants to terminate the run cleanly after
	// this result is recorded. The loop stops with status "halted_by_tool"
	// without issuing another LLM call. Side-effects of the tool have
	// already happened, so the loop does not roll anything back.
	//
	// Canonical use cases:
	//   - request_user_decision: pause the run until a human provides input.
	//   - end_of_conversation: the workflow is logically complete and the
	//     tool signals that no further model reasoning is needed.
	//
	// When multiple tool calls in one turn include Halt=true, the loop
	// still records all results in history before stopping, so no output
	// is silently dropped. The first halting result determines the status.
	Halt bool `json:"halt,omitempty"`
}

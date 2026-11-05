package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func TestChatStream_ToolCallWithoutArgumentsGetsEmptyObject(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_pages","input":{}}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		"",
	}, "\n")

	srv := sseServer(t, body)
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{{Type: message.MessageUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	final := drain(t, ch)
	if len(final.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(final.ToolCalls))
	}
	if got := string(final.ToolCalls[0].Arguments); got != "{}" {
		t.Errorf("Arguments = %q, want %q", got, "{}")
	}

	history := []message.Message{
		{Type: message.MessageUser, Content: "hi"},
		{Type: message.MessageAssistant, ToolCalls: final.ToolCalls},
		{Type: message.MessageTool, ToolID: "toolu_1", Content: "ok"},
	}
	native := p.Translator().ToNative("", history, nil)
	if _, err := json.Marshal(native); err != nil {
		t.Errorf("history with a no-argument tool call does not serialise: %v", err)
	}
}

func TestToNative_EmptyToolArgumentsSerialise(t *testing.T) {
	p := NewProvider("test-key")
	history := []message.Message{
		{Type: message.MessageUser, Content: "hi"},
		{Type: message.MessageAssistant, ToolCalls: []message.ToolCall{{ID: "toolu_1", Name: "list_pages", Arguments: json.RawMessage("")}}},
		{Type: message.MessageTool, ToolID: "toolu_1", Content: "ok"},
	}

	native := p.Translator().ToNative("", history, nil)
	if _, err := json.Marshal(native); err != nil {
		t.Errorf("empty Arguments does not serialise: %v", err)
	}
}

func TestToNative_TruncatedToolArgumentsSerialise(t *testing.T) {
	p := NewProvider("test-key")
	history := []message.Message{
		{Type: message.MessageUser, Content: "hi"},
		{Type: message.MessageAssistant, ToolCalls: []message.ToolCall{{ID: "toolu_1", Name: "write_page", Arguments: json.RawMessage(`{"slug":"inicio","body":"<div`)}}},
		{Type: message.MessageTool, ToolID: "toolu_1", Content: "ok"},
	}

	native := p.Translator().ToNative("", history, nil)
	if _, err := json.Marshal(native); err != nil {
		t.Errorf("truncated Arguments does not serialise: %v", err)
	}
}

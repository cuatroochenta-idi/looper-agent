package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// omittedThinkingStream is a Claude 5 response with display "omitted": the
// thinking text is empty and only the signature carries the reasoning,
// followed by a tool call.
var omittedThinkingStream = strings.Join([]string{
	"event: message_start",
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
	"",
	"event: content_block_start",
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	"",
	"event: content_block_delta",
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`,
	"",
	"event: content_block_delta",
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`,
	"",
	"event: content_block_stop",
	`data: {"type":"content_block_stop","index":0}`,
	"",
	"event: content_block_start",
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_pages","input":{}}}`,
	"",
	"event: content_block_delta",
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"app\":\"crm\"}"}}`,
	"",
	"event: content_block_stop",
	`data: {"type":"content_block_stop","index":1}`,
	"",
	"event: message_delta",
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
	"",
	"event: message_stop",
	`data: {"type":"message_stop"}`,
	"",
	"",
}, "\n")

// assistantContent marshals the translated request and returns the content
// blocks of its messages[index] as generic JSON objects.
func assistantContent(t *testing.T, native any, index int) []map[string]any {
	t.Helper()

	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var req struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if index >= len(req.Messages) {
		t.Fatalf("request has %d messages, want index %d", len(req.Messages), index)
	}

	return req.Messages[index].Content
}

func TestChatStream_CapturesOmittedThinkingAndReplaysItBeforeToolUse(t *testing.T) {
	srv := sseServer(t, omittedThinkingStream)
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL), WithModel("claude-haiku-5-5"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	final := drain(t, ch)
	want := []message.ThinkingBlock{{Provider: thinkingFormat, Type: "thinking", Signature: "sig-abc"}}
	if len(final.ThinkingBlocks) != 1 || final.ThinkingBlocks[0] != want[0] {
		t.Fatalf("ThinkingBlocks = %+v, want %+v", final.ThinkingBlocks, want)
	}

	assistant := message.NewAssistantMessage(final.Content, final.ToolCalls)
	assistant.ThinkingBlocks = final.ThinkingBlocks
	history := []message.Message{
		message.NewUserMessage("hi"),
		assistant,
		message.NewToolResult("toolu_1", "list_pages", "[]", false),
	}

	content := assistantContent(t, p.Translator().ToNative("", history, nil), 1)
	if len(content) != 2 {
		t.Fatalf("assistant content = %v, want thinking + tool_use", content)
	}
	thinking, ok := content[0]["thinking"]
	if content[0]["type"] != "thinking" || content[0]["signature"] != "sig-abc" || !ok || thinking != "" {
		t.Errorf("first block = %v, want the thinking block with its signature and an empty thinking field", content[0])
	}
	if content[1]["type"] != "tool_use" {
		t.Errorf("second block = %v, want tool_use", content[1])
	}
}

func TestFromNative_RedactedThinkingRoundTrips(t *testing.T) {
	var resp anthropic.Message
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","stop_reason":"end_turn",` +
		`"content":[{"type":"redacted_thinking","data":"opaque=="},{"type":"thinking","thinking":"plan","signature":"sig-2"},{"type":"text","text":"Done."}],` +
		`"usage":{"input_tokens":3,"output_tokens":4}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	p := NewProvider("test-key")
	out, err := p.translator.FromNative(&resp)
	if err != nil {
		t.Fatalf("FromNative: %v", err)
	}
	want := []message.ThinkingBlock{
		{Provider: thinkingFormat, Type: "redacted_thinking", Data: "opaque=="},
		{Provider: thinkingFormat, Type: "thinking", Thinking: "plan", Signature: "sig-2"},
	}
	if len(out.ThinkingBlocks) != 2 || out.ThinkingBlocks[0] != want[0] || out.ThinkingBlocks[1] != want[1] {
		t.Fatalf("ThinkingBlocks = %+v, want %+v", out.ThinkingBlocks, want)
	}

	assistant := message.NewAssistantMessage(out.Content, nil)
	assistant.ThinkingBlocks = out.ThinkingBlocks
	stored, err := json.Marshal(assistant)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var restored message.Message
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}

	history := []message.Message{message.NewUserMessage("go"), restored, message.NewUserMessage("next")}
	content := assistantContent(t, p.Translator().ToNative("", history, nil), 1)
	if len(content) != 3 {
		t.Fatalf("assistant content = %v, want redacted + thinking + text", content)
	}
	if content[0]["type"] != "redacted_thinking" || content[0]["data"] != "opaque==" {
		t.Errorf("first block = %v, want the redacted block", content[0])
	}
	if content[1]["type"] != "thinking" || content[1]["signature"] != "sig-2" || content[1]["thinking"] != "plan" {
		t.Errorf("second block = %v, want the thinking block", content[1])
	}
	if content[2]["type"] != "text" || content[2]["text"] != "Done." {
		t.Errorf("third block = %v, want the text", content[2])
	}
}

func TestToNative_IgnoresThinkingBlocksFromAnotherProvider(t *testing.T) {
	assistant := message.NewAssistantMessage("Done.", nil)
	assistant.ThinkingBlocks = []message.ThinkingBlock{{Provider: "other", Type: "thinking", Signature: "foreign"}}
	history := []message.Message{message.NewUserMessage("go"), assistant, message.NewUserMessage("next")}

	content := assistantContent(t, NewProvider("test-key").Translator().ToNative("", history, nil), 1)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Errorf("assistant content = %v, want only the text", content)
	}
}

// signatureRejectionServer answers the first request carrying a thinking
// block with the API's history-editing 400 and every other request with
// reply. It records each request body.
type signatureRejectionServer struct {
	mu     sync.Mutex
	bodies []string
	reply  func(w http.ResponseWriter)
}

func (s *signatureRejectionServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		s.mu.Lock()
		s.bodies = append(s.bodies, string(raw))
		first := len(s.bodies) == 1
		s.mu.Unlock()

		if first && strings.Contains(string(raw), `"type":"thinking"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` +
				"`signature` in `thinking`" + ` block. The block is bound to a different conversation."}}`))

			return
		}
		s.reply(w)
	}
}

func historyWithThinking() []message.Message {
	assistant := message.NewAssistantMessage("", []message.ToolCall{{ID: "toolu_1", Name: "list_pages", Arguments: json.RawMessage(`{}`)}})
	assistant.ThinkingBlocks = []message.ThinkingBlock{{Provider: thinkingFormat, Type: "thinking", Signature: "stale-sig"}}

	return []message.Message{
		message.NewUserMessage("hi"),
		assistant,
		message.NewToolResult("toolu_1", "list_pages", "[]", false),
	}
}

func TestChat_RetriesWithoutThinkingWhenTheSignatureIsRejected(t *testing.T) {
	s := &signatureRejectionServer{reply: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":"end_turn",` +
			`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`))
	}}
	srv := httptest.NewServer(s.handler(t))
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL), WithModel("claude-haiku-5-5"))
	req := provider.LLMRequest{Messages: historyWithThinking()}
	for range 2 {
		out, err := p.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if out.Content != "ok" {
			t.Fatalf("Content = %q, want ok", out.Content)
		}
	}

	if len(s.bodies) != 3 {
		t.Fatalf("server saw %d requests, want 3 (rejected, retried, then the next call without a new 400)", len(s.bodies))
	}
	for i, body := range s.bodies[1:] {
		if strings.Contains(body, "stale-sig") {
			t.Errorf("request %d still replays the rejected thinking block", i+2)
		}
	}
}

func TestChatStream_RetriesWithoutThinkingWhenTheSignatureIsRejected(t *testing.T) {
	s := &signatureRejectionServer{reply: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_delta",
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
			"",
		}, "\n")))
	}}
	srv := httptest.NewServer(s.handler(t))
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL), WithModel("claude-haiku-5-5"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{Messages: historyWithThinking()})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	final := drain(t, ch)
	if final.Error != nil || final.Content != "ok" {
		t.Fatalf("final = %+v, want the retried answer", final)
	}
	if len(s.bodies) != 2 || strings.Contains(s.bodies[1], "stale-sig") {
		t.Errorf("requests = %d, retried body replays the rejected block: %v", len(s.bodies), len(s.bodies) == 2 && strings.Contains(s.bodies[1], "stale-sig"))
	}
}

func TestChatStream_ToolCallsKeepContentOrder(t *testing.T) {
	events := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
	}
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for i, name := range names {
		events = append(events,
			"event: content_block_start",
			fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%s","name":"%s","input":{}}}`, i, name, name),
			"",
			"event: content_block_stop",
			fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i),
			"",
		)
	}
	events = append(events,
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		"",
	)
	srv := sseServer(t, strings.Join(events, "\n"))
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	final := drain(t, ch)
	var got []string
	for _, tc := range final.ToolCalls {
		got = append(got, tc.Name)
	}
	if strings.Join(got, "") != strings.Join(names, "") {
		t.Errorf("tool calls = %v, want the content order %v", got, names)
	}
}

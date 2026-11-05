package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

const plainStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const plainMessage = `{"id":"msg_1","type":"message","role":"assistant","model":"m","stop_reason":"end_turn",` +
	`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// sentMaxTokens sends one request through the provider and returns the
// max_tokens it put on the wire.
func sentMaxTokens(t *testing.T, stream bool, req provider.LLMRequest, opts ...Option) int64 {
	t.Helper()

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(plainStream))

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(plainMessage))
	}))
	defer srv.Close()

	p := NewProvider("test-key", append([]Option{WithBaseURL(srv.URL)}, opts...)...)
	req.Messages = []message.Message{message.NewUserMessage("hi")}
	if stream {
		ch, err := p.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		if final := drain(t, ch); final.Error != nil {
			t.Fatalf("stream error: %v", final.Error)
		}
	} else if _, err := p.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	return sent.MaxTokens
}

func TestMaxTokens_DefaultsByModel(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
		req    provider.LLMRequest
		opts   []Option
		want   int64
	}{
		{"Claude 5 streaming default", true, provider.LLMRequest{}, []Option{WithModel("claude-haiku-5-5")}, 64000},
		{"Claude 5 on Bedrock", true, provider.LLMRequest{}, []Option{WithModel("us.anthropic.claude-opus-5-5")}, 64000},
		{"request model decides", true, provider.LLMRequest{Model: "claude-sonnet-5"}, []Option{WithModel("claude-sonnet-4-6")}, 64000},
		{"older model keeps the historical default", true, provider.LLMRequest{}, []Option{WithModel("claude-sonnet-4-6")}, 16384},
		// The SDK refuses a non-streaming call whose max_tokens could run
		// past ten minutes, so the non-streaming default stays put.
		{"non-streaming keeps the historical default", false, provider.LLMRequest{}, []Option{WithModel("claude-haiku-5-5")}, 16384},
		{"WithMaxTokens wins", true, provider.LLMRequest{}, []Option{WithModel("claude-haiku-5-5"), WithMaxTokens(2000)}, 2000},
		{"request MaxTokens wins", true, provider.LLMRequest{MaxTokens: 1234}, []Option{WithModel("claude-haiku-5-5")}, 1234},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sentMaxTokens(t, tc.stream, tc.req, tc.opts...); got != tc.want {
				t.Errorf("max_tokens = %d, want %d", got, tc.want)
			}
		})
	}
}

package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/openai/openai-go/v3/responses"
)

// From gpt-5.6 on OpenAI bills cache writes at 1.25x input and reports them
// as their own subset of the prompt. Both API surfaces must carry that
// count into Usage.CacheWriteTokens, or the cost model prices the written
// tokens as plain input.
func TestUsageFromResponses_CacheWrites(t *testing.T) {
	var u responses.ResponseUsage
	raw := `{"input_tokens":15000,"output_tokens":20,"total_tokens":15020,
		"input_tokens_details":{"cached_tokens":12000,"cache_write_tokens":3000},
		"output_tokens_details":{"reasoning_tokens":0}}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}

	got := usageFromResponses(u)
	want := provider.Usage{InputTokens: 15000, OutputTokens: 20, CachedTokens: 12000, CacheWriteTokens: 3000}
	if got != want {
		t.Errorf("Usage = %+v, want %+v", got, want)
	}
}

func TestChatCompletions_CacheWrites(t *testing.T) {
	const usage = `"usage":{"prompt_tokens":15000,"completion_tokens":20,"total_tokens":15020,` +
		`"prompt_tokens_details":{"cached_tokens":12000,"cache_write_tokens":3000}}`
	want := provider.Usage{InputTokens: 15000, OutputTokens: 20, CachedTokens: 12000, CacheWriteTokens: 3000}
	req := provider.LLMRequest{Messages: []message.Message{message.NewUserMessage("hi")}}

	t.Run("non-streaming", func(t *testing.T) {
		var body map[string]any
		srv := captureBody(t, `{"id":"c1","object":"chat.completion","model":"gpt-6-luna",`+
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],`+usage+`}`, &body)
		defer srv.Close()

		p := NewProvider("sk-test", WithBaseURL(srv.URL), WithModel("gpt-6-luna"), WithAPI(APIChatCompletions))
		resp, err := p.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Usage != want {
			t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, s := range []string{
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: {"id":"x","object":"chat.completion.chunk","choices":[],` + usage + `}`,
				`data: [DONE]`,
			} {
				_, _ = w.Write([]byte(s + "\n\n"))
			}
		}))
		defer srv.Close()

		p := NewProvider("sk-test", WithBaseURL(srv.URL), WithModel("gpt-6-luna"), WithAPI(APIChatCompletions))
		ch, err := p.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		var final provider.StreamChunk
		for c := range ch {
			if c.IsFinal {
				final = c
			}
		}
		if final.Usage == nil || *final.Usage != want {
			t.Errorf("Usage = %+v, want %+v", final.Usage, want)
		}
	})
}

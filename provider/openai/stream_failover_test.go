package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// TestChatStream_PreContentErrorReturnsSync reproduces the OpenRouter
// "No endpoints found for X" 404 case. The upstream sends a non-200
// before any SSE chunk; the SDK only surfaces it once stream.Next() is
// called. The provider must surface this as the function-return error
// so FailoverProvider / RetryProvider's pre-channel-error contracts
// can route around the broken inner.
func TestChatStream_PreContentErrorReturnsSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No endpoints found for qwen/qwen3.6-35b-a3b.","code":404}`))
	}))
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("qwen/qwen3.6-35b-a3b"),
	)

	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if ch != nil {
		t.Errorf("expected nil channel on pre-content error, got %T", ch)
	}
	if err == nil {
		t.Fatal("expected synchronous error so Retry/Failover can engage; got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404; got %v", err)
	}
	if !strings.Contains(err.Error(), "openai stream:") {
		t.Errorf("error should keep the legacy 'openai stream:' prefix for log compatibility; got %v", err)
	}
}

// TestChatStream_HappyPathStreamsContent verifies the probe doesn't
// regress normal streaming: a successful SSE response still produces
// a channel that emits content chunks plus a final chunk with usage.
func TestChatStream_HappyPathStreamsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter has no Flusher — httptest server expected to support flush")
		}
		// Two content deltas, then a finish-reason chunk, then a usage chunk, then [DONE].
		writes := []string{
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hola"}}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" mundo"}}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, s := range writes {
			_, _ = w.Write([]byte(s))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("gpt-x"),
	)

	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if ch == nil {
		t.Fatal("happy path returned nil channel")
	}

	var got strings.Builder
	var final provider.StreamChunk
	for c := range ch {
		if c.IsFinal {
			final = c
			continue
		}
		got.WriteString(c.Content)
	}
	if got.String() != "Hola mundo" {
		t.Errorf("content = %q, want %q", got.String(), "Hola mundo")
	}
	if !final.IsFinal {
		t.Fatal("never received final chunk")
	}
	if final.Error != nil {
		t.Errorf("happy path final.Error = %v, want nil", final.Error)
	}
	if final.Usage == nil || final.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want OutputTokens=2", final.Usage)
	}
}

// TestChatStream_ToleratesTrailingTelemetryComments reproduces the
// NeuralWatt / vLLM streaming incompatibility: after the usage chunk the
// server appends non-standard SSE *comment* frames (": energy …", ": cost …")
// carrying telemetry, then "[DONE]". openai-go decodes each comment as an
// empty-Data event and json.Unmarshal("") fails with "unexpected end of JSON
// input". Because a finish_reason was already received, the model's reply is
// complete, so the trailing error must be dropped — not propagated as a turn
// failure. Without the fix, final.Error is non-nil and the whole turn dies
// with a spurious network_error despite a fully-streamed answer.
func TestChatStream_ToleratesTrailingTelemetryComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter has no Flusher — httptest server expected to support flush")
		}
		writes := []string{
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hola"}}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" mundo"}}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}` + "\n\n",
			// Non-standard SSE comment frames appended by NeuralWatt after usage.
			`: energy {"energy_joules":56.82,"avg_power_watts":5515.3}` + "\n\n",
			`: cost {"request_cost_usd":7.9e-05,"allowance_remaining_usd":11.59}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, s := range writes {
			_, _ = w.Write([]byte(s))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("glm-x"),
	)

	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var got strings.Builder
	var final provider.StreamChunk
	for c := range ch {
		if c.IsFinal {
			final = c
			continue
		}
		got.WriteString(c.Content)
	}
	if got.String() != "Hola mundo" {
		t.Errorf("content = %q, want %q", got.String(), "Hola mundo")
	}
	if !final.IsFinal {
		t.Fatal("never received final chunk")
	}
	if final.Error != nil {
		t.Errorf("final.Error = %v, want nil — trailing telemetry comments after finish_reason must not fail the turn", final.Error)
	}
	// Statistics must survive the swallowed error. NeuralWatt sends the usage
	// chunk BEFORE the telemetry comment frames, so it is captured before the
	// drop — token accounting (cost, budgets) stays intact even when the
	// trailing parse error is ignored.
	if final.Usage == nil {
		t.Fatal("final.Usage = nil — usage stats must survive the dropped trailing error")
	}
	if final.Usage.InputTokens != 12 || final.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want InputTokens=12 OutputTokens=2", final.Usage)
	}
}

// TestChatStream_StillSurfacesRealErrorAfterFinish guards the precision of
// the trailing-telemetry exception: the drop is gated on a *json.SyntaxError,
// so a GENUINE error event that arrives after finish_reason (here a server
// "error" frame, which openai-go reports as "received error while streaming",
// not a syntax error) must still propagate. This is the "could other things
// slip through?" guard — they must not.
func TestChatStream_StillSurfacesRealErrorAfterFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		writes := []string{
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hola"}}]}` + "\n\n",
			`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			// A real server error AFTER finish_reason — must NOT be swallowed.
			`data: {"error":{"message":"backend exploded","code":500}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, s := range writes {
			_, _ = w.Write([]byte(s))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithModel("glm-x"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var final provider.StreamChunk
	for c := range ch {
		if c.IsFinal {
			final = c
		}
	}
	if final.Error == nil {
		t.Fatal("final.Error = nil, want propagated server error — a real error after finish_reason must NOT be swallowed by the telemetry exception")
	}
}

// TestChatStream_FailoverEngagesOnPreContentError is the integration
// test: pair the fixed openai provider against FailoverProvider with a
// scripted second inner. The 404 from the first inner must trigger a
// switch and the second inner's content must reach the channel.
func TestChatStream_FailoverEngagesOnPreContentError(t *testing.T) {
	var hits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No endpoints found","code":404}`))
	}))
	defer bad.Close()

	primary := NewProvider("sk-bad", WithBaseURL(bad.URL), WithModel("qwen/x"))
	secondary := &scriptedSuccess{content: "from-fallback"}

	failover, err := provider.NewFailover(
		[]provider.LLMProvider{primary, secondary},
		provider.WithFailoverNames([]string{"primary", "secondary"}),
	)
	if err != nil {
		t.Fatalf("NewFailover: %v", err)
	}

	ch, err := failover.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err != nil {
		t.Fatalf("ChatStream surfaced sync error — failover did not engage: %v", err)
	}
	// Accumulate the same way the loop does (loop.go:1338): non-final
	// chunks carry deltas, the final chunk carries the cumulative text
	// for re-derivation. Counting both would double the content.
	var got strings.Builder
	for c := range ch {
		if c.IsFinal {
			continue
		}
		got.WriteString(c.Content)
	}
	if got.String() != "from-fallback" {
		t.Errorf("content = %q, want from-fallback (failover did not deliver)", got.String())
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("primary called %d times, want exactly 1", hits)
	}
}

// TestChatStream_ContextCancelBeforeFirstChunk asserts the probe
// short-circuits on ctx-cancel rather than hanging.
func TestChatStream_ContextCancelBeforeFirstChunk(t *testing.T) {
	// Server that never responds — let ctx do the work.
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stop
	}))
	defer func() {
		close(stop)
		srv.Close()
	}()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithModel("gpt-x"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.ChatStream(ctx, provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err == nil {
		t.Fatal("expected error on canceled ctx")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// scriptedSuccess is a minimal provider that emits a single content
// chunk and a final chunk — just enough to verify failover routed to
// it without dragging in another httptest server.
type scriptedSuccess struct {
	content string
}

func (p *scriptedSuccess) Model() string                 { return "scripted" }
func (p *scriptedSuccess) Translator() provider.Translator { return nil }
func (p *scriptedSuccess) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	return &provider.LLMResponse{Content: p.content}, nil
}
func (p *scriptedSuccess) ChatStream(_ context.Context, _ provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: p.content}
	ch <- provider.StreamChunk{IsFinal: true, Content: p.content}
	close(ch)
	return ch, nil
}

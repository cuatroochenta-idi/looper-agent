package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// sseServer starts an httptest.Server that replays the given SSE body verbatim
// for any request, with the text/event-stream content type the SDK decoder
// keys on. Returned closer must be deferred by the caller.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write sse body: %v", err)
		}
	}))
	return srv
}

// drain collects every chunk from a ChatStream channel until it closes and
// returns the final (IsFinal) chunk. Fails if no final chunk arrives.
func drain(t *testing.T, ch <-chan provider.StreamChunk) provider.StreamChunk {
	t.Helper()
	var final provider.StreamChunk
	var sawFinal bool
	for chunk := range ch {
		if chunk.IsFinal {
			final = chunk
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Fatal("stream closed without a final chunk")
	}
	return final
}

// canned message_start usage: input=10, cache_creation=80, cache_read=300,
// output=1. The normalised InputTokens must sum the three prompt buckets.
const streamStartUsage = `{"input_tokens":10,"cache_creation_input_tokens":80,"cache_read_input_tokens":300,"output_tokens":1}`

// TestChatStream_UsageNormalisation drives the provider against a full,
// well-formed SSE response and asserts the final chunk carries the normalised
// usage: InputTokens is the inclusive prompt total (input + cache_read +
// cache_creation), and the cumulative output count from message_delta wins.
func TestChatStream_UsageNormalisation(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"stop_reason":null,"stop_sequence":null,"usage":` + streamStartUsage + `}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}`,
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

	if final.Content != "Hello, world" {
		t.Errorf("Content = %q, want %q", final.Content, "Hello, world")
	}
	if final.Error != nil {
		t.Errorf("unexpected Error on clean stream: %v", final.Error)
	}
	if final.Usage == nil {
		t.Fatal("final chunk Usage is nil")
	}
	if got := final.Usage.InputTokens; got != 390 {
		t.Errorf("InputTokens = %d, want 390 (10+80+300)", got)
	}
	if got := final.Usage.CachedTokens; got != 300 {
		t.Errorf("CachedTokens = %d, want 300", got)
	}
	if got := final.Usage.CacheWriteTokens; got != 80 {
		t.Errorf("CacheWriteTokens = %d, want 80", got)
	}
	if got := final.Usage.OutputTokens; got != 42 {
		t.Errorf("OutputTokens = %d, want 42 (cumulative from message_delta)", got)
	}
}

// abruptServer hijacks the connection and writes a response whose declared
// Content-Length exceeds the bytes actually sent, then closes the socket. The
// client's chunk reader hits an unexpected EOF, so stream.Err() is non-nil —
// the only way to exercise the "connection died mid-stream" branch (a clean
// EOF on a bufio.Scanner reports no error).
func abruptServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter is not a Hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		// Declare more bytes than we send: the client reads the header, starts
		// consuming the body, and gets a truncated read when we close.
		overstated := len(body) + 4096
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", overstated)
		buf.WriteString(body)
		buf.Flush()
		// conn.Close (deferred) truncates the body → io.ErrUnexpectedEOF client-side.
	}))
	return srv
}

// TestChatStream_AbruptCloseKeepsPartialUsage asserts the mid-stream failure
// contract: when the connection dies after message_start but before
// message_stop, the final chunk still carries Error AND the partial usage the
// API already reported (so those tokens are billed, not lost).
func TestChatStream_AbruptCloseKeepsPartialUsage(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"stop_reason":null,"stop_sequence":null,"usage":` + streamStartUsage + `}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		"",
		"", // trailing blank so the delta event is dispatched before the truncated read
	}, "\n")

	srv := abruptServer(t, body)
	defer srv.Close()

	p := NewProvider("test-key", WithBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{{Type: message.MessageUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	final := drain(t, ch)

	if final.Error == nil {
		t.Error("expected Error on abrupt close, got nil")
	}
	if final.Usage == nil {
		t.Fatal("partial Usage must survive the error, got nil")
	}
	// message_start already reported the prompt buckets: 10+80+300 = 390.
	if got := final.Usage.InputTokens; got != 390 {
		t.Errorf("partial InputTokens = %d, want 390", got)
	}
	if got := final.Usage.CacheWriteTokens; got != 80 {
		t.Errorf("partial CacheWriteTokens = %d, want 80", got)
	}
}

// TestFromNative_UsageNormalisation covers the non-streaming path: FromNative
// must sum the three disjoint Anthropic prompt buckets into the inclusive
// InputTokens and surface cache_creation as CacheWriteTokens.
func TestFromNative_UsageNormalisation(t *testing.T) {
	tr := &Translator{}
	msg := &anthropic.Message{
		Usage: anthropic.Usage{
			InputTokens:              10,
			CacheReadInputTokens:     300,
			CacheCreationInputTokens: 80,
			OutputTokens:             42,
		},
	}

	out, err := tr.FromNative(msg)
	if err != nil {
		t.Fatalf("FromNative: %v", err)
	}
	if got := out.Usage.InputTokens; got != 390 {
		t.Errorf("InputTokens = %d, want 390 (10+300+80)", got)
	}
	if got := out.Usage.CachedTokens; got != 300 {
		t.Errorf("CachedTokens = %d, want 300", got)
	}
	if got := out.Usage.CacheWriteTokens; got != 80 {
		t.Errorf("CacheWriteTokens = %d, want 80", got)
	}
	if got := out.Usage.OutputTokens; got != 42 {
		t.Errorf("OutputTokens = %d, want 42", got)
	}
}

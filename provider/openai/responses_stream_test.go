package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// newResponsesSSEServer streams the given frames verbatim as an SSE body.
// Frames must already carry their "event:"/"data:" lines and trailing
// blank line, matching the wire shape of /v1/responses streaming.
func newResponsesSSEServer(t *testing.T, frames []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter has no Flusher — httptest server expected to support flush")
		}
		for _, f := range frames {
			_, _ = w.Write([]byte(f))
			flusher.Flush()
		}
	}))
}

// sseFrame renders one SSE frame. The responses SDK discriminates on the
// "type" field inside the data JSON; the event: line matches what the real
// API sends. The payload is compacted first — SSE data must be a single
// line, and the shared response-body fixtures are pretty-printed.
func sseFrame(eventType, data string) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(data)); err != nil {
		panic("sseFrame: invalid JSON fixture: " + err.Error())
	}
	return "event: " + eventType + "\ndata: " + compact.String() + "\n\n"
}

const createdEventData = `{"type":"response.created","sequence_number":0,` +
	`"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-5.6","status":"in_progress"}}`

// TestResponsesStream_HappyPath: two text deltas then response.completed
// with usage → two content chunks and a final chunk carrying cumulative
// content, usage, and no error.
func TestResponsesStream_HappyPath(t *testing.T) {
	srv := newResponsesSSEServer(t, []string{
		sseFrame("response.created", createdEventData),
		sseFrame("response.output_text.delta",
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hola","sequence_number":1}`),
		sseFrame("response.output_text.delta",
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" mundo","sequence_number":2}`),
		sseFrame("response.completed",
			`{"type":"response.completed","sequence_number":3,"response":`+finalTextResponseBody+`}`),
	})
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var deltas strings.Builder
	var final provider.StreamChunk
	var sawFinal bool
	for c := range ch {
		if c.IsFinal {
			final = c
			sawFinal = true
			continue
		}
		deltas.WriteString(c.Content)
	}
	if deltas.String() != "Hola mundo" {
		t.Errorf("delta content = %q, want %q", deltas.String(), "Hola mundo")
	}
	if !sawFinal {
		t.Fatal("never received final chunk")
	}
	if final.Error != nil {
		t.Errorf("final.Error = %v, want nil", final.Error)
	}
	if final.Usage == nil || final.Usage.InputTokens != 10 || final.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want input=10 output=5", final.Usage)
	}
	if final.Content != "Hola mundo" {
		t.Errorf("final.Content = %q, want cumulative %q", final.Content, "Hola mundo")
	}
	if final.ProviderID != "openai" || final.ModelID != "gpt-5.6" {
		t.Errorf("provenance = %q/%q, want openai/gpt-5.6", final.ProviderID, final.ModelID)
	}
}

// TestResponsesStream_ToolCall: the completed event's output carries a
// reasoning item plus a function call — the final chunk must expose the
// tool call (ID = call_id) with the signature blob on it.
func TestResponsesStream_ToolCall(t *testing.T) {
	srv := newResponsesSSEServer(t, []string{
		sseFrame("response.created", createdEventData),
		sseFrame("response.completed",
			`{"type":"response.completed","sequence_number":1,"response":`+toolCallResponseBody+`}`),
	})
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("¿qué tiempo hace?")},
		Tools:    []*tool.Tool{testWeatherTool()},
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
	if final.Error != nil {
		t.Fatalf("final.Error = %v, want nil", final.Error)
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %v, want 1", final.ToolCalls)
	}
	tc := final.ToolCalls[0]
	if tc.ID != "call_9" || tc.Name != "get_weather" {
		t.Errorf("ToolCall = %+v, want call_9/get_weather", tc)
	}
	var blob map[string]any
	if err := json.Unmarshal(tc.Signature, &blob); err != nil {
		t.Fatalf("Signature is not a looper blob: %v (%q)", err, tc.Signature)
	}
	if blob["looper_openai_responses"] != float64(1) {
		t.Errorf("Signature marker = %v, want 1", blob["looper_openai_responses"])
	}
	if items, _ := blob["items"].([]any); len(items) != 2 {
		t.Errorf("Signature items = %v, want reasoning + function_call", blob["items"])
	}
	if final.Usage == nil || final.Usage.InputTokens != 100 || final.Usage.CachedTokens != 40 {
		t.Errorf("Usage = %+v, want input=100 cached=40", final.Usage)
	}
}

// TestResponsesStream_ReasoningSummaryDeltas: summary text deltas surface
// on StreamChunk.Reasoning only when reasoning output was requested.
func TestResponsesStream_ReasoningSummaryDeltas(t *testing.T) {
	frames := []string{
		sseFrame("response.created", createdEventData),
		sseFrame("response.reasoning_summary_text.delta",
			`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"pondering","sequence_number":1}`),
		sseFrame("response.completed",
			`{"type":"response.completed","sequence_number":2,"response":`+finalTextResponseBody+`}`),
	}

	collect := func(t *testing.T, include bool) (reasoning string, err error) {
		t.Helper()
		srv := newResponsesSSEServer(t, frames)
		defer srv.Close()
		opts := []Option{WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6")}
		if include {
			opts = append(opts, WithIncludeReasoning(true))
		}
		p := NewProvider("sk-test", opts...)
		ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
			Messages: []message.Message{message.NewUserMessage("hola")},
		})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for c := range ch {
			b.WriteString(c.Reasoning)
		}
		return b.String(), nil
	}

	if got, err := collect(t, true); err != nil || got != "pondering" {
		t.Errorf("with include: reasoning = %q err = %v, want %q", got, err, "pondering")
	}
	if got, err := collect(t, false); err != nil || got != "" {
		t.Errorf("without include: reasoning = %q err = %v, want empty", got, err)
	}
}

// TestResponsesStream_FailedEvent: a response.failed terminal event must
// produce a final chunk with a wrapped error AND the usage it carried, so
// a failed call still bills its tokens.
func TestResponsesStream_FailedEvent(t *testing.T) {
	srv := newResponsesSSEServer(t, []string{
		sseFrame("response.created", createdEventData),
		sseFrame("response.failed",
			`{"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response",`+
				`"created_at":1,"model":"gpt-5.6","status":"failed",`+
				`"error":{"code":"server_error","message":"boom"},`+
				`"usage":{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},`+
				`"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}}}`),
	})
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
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
	if final.Error == nil || !strings.Contains(final.Error.Error(), "boom") {
		t.Errorf("final.Error = %v, want the server's failure message", final.Error)
	}
	if final.Error != nil && !strings.Contains(final.Error.Error(), "openai responses stream:") {
		t.Errorf("final.Error = %v, want the 'openai responses stream:' prefix", final.Error)
	}
	if final.Usage == nil || final.Usage.InputTokens != 7 {
		t.Errorf("Usage = %+v, want the failed call's 7 input tokens billed", final.Usage)
	}
}

// TestResponsesStream_PreContentErrorReturnsSync mirrors the chat path's
// silent-failover guard: an HTTP 400 before any event must surface as the
// function-return error (nil channel), so Failover/Retry wrappers can
// route around the broken inner instead of staying committed to it.
func TestResponsesStream_PreContentErrorReturnsSync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"To use function tools, use /v1/responses","type":"invalid_request_error","param":"reasoning_effort"}}`))
	}))
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	})
	if ch != nil {
		t.Errorf("expected nil channel on pre-content error, got %T", ch)
	}
	if err == nil {
		t.Fatal("expected synchronous error so Retry/Failover can engage; got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400; got %v", err)
	}
	if !strings.Contains(err.Error(), "openai responses stream:") {
		t.Errorf("error should carry the 'openai responses stream:' prefix; got %v", err)
	}
}

package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// captureRequest stands up a fake Messages endpoint, runs one Chat call
// against it, and returns the decoded request body plus its headers.
func captureRequest(t *testing.T, apiKey string, opts []Option, req provider.LLMRequest) (map[string]any, http.Header) {
	t.Helper()

	var body map[string]any
	var headers http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request body: %v (raw: %s)", err, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"model": "anthropic.claude-opus-4-8",
			"stop_reason": "end_turn",
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 10, "output_tokens": 2}
		}`)
	}))
	defer srv.Close()

	p := NewProvider(apiKey, append(opts, WithBaseURL(srv.URL))...)

	if _, err := p.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return body, headers
}

// TestBedrockStyleRequestShape covers the wiring the Bedrock example
// depends on: an empty API key must not put an x-api-key header on the
// wire (it would invalidate the SigV4 signature), and the "anthropic."
// -prefixed Bedrock model id must resolve to the same capabilities as the
// first-party id — adaptive thinking, no sampling parameters.
func TestBedrockStyleRequestShape(t *testing.T) {
	opts := []Option{
		WithModel("anthropic.claude-opus-4-8"),
		WithTemperature(0.7),
		WithEffort("high"),
		// Required on the Bedrock path: without it the SDK runs its own
		// credential resolution first and aborts with ErrNoCredentials
		// before the Bedrock middleware ever gets to sign the request.
		WithRequestOptions(option.WithoutEnvironmentDefaults()),
	}

	body, headers := captureRequest(t, "", opts, provider.LLMRequest{
		SystemPrompt: "be terse",
		Messages:     []message.Message{{Type: message.MessageUser, Content: "hi"}},
		Reasoning:    &provider.ReasoningConfig{Effort: provider.ReasoningEffortHigh},
	})

	if got := headers.Get("x-api-key"); got != "" {
		t.Errorf("empty API key must not send an x-api-key header, got %q", got)
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature must be dropped for Opus 4.8 — the API rejects it")
	}
	if _, ok := body["top_p"]; ok {
		t.Error("top_p must be dropped")
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("want a thinking config, got %v", body["thinking"])
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v, want adaptive (budget_tokens is a 400 here)", thinking["type"])
	}
	if _, ok := thinking["budget_tokens"]; ok {
		t.Error("budget_tokens must not be sent to Opus 4.8")
	}

	oc, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("want output_config, got %v", body["output_config"])
	}
	if oc["effort"] != "high" {
		t.Errorf("effort = %v, want high", oc["effort"])
	}
}

// TestAPIKeyStillSentWhenProvided guards the other side of the empty-key
// change: the first-party path must keep authenticating normally.
func TestAPIKeyStillSentWhenProvided(t *testing.T) {
	_, headers := captureRequest(t, "sk-ant-test-key", []Option{WithModel("claude-sonnet-4-5")}, provider.LLMRequest{
		Messages: []message.Message{{Type: message.MessageUser, Content: "hi"}},
	})

	if got := headers.Get("x-api-key"); got != "sk-ant-test-key" {
		t.Errorf("x-api-key = %q, want the configured key", got)
	}
}

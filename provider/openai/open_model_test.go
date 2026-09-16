package openai

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
	"github.com/openai/openai-go/v3"
)

// captureBody serves a canned response and records the request body so a
// test can assert on the wire shape the provider produced.
func captureBody(t *testing.T, response string, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v (%s)", err, raw)
			return
		}
		*got = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
}

const reasoningChatResponse = `{
  "id":"c1","object":"chat.completion","model":"deepseek/deepseek-v4.1-flash",
  "choices":[{"index":0,"finish_reason":"stop","message":{
    "role":"assistant","content":"42",
    "reasoning":"first I counted, then I stopped",
    "reasoning_details":[{"type":"reasoning.text","text":"first I counted","signature":"sha256:abc","id":"r-1","format":"openai-responses-v1","index":0}]
  }}],
  "usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
}`

func TestOpenModelReasoning_OpenRouterWireRequestAndCapture(t *testing.T) {
	enabled := true
	disabled := false

	tests := []struct {
		name      string
		cfg       OpenModelReasoning
		effort    provider.ReasoningEffort // provider-level WithReasoningEffort
		wantField map[string]any
	}{
		{
			name:      "effort only",
			cfg:       OpenModelReasoning{Wire: ReasoningWireOpenRouter, Effort: provider.ReasoningEffortMedium},
			wantField: map[string]any{"effort": "medium"},
		},
		{
			name:      "max_tokens wins over effort",
			cfg:       OpenModelReasoning{Wire: ReasoningWireOpenRouter, Effort: provider.ReasoningEffortHigh, MaxTokens: 8000},
			wantField: map[string]any{"max_tokens": float64(8000)},
		},
		{
			name:      "enabled true rides along",
			cfg:       OpenModelReasoning{Wire: ReasoningWireOpenRouter, Effort: provider.ReasoningEffortLow, Enabled: &enabled},
			wantField: map[string]any{"effort": "low", "enabled": true},
		},
		{
			name:      "enabled false switches thinking off",
			cfg:       OpenModelReasoning{Wire: ReasoningWireOpenRouter, Enabled: &disabled},
			wantField: map[string]any{"enabled": false},
		},
		{
			name:      "unset effort falls back to the older knob",
			cfg:       OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true},
			effort:    provider.ReasoningEffortHigh,
			wantField: map[string]any{"effort": "high"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			srv := captureBody(t, reasoningChatResponse, &body)
			defer srv.Close()

			opts := []Option{
				WithBaseURL(srv.URL),
				WithModel("deepseek/deepseek-v4.1-flash"),
				WithAPI(APIChatCompletions),
				WithOpenModelReasoning(tc.cfg),
			}
			if tc.effort != provider.ReasoningEffortNone {
				opts = append(opts, WithReasoningEffort(tc.effort))
			}
			p := NewProvider("sk-test", opts...)

			if _, err := p.Chat(context.Background(), provider.LLMRequest{
				Messages: []message.Message{message.NewUserMessage("count")},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			obj, ok := body["reasoning"].(map[string]any)
			if !ok {
				t.Fatalf("request has no `reasoning` object; body=%v", body)
			}
			for k, want := range tc.wantField {
				if obj[k] != want {
					t.Errorf("reasoning.%s = %v (%T), want %v", k, obj[k], obj[k], want)
				}
			}
			if len(obj) != len(tc.wantField) {
				t.Errorf("reasoning object = %v, want exactly %v", obj, tc.wantField)
			}
			// The OpenRouter wire owns the effort; the legacy top-level
			// field must not disagree with the object.
			if _, present := body["reasoning_effort"]; present {
				t.Errorf("legacy reasoning_effort must be suppressed on the OpenRouter wire; body=%v", body)
			}
		})
	}
}

func TestOpenModelReasoning_ChatCapturesTextAndDetails(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, reasoningChatResponse, &body)
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("deepseek/deepseek-v4.1-flash"),
		WithAPI(APIChatCompletions),
		WithOpenModelReasoning(OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true, Trace: true}),
	)

	resp, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("count")},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Reasoning != "first I counted, then I stopped" {
		t.Errorf("Reasoning = %q", resp.Reasoning)
	}
	if !strings.Contains(string(resp.ReasoningDetails), `"signature":"sha256:abc"`) {
		t.Errorf("ReasoningDetails lost the signature: %s", resp.ReasoningDetails)
	}
	var arr []map[string]any
	if err := json.Unmarshal(resp.ReasoningDetails, &arr); err != nil {
		t.Fatalf("ReasoningDetails is not a JSON array: %v", err)
	}
	if len(arr) != 1 || arr[0]["id"] != "r-1" {
		t.Errorf("ReasoningDetails = %s", resp.ReasoningDetails)
	}
}

// TestOpenModelReasoning_ReasoningContentWireUsesLegacyEffort pins the
// DeepSeek-native dialect: no `reasoning` object, effort on the top-level
// reasoning_effort param.
func TestOpenModelReasoning_ReasoningContentWireUsesLegacyEffort(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, reasoningChatResponse, &body)
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("deepseek-reasoner"),
		WithAPI(APIChatCompletions),
		WithOpenModelReasoning(OpenModelReasoning{
			Wire:     ReasoningWireReasoningContent,
			Effort:   provider.ReasoningEffortHigh,
			PassBack: true,
		}),
	)

	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("count")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high; body=%v", body["reasoning_effort"], body)
	}
	if _, present := body["reasoning"]; present {
		t.Errorf("reasoning_content wire must not send a `reasoning` object; body=%v", body)
	}
}

// TestOpenModelReasoning_ExtraParamsSurvive guards the single-SetExtraFields
// rule: the SDK's setter replaces the map, so reasoning and the caller's own
// routing knobs have to be merged before it is called.
func TestOpenModelReasoning_ExtraParamsSurvive(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, reasoningChatResponse, &body)
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("deepseek/deepseek-v4.1-flash"),
		WithAPI(APIChatCompletions),
		WithOpenModelReasoning(OpenModelReasoning{Wire: ReasoningWireOpenRouter, Effort: provider.ReasoningEffortLow}),
	)

	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages:    []message.Message{message.NewUserMessage("count")},
		ExtraParams: map[string]any{"provider": map[string]any{"require_parameters": true}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if _, ok := body["reasoning"]; !ok {
		t.Errorf("reasoning object dropped; body=%v", body)
	}
	if _, ok := body["provider"]; !ok {
		t.Errorf("caller ExtraParams dropped; body=%v", body)
	}
}

// TestOpenModelReasoning_StreamMergesDetails feeds reasoning_details split
// across SSE chunks and checks the merged array on the final chunk.
func TestOpenModelReasoning_StreamMergesDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writes := []string{
			`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"let me ","reasoning_details":[{"type":"reasoning.text","text":"let me ","id":"r-1","format":"openai-responses-v1","index":0}]}}]}` + "\n\n",
			`data: {"id":"x","choices":[{"index":0,"delta":{"reasoning":"think","reasoning_details":[{"type":"reasoning.text","text":"think","signature":"sha256:zz","index":0}]}}]}` + "\n\n",
			`data: {"id":"x","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"counted","index":1}]}}]}` + "\n\n",
			`data: {"id":"x","choices":[{"index":0,"delta":{"content":"42"}}]}` + "\n\n",
			`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"id":"x","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n",
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
		WithModel("deepseek/deepseek-v4.1-flash"),
		WithAPI(APIChatCompletions),
		WithOpenModelReasoning(OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true, Trace: true}),
	)

	ch, err := p.ChatStream(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("count")},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var deltas []string
	var final provider.StreamChunk
	for c := range ch {
		if c.IsFinal {
			final = c
			continue
		}
		if c.Reasoning != "" {
			deltas = append(deltas, c.Reasoning)
		}
	}

	if strings.Join(deltas, "") != "let me think" {
		t.Errorf("reasoning deltas = %q, want the two fragments", deltas)
	}
	if final.Reasoning != "let me think" {
		t.Errorf("final chunk Reasoning = %q, want the whole think", final.Reasoning)
	}

	var merged []map[string]any
	if err := json.Unmarshal(final.ReasoningDetails, &merged); err != nil {
		t.Fatalf("merged details are not a JSON array: %v (%s)", err, final.ReasoningDetails)
	}
	if len(merged) != 2 {
		t.Fatalf("merged %d entries, want 2: %s", len(merged), final.ReasoningDetails)
	}
	if merged[0]["text"] != "let me think" {
		t.Errorf("entry 0 text = %v, want the concatenation", merged[0]["text"])
	}
	if merged[0]["signature"] != "sha256:zz" {
		t.Errorf("entry 0 signature = %v, want last-wins", merged[0]["signature"])
	}
	if merged[0]["id"] != "r-1" {
		t.Errorf("entry 0 id = %v, want the id from the first fragment", merged[0]["id"])
	}
	if merged[1]["summary"] != "counted" {
		t.Errorf("entry 1 summary = %v", merged[1]["summary"])
	}
}

// TestTranslatorEchoBack covers the interleaved-thinking fix on the wire:
// a stored assistant turn replays its thinking only when PassBack is set,
// under the field name the configured dialect expects.
func TestTranslatorEchoBack(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"I planned","signature":"sha256:keepme","id":"r-9","index":0}]`)
	assistant := message.NewAssistantMessageWithReasoning("ok", []message.ToolCall{{
		ID: "call_1", Name: "search", Arguments: json.RawMessage(`{"q":"x"}`),
	}}, "I planned", details)
	plain := message.NewAssistantMessageWithReasoning("ok", nil, "I planned", details)

	tests := []struct {
		name       string
		cfg        *OpenModelReasoning
		msg        message.Message
		wantHas    []string
		wantAbsent []string
	}{
		{
			name:       "no config echoes nothing",
			cfg:        nil,
			msg:        assistant,
			wantAbsent: []string{"reasoning_details", "reasoning_content"},
		},
		{
			name:       "passback off echoes nothing",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireOpenRouter, Trace: true},
			msg:        assistant,
			wantAbsent: []string{"reasoning_details", "reasoning_content"},
		},
		{
			name:       "openrouter wire echoes details verbatim",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true},
			msg:        assistant,
			wantHas:    []string{`"signature":"sha256:keepme"`, `"reasoning.text"`, `"id":"r-9"`},
			wantAbsent: []string{"reasoning_content"},
		},
		{
			name:       "openrouter wire also echoes on a tool-free turn",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireOpenRouter, PassBack: true},
			msg:        plain,
			wantHas:    []string{`"signature":"sha256:keepme"`},
			wantAbsent: []string{"reasoning_content"},
		},
		{
			name:       "reasoning_content wire echoes the text",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireReasoningContent, PassBack: true},
			msg:        assistant,
			wantHas:    []string{`"reasoning_content":"I planned"`},
			wantAbsent: []string{"reasoning_details"},
		},
		{
			// DeepSeek rejects a thinking-mode request whose history drops
			// reasoning_content on ANY assistant turn, tool-calling or not.
			name:       "reasoning_content wire echoes on a tool-free turn too",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireReasoningContent, PassBack: true},
			msg:        plain,
			wantHas:    []string{`"reasoning_content":"I planned"`},
			wantAbsent: []string{"reasoning_details"},
		},
		{
			// No thinking stored means no field: the wire defines nothing
			// for an empty one, so inventing "" would be a guess.
			name:       "a turn without thinking sends no field",
			cfg:        &OpenModelReasoning{Wire: ReasoningWireReasoningContent, PassBack: true},
			msg:        message.NewAssistantMessage("ok", nil),
			wantAbsent: []string{"reasoning_content", "reasoning_details"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &Translator{model: "m", openModel: tc.cfg}
			params := tr.ToNative("", []message.Message{tc.msg}, nil).(openai.ChatCompletionNewParams)
			if len(params.Messages) != 1 {
				t.Fatalf("translated %d messages, want 1", len(params.Messages))
			}
			raw, err := json.Marshal(params.Messages[0])
			if err != nil {
				t.Fatalf("marshal assistant param: %v", err)
			}
			got := string(raw)
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("assistant param missing %s\ngot: %s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("assistant param should not carry %s\ngot: %s", absent, got)
				}
			}
		})
	}
}

// TestOpenModelReasoning_IgnoredOnResponsesAPI pins the boundary: the
// Responses API has its own reasoning surface and must not grow a chat
// completions `reasoning` object.
func TestOpenModelReasoning_IgnoredOnResponsesAPI(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"42"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`, &body)
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithModel("gpt-5.6"),
		WithAPI(APIResponses),
		WithOpenModelReasoning(OpenModelReasoning{Wire: ReasoningWireOpenRouter, MaxTokens: 8000, PassBack: true}),
	)

	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("count")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if obj, ok := body["reasoning"].(map[string]any); ok {
		if _, has := obj["max_tokens"]; has {
			t.Errorf("responses path grew the chat-completions reasoning object: %v", body)
		}
	}
}

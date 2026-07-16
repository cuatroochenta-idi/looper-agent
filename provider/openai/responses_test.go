package openai

import (
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

// testWeatherTool builds the single function tool used across the
// responses tests. Declarative construction — a schema failure here is a
// programmer error, so MustNewTool is appropriate.
func testWeatherTool() *tool.Tool {
	type weatherInput struct {
		City string `json:"city"`
	}
	return tool.MustNewTool(weatherInput{}, func(_ context.Context, _ weatherInput) (string, error) {
		return "sunny", nil
	}, tool.ToolConfig{Name: "get_weather", Description: "Get the weather for a city"})
}

// testSignatureBlob is a canonical looper responses signature: one
// reasoning item (with encrypted content) followed by the function call it
// produced — the exact shape §3 of the design stores on the first ToolCall.
const testSignatureBlob = `{"looper_openai_responses":1,"items":[` +
	`{"type":"reasoning","id":"rs_1","encrypted_content":"enc-blob","summary":["earlier thought"]},` +
	`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Madrid\"}"}]}`

// finalTextResponseBody is a minimal completed /v1/responses payload whose
// output is a single assistant message — the "no tool calls" terminal case.
const finalTextResponseBody = `{
	"id":"resp_2","object":"response","created_at":1,"model":"gpt-5.6","status":"completed",
	"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed",
		"content":[{"type":"output_text","text":"Hace sol.","annotations":[]}]}],
	"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},
		"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}`

// toolCallResponseBody is a completed payload whose output carries a
// reasoning item (with encrypted content) followed by a function call.
const toolCallResponseBody = `{
	"id":"resp_1","object":"response","created_at":1,"model":"gpt-5.6","status":"completed",
	"output":[
		{"type":"reasoning","id":"rs_9","encrypted_content":"gAAA-enc","summary":[{"type":"summary_text","text":"weighing options"}]},
		{"type":"function_call","id":"fc_9","call_id":"call_9","name":"get_weather","arguments":"{\"city\":\"Madrid\"}","status":"completed"}],
	"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},
		"output_tokens":20,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":120}}`

// captureResponsesServer returns an httptest server that records the last
// request path and JSON body, and answers every call with respBody.
func captureResponsesServer(respBody string, path *string, body *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*path = r.URL.Path
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		*body = m
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
}

// TestResponses_RequestShape pins the whole request mapping in one pass:
// endpoint, reasoning effort, statelessness (store:false + encrypted
// reasoning include), instructions, flat tool shape, and the input item
// sequence rebuilt from a multi-turn tool-loop history whose assistant turn
// carries a looper signature blob.
func TestResponses_RequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := captureResponsesServer(finalTextResponseBody, &gotPath, &gotBody)
	defer srv.Close()

	p := NewProvider("sk-test",
		WithBaseURL(srv.URL),
		WithAPI(APIResponses),
		WithModel("gpt-5.6"),
		WithReasoningEffort(provider.ReasoningEffortMedium),
		WithMaxTokens(500),
	)

	tc := message.ToolCall{
		ID:        "call_1",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Madrid"}`),
		Signature: []byte(testSignatureBlob),
	}
	msgs := []message.Message{
		message.NewUserMessage("¿qué tiempo hace?"),
		message.NewAssistantMessage("", []message.ToolCall{tc}),
		message.NewToolResult("call_1", "get_weather", "sunny", false),
		message.NewUserMessage("gracias, ¿y mañana?"),
	}

	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		SystemPrompt: "You are a weather bot.",
		Messages:     msgs,
		Tools:        []*tool.Tool{testWeatherTool()},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if got := gotBody["instructions"]; got != "You are a weather bot." {
		t.Errorf("instructions = %v", got)
	}
	if got := gotBody["store"]; got != false {
		t.Errorf("store = %v, want false", got)
	}
	if got := gotBody["max_output_tokens"]; got != float64(500) {
		t.Errorf("max_output_tokens = %v, want 500", got)
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Errorf("reasoning.effort = %v, want medium", reasoning["effort"])
	}
	include, _ := gotBody["include"].([]any)
	foundInclude := false
	for _, v := range include {
		if v == "reasoning.encrypted_content" {
			foundInclude = true
		}
	}
	if !foundInclude {
		t.Errorf("include = %v, want reasoning.encrypted_content", include)
	}

	// Flat tool shape: name/parameters at the top level, no nested
	// "function" object (that's the chat/completions shape).
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 entry", tools)
	}
	tl, _ := tools[0].(map[string]any)
	if tl["type"] != "function" || tl["name"] != "get_weather" {
		t.Errorf("tool = %v, want flat {type:function,name:get_weather,...}", tl)
	}
	if _, nested := tl["function"]; nested {
		t.Errorf("tool has nested 'function' object — chat shape leaked into responses path: %v", tl)
	}
	if _, ok := tl["parameters"].(map[string]any); !ok {
		t.Errorf("tool.parameters missing or not an object: %v", tl["parameters"])
	}

	// Input item order: user msg, reasoning (replayed from the blob),
	// function_call, function_call_output, user msg. The blob is the
	// authoritative replay — nothing else may be synthesized for that turn.
	input, _ := gotBody["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("input has %d items, want 5: %v", len(input), input)
	}
	item := func(i int) map[string]any {
		m, _ := input[i].(map[string]any)
		return m
	}
	if item(0)["role"] != "user" {
		t.Errorf("input[0] = %v, want user message", item(0))
	}
	if item(1)["type"] != "reasoning" || item(1)["id"] != "rs_1" ||
		item(1)["encrypted_content"] != "enc-blob" {
		t.Errorf("input[1] = %v, want replayed reasoning item rs_1 with encrypted content", item(1))
	}
	if summary, _ := item(1)["summary"].([]any); len(summary) != 1 {
		t.Errorf("input[1].summary = %v, want the blob's summary text replayed", item(1)["summary"])
	}
	if item(2)["type"] != "function_call" || item(2)["call_id"] != "call_1" ||
		item(2)["name"] != "get_weather" || item(2)["arguments"] != `{"city":"Madrid"}` {
		t.Errorf("input[2] = %v, want replayed function_call call_1", item(2))
	}
	if item(3)["type"] != "function_call_output" || item(3)["call_id"] != "call_1" ||
		item(3)["output"] != "sunny" {
		t.Errorf("input[3] = %v, want function_call_output for call_1", item(3))
	}
	if item(4)["role"] != "user" {
		t.Errorf("input[4] = %v, want user message", item(4))
	}
}

// TestResponses_MaxOutputTokensOmittedWhenUnset preserves the "0 means no
// cap" semantic on the responses path — same philosophy as applyMaxTokens.
func TestResponses_MaxOutputTokensOmittedWhenUnset(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := captureResponsesServer(finalTextResponseBody, &gotPath, &gotBody)
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("hola")},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, present := gotBody["max_output_tokens"]; present {
		t.Errorf("max_output_tokens present without a configured cap: %v", gotBody["max_output_tokens"])
	}
}

// TestResponses_ParseToolCall pins the response mapping for a tool-call
// turn: ToolCall.ID must be the call_id (the loop echoes it back as the
// function_call_output call_id — the item id would 400), the signature
// blob must capture both output items for stateless replay, and usage must
// map inclusively (input includes cached).
func TestResponses_ParseToolCall(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := captureResponsesServer(toolCallResponseBody, &gotPath, &gotBody)
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	resp, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("¿qué tiempo hace?")},
		Tools:    []*tool.Tool{testWeatherTool()},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %v, want 1", resp.ToolCalls)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_9" {
		t.Errorf("ToolCall.ID = %q, want call_9 (the call_id, NOT the item id fc_9)", tc.ID)
	}
	if tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"Madrid"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
	if resp.IsFinal {
		t.Error("IsFinal = true on a tool-call turn, want false")
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 20 || resp.Usage.CachedTokens != 40 {
		t.Errorf("Usage = %+v, want input=100 output=20 cached=40", resp.Usage)
	}

	// The signature blob must be JSON with the looper marker and both items
	// (reasoning first, function_call second) in output order.
	var blob map[string]any
	if err := json.Unmarshal(tc.Signature, &blob); err != nil {
		t.Fatalf("Signature is not JSON: %v (%q)", err, tc.Signature)
	}
	if blob["looper_openai_responses"] != float64(1) {
		t.Errorf("Signature marker = %v, want 1", blob["looper_openai_responses"])
	}
	items, _ := blob["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("Signature items = %v, want 2", items)
	}
	first, _ := items[0].(map[string]any)
	if first["type"] != "reasoning" || first["id"] != "rs_9" || first["encrypted_content"] != "gAAA-enc" {
		t.Errorf("Signature items[0] = %v, want the reasoning item with encrypted content", first)
	}
	second, _ := items[1].(map[string]any)
	if second["type"] != "function_call" || second["call_id"] != "call_9" || second["name"] != "get_weather" {
		t.Errorf("Signature items[1] = %v, want the function_call item", second)
	}
}

// TestResponses_FinalText pins the terminal case: message output becomes
// Content, IsFinal is true, and no signature blob exists (there is no
// ToolCall to attach it to; a completed turn needs no reasoning replay).
func TestResponses_FinalText(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := captureResponsesServer(finalTextResponseBody, &gotPath, &gotBody)
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	resp, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: []message.Message{message.NewUserMessage("¿qué tiempo hace?")},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Hace sol." {
		t.Errorf("Content = %q, want %q", resp.Content, "Hace sol.")
	}
	if !resp.IsFinal {
		t.Error("IsFinal = false on a text-only reply, want true")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want none", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want input=10 output=5", resp.Usage)
	}
}

// assertFallbackSynthesis is the shared body of the fallback tests: given
// an assistant turn whose first ToolCall carries the given signature, the
// request must synthesize [assistant message, function_call] with NO
// reasoning item — the shape used when no looper blob is available.
func assertFallbackSynthesis(t *testing.T, signature []byte) {
	t.Helper()
	var gotPath string
	var gotBody map[string]any
	srv := captureResponsesServer(finalTextResponseBody, &gotPath, &gotBody)
	defer srv.Close()

	p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
	tc := message.ToolCall{
		ID:        "call_1",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Madrid"}`),
		Signature: signature,
	}
	msgs := []message.Message{
		message.NewUserMessage("¿qué tiempo hace?"),
		message.NewAssistantMessage("déjame consultar", []message.ToolCall{tc}),
		message.NewToolResult("call_1", "get_weather", "sunny", false),
	}
	if _, err := p.Chat(context.Background(), provider.LLMRequest{
		Messages: msgs,
		Tools:    []*tool.Tool{testWeatherTool()},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	input, _ := gotBody["input"].([]any)
	var sawFunctionCall, sawAssistantText bool
	for _, raw := range input {
		m, _ := raw.(map[string]any)
		if m["type"] == "reasoning" {
			t.Errorf("fallback synthesis emitted a reasoning item: %v", m)
		}
		if m["type"] == "function_call" {
			sawFunctionCall = true
			if m["call_id"] != "call_1" || m["name"] != "get_weather" {
				t.Errorf("synthesized function_call = %v", m)
			}
		}
		if m["role"] == "assistant" {
			sawAssistantText = true
		}
	}
	if !sawFunctionCall {
		t.Errorf("no function_call synthesized from the assistant ToolCall: %v", input)
	}
	if !sawAssistantText {
		t.Errorf("assistant text content dropped in fallback synthesis: %v", input)
	}
}

// TestResponses_FallbackSynthesisEmptySignature: history recorded by the
// chat/completions path (or an older looper) has no signature at all.
func TestResponses_FallbackSynthesisEmptySignature(t *testing.T) {
	assertFallbackSynthesis(t, nil)
}

// TestResponses_ForeignSignature: a Gemini thoughtSignature (opaque raw
// bytes, not JSON) or unrelated JSON without the looper marker must be
// treated as foreign — fallback synthesis, no error, blob ignored.
func TestResponses_ForeignSignature(t *testing.T) {
	t.Run("non_json_bytes", func(t *testing.T) {
		assertFallbackSynthesis(t, []byte{0x01, 0xC0, 0xFF, 0xEE})
	})
	t.Run("json_without_marker", func(t *testing.T) {
		assertFallbackSynthesis(t, []byte(`{"thought":"opaque"}`))
	})
}

// newRoutingServer answers both endpoints so routing tests can assert the
// path each configuration lands on.
func newRoutingServer(gotPath *string) *httptest.Server {
	const chatBody = `{"id":"x","object":"chat.completion","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = w.Write([]byte(finalTextResponseBody))
			return
		}
		_, _ = w.Write([]byte(chatBody))
	}))
}

// TestResponses_Routing asserts the per-call API selection through the
// actual request path. baseURL is always set here (httptest), so APIAuto
// must stay on chat/completions even with an effort configured — compat
// endpoints often lack /v1/responses. The baseURL=="" arm of the auto rule
// is covered by TestAPIFor (it can't hit a fake server by definition).
func TestResponses_Routing(t *testing.T) {
	cases := []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{
			name:     "auto_with_effort_and_baseurl_stays_chat",
			opts:     []Option{WithReasoningEffort(provider.ReasoningEffortMedium)},
			wantPath: "/chat/completions",
		},
		{
			name:     "auto_without_effort_stays_chat",
			opts:     nil,
			wantPath: "/chat/completions",
		},
		{
			name:     "explicit_responses_wins",
			opts:     []Option{WithAPI(APIResponses)},
			wantPath: "/responses",
		},
		{
			name:     "explicit_chat_wins_over_effort",
			opts:     []Option{WithAPI(APIChatCompletions), WithReasoningEffort(provider.ReasoningEffortMedium)},
			wantPath: "/chat/completions",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath string
			srv := newRoutingServer(&gotPath)
			defer srv.Close()

			opts := append([]Option{WithBaseURL(srv.URL), WithModel("gpt-5.6")}, c.opts...)
			p := NewProvider("sk-test", opts...)
			if _, err := p.Chat(context.Background(), provider.LLMRequest{
				Messages: []message.Message{message.NewUserMessage("hola")},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if gotPath != c.wantPath {
				t.Errorf("path = %q, want %q", gotPath, c.wantPath)
			}
		})
	}
}

// TestAPIFor covers the routing helper directly, including the
// baseURL=="" arm that no httptest server can exercise: Auto only routes
// to Responses against the real api.openai.com AND when an effort is in
// play for the request.
func TestAPIFor(t *testing.T) {
	cases := []struct {
		name   string
		opts   []Option
		effort provider.ReasoningEffort
		want   API
	}{
		{"auto_effort_no_baseurl", nil, provider.ReasoningEffortMedium, APIResponses},
		{"auto_no_effort_no_baseurl", nil, provider.ReasoningEffortNone, APIChatCompletions},
		{"auto_effort_with_baseurl", []Option{WithBaseURL("http://localhost:1")}, provider.ReasoningEffortMedium, APIChatCompletions},
		{"explicit_responses_wins", []Option{WithAPI(APIResponses)}, provider.ReasoningEffortNone, APIResponses},
		{"explicit_chat_wins", []Option{WithAPI(APIChatCompletions)}, provider.ReasoningEffortHigh, APIChatCompletions},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewProvider("sk-test", c.opts...)
			var rc *provider.ReasoningConfig
			if c.effort != provider.ReasoningEffortNone {
				rc = &provider.ReasoningConfig{Effort: c.effort}
			}
			if got := p.apiFor(p.resolveEffort(rc)); got != c.want {
				t.Errorf("apiFor = %q, want %q", got, c.want)
			}
		})
	}
}

// TestResponses_StructuredOutput mirrors buildResponseFormatParams onto
// the responses text.format union: json_schema carries name+schema,
// json_object carries neither, and None suppresses the block entirely.
func TestResponses_StructuredOutput(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)

	run := func(t *testing.T, req provider.LLMRequest) map[string]any {
		t.Helper()
		var gotPath string
		var gotBody map[string]any
		srv := captureResponsesServer(finalTextResponseBody, &gotPath, &gotBody)
		defer srv.Close()
		p := NewProvider("sk-test", WithBaseURL(srv.URL), WithAPI(APIResponses), WithModel("gpt-5.6"))
		req.Messages = []message.Message{message.NewUserMessage("hola")}
		if _, err := p.Chat(context.Background(), req); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		return gotBody
	}

	t.Run("json_schema", func(t *testing.T) {
		body := run(t, provider.LLMRequest{
			ResponseSchema:     schema,
			ResponseFormatMode: provider.ResponseFormatJSONSchema,
		})
		text, _ := body["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		if format["type"] != "json_schema" {
			t.Errorf("text.format = %v, want json_schema", format)
		}
		if format["name"] != "result" {
			t.Errorf("text.format.name = %v, want default 'result'", format["name"])
		}
		if _, ok := format["schema"].(map[string]any); !ok {
			t.Errorf("text.format.schema missing: %v", format)
		}
	})

	t.Run("json_object", func(t *testing.T) {
		body := run(t, provider.LLMRequest{
			ResponseSchema:     schema,
			ResponseFormatMode: provider.ResponseFormatJSONObject,
		})
		text, _ := body["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Errorf("text.format = %v, want json_object", format)
		}
		if _, hasSchema := format["schema"]; hasSchema {
			t.Errorf("json_object mode must not carry the schema body: %v", format)
		}
	})

	t.Run("none", func(t *testing.T) {
		body := run(t, provider.LLMRequest{
			ResponseSchema:     schema,
			ResponseFormatMode: provider.ResponseFormatNone,
		})
		if _, present := body["text"]; present {
			t.Errorf("text present with ResponseFormatNone: %v", body["text"])
		}
	})
}

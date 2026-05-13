package looper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

// TestNativeResponseFormat_OpenAI_AdvertisesCapability pins the contract
// that the OpenAI provider opts in to native structured output. The agent
// loop reads this via provider.SupportsNativeResponseFormat to decide
// whether to inject the final_response tool.
func TestNativeResponseFormat_OpenAI_AdvertisesCapability(t *testing.T) {
	p := openai.NewProvider("test-key")
	if !provider.SupportsNativeResponseFormat(p) {
		t.Error("openai.Provider must implement ResponseFormatCapable returning true")
	}
}

// TestNativeResponseFormat_Google_AdvertisesCapability — same contract
// for Gemini.
func TestNativeResponseFormat_Google_AdvertisesCapability(t *testing.T) {
	p := google.NewProvider("test-key")
	if !provider.SupportsNativeResponseFormat(p) {
		t.Error("google.Provider must implement ResponseFormatCapable returning true")
	}
}

// TestNativeResponseFormat_Anthropic_OptsOut asserts Anthropic stays on
// the tool-injection path — it has no first-class structured output
// endpoint, so the framework should treat it accordingly.
func TestNativeResponseFormat_Anthropic_OptsOut(t *testing.T) {
	p := anthropic.NewProvider("test-key")
	if provider.SupportsNativeResponseFormat(p) {
		t.Error("anthropic.Provider must NOT advertise native response format")
	}
}

// captureProvider snapshots the LLMRequest of the most recent call so
// tests can assert on the wire-shape decision the loop makes given a
// configured agent.
type captureProvider struct {
	model string
	last  provider.LLMRequest
	// inheritCapability decides whether this fake reports native support;
	// each sub-test toggles it to simulate either path.
	inheritCapability bool
}

func (p *captureProvider) Model() string                   { return p.model }
func (p *captureProvider) Translator() provider.Translator { return nil }

// SupportsResponseFormat is the optional capability interface the agent
// loop probes. We make it conditional so a single fake covers both paths.
func (p *captureProvider) SupportsResponseFormat() bool { return p.inheritCapability }

func (p *captureProvider) Chat(_ context.Context, req provider.LLMRequest) (*provider.LLMResponse, error) {
	p.last = req
	// Echo a final_response tool call only on the tool-injection path;
	// native path expects the JSON in Content.
	if p.inheritCapability {
		return &provider.LLMResponse{
			Content: `{"sentiment":"positive","score":0.9,"keywords":["go"]}`,
			IsFinal: true,
		}, nil
	}
	return &provider.LLMResponse{
		ToolCalls: []message.ToolCall{{
			ID:        "1",
			Name:      "final_response",
			Arguments: json.RawMessage(`{"output":{"sentiment":"positive","score":0.9,"keywords":["go"]}}`),
		}},
	}, nil
}

func (p *captureProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := p.Chat(ctx, req)
		ch <- provider.StreamChunk{
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
			IsFinal:   true,
		}
	}()
	return ch, nil
}

type sentResult struct {
	Sentiment string   `json:"sentiment" jsonschema:"required"`
	Score     float64  `json:"score"`
	Keywords  []string `json:"keywords"`
}

// TestStructuredOutput_NativePath_PassesResponseSchema asserts that when
// the provider opts in to native response_format, the loop sets
// LLMRequest.ResponseSchema and does NOT inject a final_response tool.
func TestStructuredOutput_NativePath_PassesResponseSchema(t *testing.T) {
	p := &captureProvider{model: "fake", inheritCapability: true}

	agent := MustNewAgent(p, "be precise",
		WithStructuredOutput[sentResult](),
	)

	res, err := agent.Run(context.Background(), "score this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(p.last.ResponseSchema) == 0 {
		t.Error("native path: LLMRequest.ResponseSchema must be set")
	}
	for _, tl := range p.last.Tools {
		if tl.Name() == "final_response" {
			t.Errorf("native path: final_response tool must NOT be injected, got %v in tool list", tl.Name())
		}
	}

	var got sentResult
	if err := Decode(res, &got); err != nil {
		t.Fatalf("decode native path: %v\noutput=%q", err, res.Output)
	}
	if got.Sentiment != "positive" {
		t.Errorf("unexpected decoded value: %+v", got)
	}
}

// TestStructuredOutput_FallbackPath_InjectsTool asserts the inverse: a
// provider that doesn't support native response_format gets the
// final_response tool injected and ResponseSchema is left zero.
func TestStructuredOutput_FallbackPath_InjectsTool(t *testing.T) {
	p := &captureProvider{model: "fake", inheritCapability: false}

	agent := MustNewAgent(p, "be precise",
		WithStructuredOutput[sentResult](),
	)

	res, err := agent.Run(context.Background(), "score this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(p.last.ResponseSchema) != 0 {
		t.Error("fallback path: ResponseSchema must be left empty when provider can't honour it")
	}
	saw := false
	for _, tl := range p.last.Tools {
		if tl.Name() == "final_response" {
			saw = true
		}
	}
	if !saw {
		t.Error("fallback path: final_response tool must be injected")
	}
	var got sentResult
	if err := Decode(res, &got); err != nil {
		t.Fatalf("decode fallback path: %v", err)
	}
	if got.Sentiment != "positive" {
		t.Errorf("unexpected decoded value via tool path: %+v", got)
	}
}

// TestNativeSystemPrompt_DoesNotMentionToolCall asserts the system-prompt
// nudge differs by path: native path tells the model to "reply with JSON";
// the legacy tool-injection path is the one that mentions final_response.
func TestNativeSystemPrompt_DoesNotMentionToolCall(t *testing.T) {
	p := &captureProvider{model: "fake", inheritCapability: true}
	agent := MustNewAgent(p, "be precise",
		WithStructuredOutput[sentResult](),
	)
	_, _ = agent.Run(context.Background(), "go")
	if strings.Contains(p.last.SystemPrompt, "final_response") {
		t.Errorf("native path prompt must not mention final_response tool, got: %s", p.last.SystemPrompt)
	}
}

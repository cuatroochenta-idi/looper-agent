package looper

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// AnalysisResult is the typed output exercised by the structured-output
// tests. It deliberately uses constraints (enum, range) so the schema we
// hand the provider has real shape, not just type=string.
type AnalysisResult struct {
	Sentiment string   `json:"sentiment" jsonschema:"description=Verdict,enum=positive|negative|neutral,required"`
	Score     float64  `json:"score" jsonschema:"description=Confidence 0-1,minimum=0,maximum=1"`
	Topics    []string `json:"topics" jsonschema:"description=Topics found"`
}

// structuredProvider drives the assistant to emit a final_response tool
// call carrying the structured payload, which is what the framework's
// tool-injection path expects. The first response is a tool call; the
// second is unused (the tool's stringified output becomes the final
// answer).
type structuredProvider struct {
	mockEcho
	payload string
}

type mockEcho struct{ model string }

func (m *mockEcho) Model() string                            { return m.model }
func (m *mockEcho) Translator() provider.Translator          { return nil }

func (p *structuredProvider) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	return &provider.LLMResponse{
		ToolCalls: []message.ToolCall{{
			ID:        "1",
			Name:      "final_response",
			Arguments: json.RawMessage(`{"output":` + p.payload + `}`),
		}},
	}, nil
}

func (p *structuredProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := p.Chat(ctx, req)
		ch <- provider.StreamChunk{ToolCalls: resp.ToolCalls, IsFinal: true}
	}()
	return ch, nil
}

// TestStructuredOutput_DecodeReturnsTypedValue asserts the happy-path
// experience: the agent emits a JSON object matching the user's schema
// and Decode[T] unmarshals it into a typed Go struct.
func TestStructuredOutput_DecodeReturnsTypedValue(t *testing.T) {
	want := AnalysisResult{Sentiment: "positive", Score: 0.92, Topics: []string{"go", "agents"}}
	wantJSON, _ := json.Marshal(want)
	prov := &structuredProvider{mockEcho: mockEcho{model: "mock"}, payload: string(wantJSON)}

	agent := MustNewAgent(prov, "be precise",
		WithStructuredOutput[AnalysisResult](),
	)

	res, err := agent.Run(context.Background(), "analyze this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var got AnalysisResult
	if err := Decode(res, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sentiment != want.Sentiment || got.Score != want.Score || len(got.Topics) != len(want.Topics) {
		t.Errorf("decoded value mismatch: got %+v, want %+v", got, want)
	}
}

// TestStructuredOutput_DecodeErrorsOnNonJSON asserts that Decode surfaces
// a clear error when the agent output is not the expected JSON shape —
// e.g. the model produced plain text despite the structured-output prompt.
func TestStructuredOutput_DecodeErrorsOnNonJSON(t *testing.T) {
	res := &RunResult{Output: "not json"}
	var got AnalysisResult
	if err := Decode(res, &got); err == nil {
		t.Error("expected decode error on non-JSON output")
	}
}

// TestStructuredOutput_DecodeNilResultErrors guards against a common
// caller mistake — handing a nil result to Decode — with a typed error.
func TestStructuredOutput_DecodeNilResultErrors(t *testing.T) {
	var got AnalysisResult
	if err := Decode[AnalysisResult](nil, &got); err == nil {
		t.Error("expected error for nil RunResult")
	}
}

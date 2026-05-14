package looper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// gradedReply is the typed output the validation tests expect. Score is
// constrained 0..1 and sentiment is enum-bound so a malformed response is
// easy to engineer deterministically.
type gradedReply struct {
	Sentiment string  `json:"sentiment" jsonschema:"enum=positive,enum=negative,enum=neutral,required"`
	Score     float64 `json:"score" jsonschema:"minimum=0,maximum=1"`
}

// scriptedJSON drives a sequence of structured-output candidates the
// agent will receive on consecutive turns. The provider emits each
// payload as the final_response tool call so the loop's short-circuit
// triggers (matching the fallback path); the native path is exercised
// in TestStructuredOutput_NativePath above.
type scriptedJSON struct {
	model    string
	payloads []string
	idx      int
}

func (p *scriptedJSON) Model() string                   { return p.model }
func (p *scriptedJSON) Translator() provider.Translator { return nil }

func (p *scriptedJSON) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	out := `{}`
	if p.idx < len(p.payloads) {
		out = p.payloads[p.idx]
		p.idx++
	}
	return &provider.LLMResponse{
		ToolCalls: []message.ToolCall{{
			ID:        "1",
			Name:      "final_response",
			Arguments: json.RawMessage(`{"output":` + out + `}`),
		}},
	}, nil
}

func (p *scriptedJSON) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := p.Chat(ctx, req)
		ch <- provider.StreamChunk{ToolCalls: resp.ToolCalls, IsFinal: true}
	}()
	return ch, nil
}

// TestOutputValidation_RejectsInvalidThenRetriesUntilValid asserts the
// core promise: a malformed first turn (sentiment outside enum, score
// out of range) is rejected, the framework re-prompts, and the second
// turn's compliant JSON is returned.
func TestOutputValidation_RejectsInvalidThenRetriesUntilValid(t *testing.T) {
	bad := `{"sentiment":"happy","score":1.7}`
	good := `{"sentiment":"positive","score":0.9}`
	prov := &scriptedJSON{model: "mock", payloads: []string{bad, good}}

	agent := MustNewAgent(prov, "be precise",
		WithStructuredOutput[gradedReply](),
		WithOutputRetries(3),
	)

	res, err := agent.Run(context.Background(), "score this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("expected completed after recovery, got %q", res.Status)
	}
	if res.Turns < 2 {
		t.Errorf("expected at least 2 turns (1 reject + 1 retry), got %d", res.Turns)
	}

	var got gradedReply
	if err := Decode(res, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sentiment != "positive" || got.Score != 0.9 {
		t.Errorf("unexpected decoded value: %+v", got)
	}

	// Hint MUST have made it into history as a system message so the
	// model could see what went wrong.
	sawHint := false
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageSystem && strings.Contains(m.Content, "schema") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Error("validation hint should land in history as a system message")
	}
}

// TestOutputValidation_ExhaustsRetries asserts the run terminates with
// status=output_validation_exhausted when the model never produces a
// schema-compliant payload. The last attempted JSON is preserved.
func TestOutputValidation_ExhaustsRetries(t *testing.T) {
	prov := &scriptedJSON{model: "mock", payloads: []string{
		`{"sentiment":"happy","score":2}`,
		`{"sentiment":"sad","score":-1}`,
		`{"sentiment":"angry","score":99}`,
	}}

	agent := MustNewAgent(prov, "be precise",
		WithStructuredOutput[gradedReply](),
		WithOutputRetries(1),
	)

	res, err := agent.Run(context.Background(), "score this")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "output_validation_exhausted" {
		t.Errorf("expected output_validation_exhausted, got %q", res.Status)
	}
	if res.Output == "" {
		t.Error("last attempted JSON should be preserved even on failure")
	}
}

// TestOutputValidation_NoRetriesByDefault asserts back-compat — a user
// who just sets WithStructuredOutput without WithOutputRetries gets
// today's behaviour (single attempt, no validation gate).
func TestOutputValidation_NoRetriesByDefault(t *testing.T) {
	prov := &scriptedJSON{model: "mock", payloads: []string{
		`{"sentiment":"happy","score":2}`, // invalid but ignored
	}}
	agent := MustNewAgent(prov, "be precise",
		WithStructuredOutput[gradedReply](),
	)
	res, err := agent.Run(context.Background(), "score")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("legacy mode should still complete, got %q", res.Status)
	}
}

// TestOutputValidation_CustomValidatorRetries asserts WithOutputValidator
// composes on top of the schema check. Schema passes, business rule
// fails on first try, passes on second.
func TestOutputValidation_CustomValidatorRetries(t *testing.T) {
	prov := &scriptedJSON{model: "mock", payloads: []string{
		`{"sentiment":"neutral","score":0.5}`,
		`{"sentiment":"positive","score":0.9}`,
	}}

	agent := MustNewAgent(prov, "be precise",
		WithStructuredOutput[gradedReply](),
		WithOutputRetries(2),
		WithOutputValidator(func(r gradedReply) error {
			if r.Sentiment == "neutral" {
				return ErrOutputInvalid("we only want strong signals; pick positive or negative")
			}
			return nil
		}),
	)

	res, err := agent.Run(context.Background(), "score")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("custom validator: expected completed, got %q", res.Status)
	}
	var got gradedReply
	_ = Decode(res, &got)
	if got.Sentiment != "positive" {
		t.Errorf("unexpected final value: %+v", got)
	}
}

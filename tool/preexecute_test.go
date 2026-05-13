package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type prePostInput struct {
	Value string `json:"value" jsonschema:"required"`
}

// TestPreExecute_RejectWithHint_ReturnsErrorResult asserts that returning
// RejectWithHint("...") from a PreExecute hook produces an error result
// the agent loop can feed back to the LLM — the tool body never runs.
func TestPreExecute_RejectWithHint_ReturnsErrorResult(t *testing.T) {
	var ran bool
	tl := MustNewTool(prePostInput{},
		func(_ context.Context, in prePostInput) (string, error) {
			ran = true
			return "should not happen", nil
		},
		ToolConfig{Name: "guarded"},
		WithPreExecute(func(_ context.Context, in prePostInput) error {
			return RejectWithHint("publish_pages must succeed first")
		}),
	)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"value":"v"}`))
	if ran {
		t.Error("tool body must NOT run when PreExecute rejects")
	}
	if err == nil {
		t.Fatal("expected error from rejected PreExecute")
	}
	if !strings.Contains(err.Error(), "publish_pages must succeed first") {
		t.Errorf("error should carry hint text, got %v", err)
	}
	if !IsRejection(err) {
		t.Error("error should be detectable as a rejection via IsRejection")
	}
	_ = out
}

// TestPreExecute_AcceptsAndForwards asserts the happy path: PreExecute
// returning nil lets the tool body run normally and the body's output is
// returned unchanged.
func TestPreExecute_AcceptsAndForwards(t *testing.T) {
	tl := MustNewTool(prePostInput{},
		func(_ context.Context, in prePostInput) (string, error) {
			return "got " + in.Value, nil
		},
		ToolConfig{Name: "ok"},
		WithPreExecute(func(_ context.Context, in prePostInput) error {
			if in.Value == "" {
				return RejectWithHint("value required")
			}
			return nil
		}),
	)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"value":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "got hello" {
		t.Errorf("expected 'got hello', got %q", out)
	}
}

// TestPreExecute_NonRejectionErrorPropagatesAsExecutionFailure asserts
// that other errors from PreExecute (e.g. infrastructure failures) are
// propagated normally — not as rejection feedback. The framework retries
// or surfaces them per the tool's Retries config.
func TestPreExecute_NonRejectionErrorPropagatesAsExecutionFailure(t *testing.T) {
	plainErr := &testError{"db down"}
	tl := MustNewTool(prePostInput{},
		func(_ context.Context, _ prePostInput) (string, error) { return "", nil },
		ToolConfig{Name: "infra"},
		WithPreExecute(func(_ context.Context, _ prePostInput) error {
			return plainErr
		}),
	)

	_, err := tl.Execute(context.Background(), json.RawMessage(`{"value":"v"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if IsRejection(err) {
		t.Error("plain errors must not be classified as rejections")
	}
}

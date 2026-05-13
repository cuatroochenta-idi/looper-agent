package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// These tests document gaps in the hand-rolled validator that a spec-compliant
// library should cover. The agent feeds validation errors back to the LLM for
// self-correction, so catching real schema violations is correctness-critical.

type rangeInput struct {
	Count int `json:"count" jsonschema:"description=Count,minimum=1,maximum=10"`
}

// TestValidate_NumberOutOfRange asserts that minimum/maximum on integer
// fields actually rejects out-of-range values. The hand-rolled validator
// reads "minimum" as float64 only — int constraints from json.Unmarshal of
// schema produce float64 too, so this should work today; the test guards the
// invariant after migration.
func TestValidate_NumberOutOfRange(t *testing.T) {
	tl := MustNewTool(rangeInput{}, func(ctx context.Context, in rangeInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "count"})

	if err := Validate(tl, json.RawMessage(`{"count":5}`)); err != nil {
		t.Errorf("expected 5 to be valid, got: %v", err)
	}
	if err := Validate(tl, json.RawMessage(`{"count":0}`)); err == nil {
		t.Errorf("expected 0 < minimum to be rejected")
	}
	if err := Validate(tl, json.RawMessage(`{"count":11}`)); err == nil {
		t.Errorf("expected 11 > maximum to be rejected")
	}
}

// TestValidate_NestedRequiredMissing asserts that nested objects also enforce
// their own "required". The hand-rolled validator does walk into nested
// objects via the recursive call but doesn't reliably surface inner errors —
// this test pins the contract.
func TestValidate_NestedRequiredMissing(t *testing.T) {
	tl := MustNewTool(NestedInput{}, func(ctx context.Context, in NestedInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "nested"})

	// user.id is required; missing it should fail.
	err := Validate(tl, json.RawMessage(`{"user":{"email":"x@example.com"}}`))
	if err == nil {
		t.Fatal("expected error for missing nested required field user.id")
	}
}

// TestValidate_ArrayItemsTypeCheck asserts that arrays validate their items.
// The hand-rolled validateArray is a no-op — this test currently fails.
func TestValidate_ArrayItemsTypeCheck(t *testing.T) {
	tl := MustNewTool(sliceOfStructs{}, func(ctx context.Context, in sliceOfStructs) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "list"})

	// items[0] is a string instead of an object — should be rejected.
	err := Validate(tl, json.RawMessage(`{"items":["not an object"]}`))
	if err == nil {
		t.Fatal("expected array-items-type-mismatch to be rejected")
	}
}

// TestValidate_AdditionalPropertiesRejected asserts that unknown properties
// are rejected on tool inputs — required for OpenAI strict mode parity.
// Currently the hand-rolled validator silently accepts them.
func TestValidate_AdditionalPropertiesRejected(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, in SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "simple"})

	err := Validate(tl, json.RawMessage(`{"name":"x","count":1,"unexpected":true}`))
	if err == nil {
		t.Fatal("expected unknown property 'unexpected' to be rejected")
	}
}

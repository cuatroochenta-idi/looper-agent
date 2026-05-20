package openai

import (
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/openai/openai-go"
)

const minimalSchemaJSON = `{
  "type": "object",
  "properties": {"name": {"type": "string"}},
  "required": ["name"]
}`

func TestBuildResponseFormatParams_AutoUsesJSONSchema(t *testing.T) {
	rf, err := buildResponseFormatParams([]byte(minimalSchemaJSON), "candidate", provider.ResponseFormatAuto)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rf == nil || rf.OfJSONSchema == nil {
		t.Fatalf("expected json_schema variant, got %#v", rf)
	}
	if got, want := rf.OfJSONSchema.JSONSchema.Name, "candidate"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if rf.OfJSONObject != nil {
		t.Errorf("json_object variant should be unset")
	}
}

func TestBuildResponseFormatParams_AutoFallsBackToNilWithoutSchema(t *testing.T) {
	rf, err := buildResponseFormatParams(nil, "", provider.ResponseFormatAuto)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rf != nil {
		t.Fatalf("expected nil response_format when schema is empty, got %#v", rf)
	}
}

func TestBuildResponseFormatParams_ExplicitJSONSchemaSameAsAuto(t *testing.T) {
	auto, err := buildResponseFormatParams([]byte(minimalSchemaJSON), "x", provider.ResponseFormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := buildResponseFormatParams([]byte(minimalSchemaJSON), "x", provider.ResponseFormatJSONSchema)
	if err != nil {
		t.Fatal(err)
	}
	if (auto == nil) != (explicit == nil) {
		t.Fatalf("nil-ness diverged: auto=%v explicit=%v", auto == nil, explicit == nil)
	}
	if auto.OfJSONSchema.JSONSchema.Name != explicit.OfJSONSchema.JSONSchema.Name {
		t.Errorf("name diverged")
	}
}

func TestBuildResponseFormatParams_JSONObjectIgnoresSchema(t *testing.T) {
	rf, err := buildResponseFormatParams([]byte(minimalSchemaJSON), "irrelevant", provider.ResponseFormatJSONObject)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rf == nil || rf.OfJSONObject == nil {
		t.Fatalf("expected json_object variant, got %#v", rf)
	}
	if rf.OfJSONSchema != nil {
		t.Errorf("json_schema variant should be unset in json_object mode")
	}
}

func TestBuildResponseFormatParams_JSONObjectWorksWithoutSchema(t *testing.T) {
	// Some callers downgrade to json_object and stop sending a schema —
	// embed it in the system prompt instead. We must still emit
	// response_format: {type: "json_object"} so the model knows to JSON-ify.
	rf, err := buildResponseFormatParams(nil, "", provider.ResponseFormatJSONObject)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rf == nil || rf.OfJSONObject == nil {
		t.Fatalf("expected json_object variant, got %#v", rf)
	}
}

func TestBuildResponseFormatParams_NoneSuppressesResponseFormat(t *testing.T) {
	rf, err := buildResponseFormatParams([]byte(minimalSchemaJSON), "x", provider.ResponseFormatNone)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rf != nil {
		t.Fatalf("expected nil response_format with mode=none, got %#v", rf)
	}
}

func TestBuildResponseFormatParams_InvalidSchemaErrors(t *testing.T) {
	_, err := buildResponseFormatParams([]byte("not json"), "x", provider.ResponseFormatAuto)
	if err == nil {
		t.Fatalf("expected error on invalid JSON schema, got nil")
	}
}

// TestExtraParamsSerialization ensures ExtraParams ends up in the marshaled
// request body the underlying SDK sends. This guards the OpenRouter
// require_parameters use case from regressing.
func TestExtraParamsSerialization(t *testing.T) {
	var params openai.ChatCompletionNewParams
	params.Model = "qwen/qwen3.6-flash"
	params.SetExtraFields(map[string]any{
		"provider": map[string]any{"require_parameters": true},
	})
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	prov, ok := decoded["provider"].(map[string]any)
	if !ok {
		t.Fatalf("expected provider map in marshaled body, got %T (full: %s)", decoded["provider"], raw)
	}
	if got, want := prov["require_parameters"], true; got != want {
		t.Errorf("require_parameters = %v, want %v", got, want)
	}
}

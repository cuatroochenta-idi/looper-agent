package anthropic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type schemaIn struct {
	SKU  string `json:"sku" jsonschema:"description=The SKU to look up,required"`
	Note string `json:"note" jsonschema:"description=Optional note"`
}

// TestToolSchemaIsNotDoubleNested is the regression guard for a bug that
// silently broke every Anthropic tool call: the whole JSON Schema was
// assigned to input_schema.properties, so the model saw parameters named
// "type", "properties", "required" and "$schema" instead of the real ones,
// and no top-level required list.
func TestToolSchemaIsNotDoubleNested(t *testing.T) {
	tl := tool.MustNewTool(schemaIn{},
		func(_ context.Context, in schemaIn) (string, error) { return "", nil },
		tool.ToolConfig{Name: "lookup_stock", Description: "look up stock"},
	)

	p := NewProvider("k")
	raw, err := json.Marshal(p.Translator().ToNative("sys", nil, []*tool.Tool{tl}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var payload struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type       string         `json:"type"`
				Required   []string       `json:"required"`
				Properties map[string]any `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, raw)
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(payload.Tools))
	}
	got := payload.Tools[0].InputSchema

	if got.Type != "object" {
		t.Errorf("input_schema.type = %q, want object", got.Type)
	}

	// The real parameters must be directly under properties...
	if _, ok := got.Properties["sku"]; !ok {
		t.Errorf("properties must expose %q directly, got keys %v", "sku", keysOf(got.Properties))
	}
	if _, ok := got.Properties["note"]; !ok {
		t.Errorf("properties must expose %q directly, got keys %v", "note", keysOf(got.Properties))
	}

	// ...and schema keywords must NOT appear there as if they were params.
	for _, leaked := range []string{"type", "properties", "required", "$schema"} {
		if _, ok := got.Properties[leaked]; ok {
			t.Errorf("schema keyword %q leaked into properties as a parameter", leaked)
		}
	}

	// required must survive at the top level, or the model is free to omit
	// mandatory arguments. (The schema generator decides which fields land
	// here; all that matters is that the list reaches the wire.)
	if len(got.Required) == 0 {
		t.Error("required must be forwarded to the top level, got none")
	}
	var sawSKU bool
	for _, r := range got.Required {
		if r == "sku" {
			sawSKU = true
		}
	}
	if !sawSKU {
		t.Errorf("required = %v, want it to contain sku", got.Required)
	}
}

// TestToolSchemaForwardsConstraints checks that keywords with no field on
// ToolInputSchemaParam still reach the wire via ExtraFields.
func TestToolSchemaForwardsConstraints(t *testing.T) {
	in := buildInputSchema(map[string]any{
		"type":                 "object",
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []any{"a"},
		"additionalProperties": false,
		"$defs":                map[string]any{"X": map[string]any{"type": "string"}},
	})

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if v, ok := got["additionalProperties"]; !ok || v != false {
		t.Errorf("additionalProperties must be forwarded, got %v", got["additionalProperties"])
	}
	if _, ok := got["$defs"]; !ok {
		t.Error("$defs must be forwarded")
	}
	// $schema is metadata Anthropic does not accept.
	if _, ok := got["$schema"]; ok {
		t.Error("$schema must be dropped")
	}
}

// A tool with no parameters must still send an object, not a null.
func TestToolSchemaEmptyProperties(t *testing.T) {
	raw, err := json.Marshal(buildInputSchema(map[string]any{"type": "object"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties must be an object, got %T", got["properties"])
	}
	if len(props) != 0 {
		t.Errorf("want empty properties, got %v", props)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

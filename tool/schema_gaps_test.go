package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// resolveDefRef walks a "$ref": "#/$defs/<name>" node back to its definition
// inside the same root schema. Returns the underlying schema map. Fails the
// test on any structural surprise (non-$ref node, malformed pointer, missing
// $defs entry) so callers can chain assertions on the resolved shape.
func resolveDefRef(t *testing.T, root, node map[string]any) map[string]any {
	t.Helper()
	ref, ok := node["$ref"].(string)
	if !ok {
		t.Fatalf("expected $ref node, got %v", node)
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		t.Fatalf("unexpected $ref shape %q", ref)
	}
	name := strings.TrimPrefix(ref, prefix)
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("root schema missing $defs to resolve %q", name)
	}
	target, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("$defs entry %q missing, got keys %v", name, defs)
	}
	return target
}

// These tests document gaps in the hand-rolled schema generator that need to
// pass after the invopop/jsonschema migration. Each case below corresponds to
// a real production failure mode — strict-mode OpenAI rejection, multi-modal
// payloads, or LLM tools consuming structs with realistic Go types.

type timeInput struct {
	When time.Time `json:"when" jsonschema:"description=Event timestamp"`
}

// TestSchema_TimeTime asserts that time.Time becomes
// {"type":"string","format":"date-time"} instead of being walked recursively
// as a struct. The hand-rolled generator produces a nonsensical
// {"type":"object","properties":{wall:..., ext:..., loc:...}}.
func TestSchema_TimeTime(t *testing.T) {
	raw, err := GenerateSchema(timeInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := m["properties"].(map[string]any)
	when, ok := props["when"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'when' property: %v", props)
	}
	if when["type"] != "string" {
		t.Errorf("time.Time should map to string, got %v (full=%v)", when["type"], when)
	}
	if when["format"] != "date-time" {
		t.Errorf("time.Time should carry format=date-time, got %v", when["format"])
	}
}

type pointerHolder struct {
	User *UserInfo `json:"user" jsonschema:"required"`
}

// TestSchema_PointerToStruct asserts that *Struct fields produce a usable
// object schema. Named structs go through $defs/$ref so we resolve before
// inspecting the underlying shape.
func TestSchema_PointerToStruct(t *testing.T) {
	raw, err := GenerateSchema(pointerHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	props := m["properties"].(map[string]any)
	user, ok := props["user"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'user' property: %v", props)
	}
	def := resolveDefRef(t, m, user)
	if def["type"] != "object" {
		t.Errorf("*Struct should produce object schema, got %v", def["type"])
	}
	userProps, ok := def["properties"].(map[string]any)
	if !ok {
		t.Fatalf("pointer struct should expose its fields, got %v", def)
	}
	if _, ok := userProps["id"]; !ok {
		t.Errorf("pointer struct should preserve nested fields, got %v", userProps)
	}
}

type sliceOfStructs struct {
	Items []UserInfo `json:"items" jsonschema:"description=List of users"`
}

// TestSchema_SliceOfStructsHasItems asserts items schema is fully populated
// for arrays of structs — required by OpenAI strict mode and Anthropic
// tool_use validation.
func TestSchema_SliceOfStructsHasItems(t *testing.T) {
	raw, err := GenerateSchema(sliceOfStructs{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	props := m["properties"].(map[string]any)
	items, ok := props["items"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'items' property: %v", props)
	}
	if items["type"] != "array" {
		t.Errorf("expected array, got %v", items["type"])
	}
	inner, ok := items["items"].(map[string]any)
	if !ok {
		t.Fatalf("array missing items schema: %v", items)
	}
	def := resolveDefRef(t, m, inner)
	if def["type"] != "object" {
		t.Errorf("inner items should resolve to object, got %v", def["type"])
	}
	innerProps, ok := def["properties"].(map[string]any)
	if !ok || innerProps["id"] == nil {
		t.Errorf("slice-of-struct should expose element fields via $defs, got %v", def)
	}
}

// TestSchema_AdditionalPropertiesFalse asserts the generator emits
// "additionalProperties": false on object schemas. OpenAI's strict tool mode
// rejects schemas that allow extra properties.
func TestSchema_AdditionalPropertiesFalse(t *testing.T) {
	raw, err := GenerateSchema(SimpleInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	ap, ok := m["additionalProperties"]
	if !ok {
		t.Fatal("expected additionalProperties key on root object schema")
	}
	b, ok := ap.(bool)
	if !ok || b {
		t.Errorf("additionalProperties should be false, got %v (%T)", ap, ap)
	}
}

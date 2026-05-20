package google

import (
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/tool"
	genai "google.golang.org/genai"
)

// TestConvertSchema_PrimitiveArray covers the original Gemini 400 error:
// invopop emits []string as {type:array, items:{type:string}} and the old
// convertSchema dropped items entirely. With the new flatten+convert
// pipeline Items must survive with the right element type.
func TestConvertSchema_PrimitiveArray(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"table_names": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
	}
	got := convertSchema(in)
	if got == nil {
		t.Fatal("convertSchema returned nil")
	}
	field := got.Properties["table_names"]
	if field == nil {
		t.Fatal("table_names property missing")
	}
	if field.Type != genai.TypeArray {
		t.Fatalf("table_names type = %q, want ARRAY", field.Type)
	}
	if field.Items == nil {
		t.Fatal("table_names.items is nil — the original Gemini bug")
	}
	if field.Items.Type != genai.TypeString {
		t.Fatalf("table_names.items.type = %q, want STRING", field.Items.Type)
	}
}

// TestConvertSchema_StructArrayViaRef covers a []Struct field: invopop
// emits the array element as {$ref: #/$defs/TypeName} with the struct
// shape in $defs. convertSchema must inline the def so Gemini sees a
// proper object schema in Items.
func TestConvertSchema_StructArrayViaRef(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"TableDef": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
				"required": []any{"name"},
			},
		},
		"properties": map[string]any{
			"tables": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/$defs/TableDef"},
			},
		},
	}
	got := convertSchema(in)
	field := got.Properties["tables"]
	if field == nil || field.Items == nil {
		t.Fatalf("tables.items missing — got %+v", field)
	}
	if field.Items.Type != genai.TypeObject {
		t.Fatalf("items.type = %q, want OBJECT", field.Items.Type)
	}
	name := field.Items.Properties["name"]
	if name == nil || name.Type != genai.TypeString {
		t.Fatalf("inlined TableDef lost its properties: %+v", field.Items)
	}
	if len(field.Items.Required) != 1 || field.Items.Required[0] != "name" {
		t.Fatalf("inlined required slice lost: %v", field.Items.Required)
	}
}

// TestConvertSchema_RecursiveCycleBreaks is the load-bearing case: a
// type that references itself through a property (Section.Children
// []Section). Naive inlining would loop forever; the visited-path
// tracker must terminate on the second occurrence with a permissive
// object schema. The model still sees one full level of structure.
func TestConvertSchema_RecursiveCycleBreaks(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"Section": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"heading": map[string]any{"type": "string"},
					"body":    map[string]any{"type": "string"},
					"children": map[string]any{
						"type":  "array",
						"items": map[string]any{"$ref": "#/$defs/Section"},
					},
				},
				"required": []any{"heading"},
			},
		},
		"properties": map[string]any{
			"sections": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/$defs/Section"},
			},
		},
	}
	got := convertSchema(in)

	// Level 0: sections.items is the inlined Section.
	section := got.Properties["sections"].Items
	if section == nil || section.Type != genai.TypeObject {
		t.Fatalf("level-0 Section not inlined: %+v", section)
	}
	if section.Properties["heading"] == nil {
		t.Fatal("level-0 lost heading property")
	}

	// Level 1: section.children.items must be present but collapsed —
	// type=object, no nested Section properties, with a description that
	// signals the recursion.
	children := section.Properties["children"]
	if children == nil || children.Type != genai.TypeArray {
		t.Fatalf("children field missing or not array: %+v", children)
	}
	if children.Items == nil {
		t.Fatal("children.items missing after cycle collapse")
	}
	if children.Items.Type != genai.TypeObject {
		t.Fatalf("collapsed cycle should be object, got %q", children.Items.Type)
	}
	if len(children.Items.Properties) != 0 {
		t.Fatalf("collapsed cycle should not carry properties, got %v", children.Items.Properties)
	}
	if children.Items.Description == "" {
		t.Fatal("collapsed cycle should keep a hint description")
	}
}

// TestConvertSchema_SiblingRefsBothInline guards against the
// visited-path tracker leaking across sibling branches. The same $ref
// appearing under two unrelated properties must inline fully in both,
// not be falsely flagged as a cycle in the second occurrence.
func TestConvertSchema_SiblingRefsBothInline(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"Entry": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
			},
		},
		"properties": map[string]any{
			"left":  map[string]any{"$ref": "#/$defs/Entry"},
			"right": map[string]any{"$ref": "#/$defs/Entry"},
		},
	}
	got := convertSchema(in)
	for _, side := range []string{"left", "right"} {
		f := got.Properties[side]
		if f == nil || f.Type != genai.TypeObject {
			t.Fatalf("%s not inlined: %+v", side, f)
		}
		if f.Properties["id"] == nil {
			t.Fatalf("%s lost its id field", side)
		}
	}
}

// TestConvertSchema_PreservesOriginalFields makes sure the refactor
// didn't drop any of the fields the previous convertSchema handled
// (type, description, enum, required, properties).
func TestConvertSchema_PreservesOriginalFields(t *testing.T) {
	in := map[string]any{
		"type":        "object",
		"description": "root",
		"properties": map[string]any{
			"status": map[string]any{
				"type":        "string",
				"description": "lifecycle state",
				"enum":        []any{"draft", "ready"},
			},
		},
		"required": []any{"status"},
	}
	got := convertSchema(in)
	if got.Type != genai.TypeObject || got.Description != "root" {
		t.Fatalf("root type/description lost: %+v", got)
	}
	if len(got.Required) != 1 || got.Required[0] != "status" {
		t.Fatalf("required lost: %v", got.Required)
	}
	status := got.Properties["status"]
	if status == nil || status.Type != genai.TypeString {
		t.Fatalf("status field broken: %+v", status)
	}
	if len(status.Enum) != 2 || status.Enum[0] != "draft" || status.Enum[1] != "ready" {
		t.Fatalf("enum lost: %v", status.Enum)
	}
}

// TestConvertSchema_BonusFields exercises the fields the original
// implementation didn't translate at all: format, nullable, length and
// numeric bounds, default. None of them should cause panics and each
// should land in the matching genai.Schema field.
func TestConvertSchema_BonusFields(t *testing.T) {
	in := map[string]any{
		"type":      "object",
		"properties": map[string]any{
			"age": map[string]any{
				"type":    "integer",
				"format":  "int32",
				"minimum": float64(0),
				"maximum": float64(120),
				"default": float64(18),
			},
			"name": map[string]any{
				"type":      "string",
				"minLength": float64(1),
				"maxLength": float64(64),
				"nullable":  true,
			},
		},
	}
	got := convertSchema(in)
	age := got.Properties["age"]
	if age.Format != "int32" {
		t.Errorf("format lost: %q", age.Format)
	}
	if age.Minimum == nil || *age.Minimum != 0 {
		t.Errorf("minimum lost: %+v", age.Minimum)
	}
	if age.Maximum == nil || *age.Maximum != 120 {
		t.Errorf("maximum lost: %+v", age.Maximum)
	}
	if age.Default == nil {
		t.Error("default lost")
	}
	name := got.Properties["name"]
	if name.MinLength == nil || *name.MinLength != 1 {
		t.Errorf("minLength lost: %+v", name.MinLength)
	}
	if name.MaxLength == nil || *name.MaxLength != 64 {
		t.Errorf("maxLength lost: %+v", name.MaxLength)
	}
	if name.Nullable == nil || *name.Nullable != true {
		t.Errorf("nullable lost: %+v", name.Nullable)
	}
}

// TestConvertSchema_UnknownRefDegrades guards graceful failure when the
// schema references a $defs entry that isn't present. Producing nil or
// panicking would propagate to the Gemini API call; instead we emit a
// permissive object so the request still goes through.
func TestConvertSchema_UnknownRefDegrades(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"$ref": "#/$defs/MissingType"},
		},
	}
	got := convertSchema(in)
	field := got.Properties["x"]
	if field == nil || field.Type != genai.TypeObject {
		t.Fatalf("missing ref should degrade to object, got %+v", field)
	}
	if field.Description == "" {
		t.Error("degraded ref should carry a description hint")
	}
}

// TestConvertSchema_NilAndEmpty covers the trivial branches:
// convertSchema(nil) returns nil and an empty map yields a zero
// *genai.Schema, both of which the caller in google.go relies on.
func TestConvertSchema_NilAndEmpty(t *testing.T) {
	if got := convertSchema(nil); got != nil {
		t.Fatalf("nil input should yield nil, got %+v", got)
	}
	if got := convertSchema(map[string]any{}); got == nil {
		t.Fatal("empty map should yield a zero *genai.Schema, not nil")
	}
}

// TestConvertSchema_RealInvopopRecursiveType bridges the unit tests
// above to the actual production pipeline: take a real recursive Go
// type, feed it through tool.GenerateSchema (invopop) and then through
// convertSchema, and assert that the cycle is broken and no fields are
// lost. This mirrors what happens when the agent registers PRDToolkit.
func TestConvertSchema_RealInvopopRecursiveType(t *testing.T) {
	type Node struct {
		Heading  string `json:"heading"`
		Body     string `json:"body,omitempty"`
		Children []Node `json:"children,omitempty"`
	}
	type Input struct {
		Sections []Node `json:"sections"`
	}

	raw, err := tool.GenerateSchema(Input{})
	if err != nil {
		t.Fatalf("invopop schema generation failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	got := convertSchema(m)
	sections := got.Properties["sections"]
	if sections == nil || sections.Type != genai.TypeArray {
		t.Fatalf("sections array not produced: %+v", sections)
	}
	if sections.Items == nil || sections.Items.Type != genai.TypeObject {
		t.Fatalf("Node not inlined at level 0: %+v", sections.Items)
	}
	if sections.Items.Properties["heading"] == nil {
		t.Fatal("level-0 Node lost heading")
	}
	deeper := sections.Items.Properties["children"]
	if deeper == nil || deeper.Items == nil {
		t.Fatalf("children/items missing: %+v", deeper)
	}
	if len(deeper.Items.Properties) != 0 {
		t.Fatalf("recursion not collapsed: items still has %d properties", len(deeper.Items.Properties))
	}
}

package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests pin down the schema generator's behaviour on the cases that
// originally motivated the invopop/jsonschema adoption. They assert the
// emitted shape end-to-end so a future generator swap (or invopop upgrade)
// surfaces regressions immediately.

// ---------- helpers ----------------------------------------------------------

func decodeSchema(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal schema: %v\nraw=%s", err, string(raw))
	}
	return m
}

func props(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	p, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing properties: %v", schema)
	}
	return p
}

func defs(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	d, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing $defs (recursive types require them): %v", schema)
	}
	return d
}

func required(t *testing.T, schema map[string]any) []string {
	t.Helper()
	r, ok := schema["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(r))
	for _, v := range r {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasRequired(t *testing.T, schema map[string]any, name string) bool {
	t.Helper()
	for _, r := range required(t, schema) {
		if r == name {
			return true
		}
	}
	return false
}

// resolveRef returns the schema entry under "$defs/<name>" when the given
// node is a $ref pointing into the root's $defs. Fails the test if either
// the node isn't a $ref or the target is missing.
func resolveRef(t *testing.T, root, node map[string]any) map[string]any {
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
	d := defs(t, root)
	target, ok := d[name].(map[string]any)
	if !ok {
		t.Fatalf("$defs missing entry %q: %v", name, d)
	}
	return target
}

// ---------- json.RawMessage --------------------------------------------------

type rawMessageHolder struct {
	Sections json.RawMessage `json:"sections" jsonschema_description:"Array of section objects (recursive shape pinned by description, type-free schema)"`
}

// TestSchema_RawMessageIsPermissive asserts json.RawMessage maps to a
// permissive `{}` schema (NOT array-of-integer, NOT bare `true`). The bug
// this guards against: descending into the underlying []byte and emitting
// {type:array, items:{type:integer}} prompted gpt-5-mini to send `[1,2,3]`
// in place of structured data.
func TestSchema_RawMessageIsPermissive(t *testing.T) {
	raw, err := GenerateSchema(rawMessageHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	sections, ok := props(t, m)["sections"].(map[string]any)
	if !ok {
		t.Fatalf("sections should be an object schema (mapper output), got %v", props(t, m)["sections"])
	}
	if _, hasType := sections["type"]; hasType {
		t.Errorf("json.RawMessage must NOT declare a type (got %v)", sections["type"])
	}
	if _, hasItems := sections["items"]; hasItems {
		t.Errorf("json.RawMessage must NOT carry items (got %v)", sections["items"])
	}
}

// TestSchema_RawMessageAcceptsAnyJSON validates the compiled schema accepts
// every shape a caller would realistically pass through json.RawMessage
// (array of nested objects, plain object, primitives).
func TestSchema_RawMessageAcceptsAnyJSON(t *testing.T) {
	raw, err := GenerateSchema(rawMessageHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	sch, err := compileSchema("rawmsg", raw)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, body := range []string{
		`{"sections":[{"id":"a","children":[{"id":"b"}]}]}`,
		`{"sections":{"any":"object"}}`,
		`{"sections":"a string"}`,
		`{"sections":42}`,
	} {
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("test fixture unmarshal: %v", err)
		}
		if err := sch.Validate(v); err != nil {
			t.Errorf("RawMessage schema rejected legitimate payload %s: %v", body, err)
		}
	}
}

// ---------- self-referential type --------------------------------------------

type recursiveSection struct {
	ID       string             `json:"id" jsonschema:"required"`
	Heading  string             `json:"heading,omitempty"`
	Children []recursiveSection `json:"children,omitempty"`
}

type recursiveInput struct {
	Root recursiveSection `json:"root" jsonschema:"required"`
}

// TestSchema_RecursiveTypeUsesDefs asserts the generator handles a struct
// that references itself via a slice field by emitting $defs + $ref instead
// of either inlining infinitely or blowing the stack.
func TestSchema_RecursiveTypeUsesDefs(t *testing.T) {
	raw, err := GenerateSchema(recursiveInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	d := defs(t, m)
	def, ok := d["recursiveSection"].(map[string]any)
	if !ok {
		t.Fatalf("expected a $defs entry for recursiveSection, got %v", keysOf(d))
	}

	children := props(t, def)["children"].(map[string]any)
	if children["type"] != "array" {
		t.Errorf("children should be an array, got %v", children["type"])
	}
	inner := children["items"].(map[string]any)
	if ref, _ := inner["$ref"].(string); ref != "#/$defs/recursiveSection" {
		t.Errorf("children.items should $ref recursiveSection, got %v", inner)
	}
}

// TestSchema_RecursiveSchemaCompiles validates the recursive schema with a
// real deeply-nested document — without $defs/$ref this either stack-
// overflowed or produced a finite-depth schema; with $defs the validator
// honours arbitrary nesting depth.
func TestSchema_RecursiveSchemaCompiles(t *testing.T) {
	raw, err := GenerateSchema(recursiveInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	sch, err := compileSchema("recursive", raw)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	body := `{
		"root": {
			"id": "a",
			"heading": "A",
			"children": [
				{"id": "b", "heading": "B", "children": [
					{"id": "c", "heading": "C", "children": [
						{"id": "d", "heading": "D"}
					]}
				]}
			]
		}
	}`
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	if err := sch.Validate(v); err != nil {
		t.Errorf("recursive schema rejected valid deep tree: %v", err)
	}
}

// ---------- mutual recursion -------------------------------------------------

type nodeA struct {
	Name string  `json:"name"`
	B    *nodeB  `json:"b,omitempty"`
	Refs []nodeA `json:"refs,omitempty"`
}
type nodeB struct {
	Tag string  `json:"tag"`
	A   *nodeA  `json:"a,omitempty"`
	Cs  []nodeB `json:"cs,omitempty"`
}

type mutualInput struct {
	Start nodeA `json:"start"`
}

// TestSchema_MutualRecursionUsesDefs covers an A→B→A cycle: both types must
// land in $defs and the cross-references must resolve to those entries.
func TestSchema_MutualRecursionUsesDefs(t *testing.T) {
	raw, err := GenerateSchema(mutualInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	d := defs(t, m)
	for _, want := range []string{"nodeA", "nodeB"} {
		if _, ok := d[want]; !ok {
			t.Fatalf("expected $defs entry %q, got %v", want, keysOf(d))
		}
	}

	defA := d["nodeA"].(map[string]any)
	bField := props(t, defA)["b"].(map[string]any)
	if ref, _ := bField["$ref"].(string); ref != "#/$defs/nodeB" {
		t.Errorf("nodeA.b should $ref nodeB, got %v", bField)
	}
}

// ---------- embedded anonymous struct ----------------------------------------

type runtimeContext struct {
	AppID  string `json:"-"`
	OrgID  string `json:"-"`
	DBName string `json:"-"`
}

type promotedInput struct {
	runtimeContext
	Names []string `json:"names" jsonschema:"required"`
}

// TestSchema_EmbeddedAnonymousIsPromoted asserts that an embedded struct
// whose fields are all `json:"-"` produces a schema that exposes ONLY the
// parent's own fields — encoding/json drops the empty embed entirely and so
// must the schema generator. The pre-invopop generator leaked it as a
// required empty `runtimeContext` object the model had to satisfy on every
// call.
func TestSchema_EmbeddedAnonymousIsPromoted(t *testing.T) {
	raw, err := GenerateSchema(promotedInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)

	for _, forbidden := range []string{"runtimeContext", "RuntimeContext", "AppID", "OrgID", "DBName"} {
		if _, leaked := p[forbidden]; leaked {
			t.Errorf("anonymous-embedded field %q leaked into schema: %v", forbidden, p)
		}
	}
	if _, ok := p["names"]; !ok {
		t.Errorf("expected own field 'names' on schema: %v", p)
	}
	if hasRequired(t, m, "runtimeContext") {
		t.Errorf("embedded marker struct must not be required, required=%v", required(t, m))
	}
}

type sidebar struct {
	Open  bool   `json:"open"`
	Width int    `json:"width,omitempty"`
	Title string `json:"title,omitempty"`
}

type embedWithFields struct {
	sidebar
	UserID string `json:"user_id" jsonschema:"required"`
}

// TestSchema_EmbeddedPromotesRealFields asserts that an embedded struct
// with real JSON-visible fields has its fields flattened into the parent —
// matching encoding/json behaviour.
func TestSchema_EmbeddedPromotesRealFields(t *testing.T) {
	raw, err := GenerateSchema(embedWithFields{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)
	for _, want := range []string{"open", "width", "title", "user_id"} {
		if _, ok := p[want]; !ok {
			t.Errorf("expected promoted field %q in props, got %v", want, p)
		}
	}
	if _, leaked := p["sidebar"]; leaked {
		t.Errorf("embedded type name leaked as property: %v", p)
	}
	for _, want := range []string{"open", "user_id"} {
		if !hasRequired(t, m, want) {
			t.Errorf("expected required field %q, required=%v", want, required(t, m))
		}
	}
	for _, notWant := range []string{"width", "title"} {
		if hasRequired(t, m, notWant) {
			t.Errorf("did not expect %q in required, got %v", notWant, required(t, m))
		}
	}
}

type namedEmbed struct {
	sidebar `json:"sidebar"`
	UserID  string `json:"user_id"`
}

// TestSchema_EmbeddedWithExplicitJSONNameStaysNested asserts that an
// embedded struct WITH a json tag name is NOT promoted — encoding/json
// nests it under that name and so does the schema (here via $ref since the
// embedded type is named).
func TestSchema_EmbeddedWithExplicitJSONNameStaysNested(t *testing.T) {
	raw, err := GenerateSchema(namedEmbed{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)
	sb, ok := p["sidebar"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested 'sidebar' node, got %v", p)
	}
	def := resolveRef(t, m, sb)
	if def["type"] != "object" {
		t.Errorf("resolved sidebar def should be object, got %v", def["type"])
	}
	if _, leaked := p["open"]; leaked {
		t.Errorf("nested sidebar fields must not promote when json tag is set: %v", p)
	}
}

// ---------- interface{} ------------------------------------------------------

type anyHolder struct {
	Payload any `json:"payload"`
}

// TestSchema_InterfaceIsPermissive asserts that an interface{} field
// produces the canonical "any JSON value" schema (`{}`), not invopop's
// default `true` (spec-valid but rejected by some strict provider
// validators) and not `{type:string}`.
func TestSchema_InterfaceIsPermissive(t *testing.T) {
	raw, err := GenerateSchema(anyHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	payload, ok := props(t, m)["payload"].(map[string]any)
	if !ok {
		t.Fatalf("interface{} should produce an object-shaped schema (mapper override), got %v (%T)",
			props(t, m)["payload"], props(t, m)["payload"])
	}
	if _, has := payload["type"]; has {
		t.Errorf("interface{} should produce no type constraint, got %v", payload)
	}
}

// ---------- maps -------------------------------------------------------------

type stringMapHolder struct {
	Attrs map[string]int `json:"attrs"`
}

type intMapHolder struct {
	Lookup map[int]string `json:"lookup"`
}

func TestSchema_MapStringKeyExposesValueSchema(t *testing.T) {
	raw, _ := GenerateSchema(stringMapHolder{})
	m := decodeSchema(t, raw)
	attrs := props(t, m)["attrs"].(map[string]any)
	if attrs["type"] != "object" {
		t.Errorf("map should be object, got %v", attrs["type"])
	}
	ap, ok := attrs["additionalProperties"].(map[string]any)
	if !ok {
		t.Fatalf("expected additionalProperties to describe value, got %v", attrs)
	}
	if ap["type"] != "integer" {
		t.Errorf("map[string]int value schema should be integer, got %v", ap["type"])
	}
}

// TestSchema_MapNonStringKeyUsesPatternProperties asserts non-string-keyed
// maps are represented via patternProperties so the schema still validates
// the on-the-wire JSON object representation.
func TestSchema_MapNonStringKeyUsesPatternProperties(t *testing.T) {
	raw, _ := GenerateSchema(intMapHolder{})
	m := decodeSchema(t, raw)
	lookup := props(t, m)["lookup"].(map[string]any)
	if lookup["type"] != "object" {
		t.Errorf("non-string-keyed map should still be an object schema, got %v", lookup)
	}
	pp, ok := lookup["patternProperties"].(map[string]any)
	if !ok {
		t.Fatalf("non-string-keyed map should use patternProperties, got %v", lookup)
	}
	// Exactly one pattern entry whose schema describes the value type.
	if len(pp) != 1 {
		t.Errorf("expected a single pattern entry, got %v", pp)
	}
	for _, v := range pp {
		entry := v.(map[string]any)
		if entry["type"] != "string" {
			t.Errorf("map[int]string value should be string, got %v", entry["type"])
		}
	}
}

// ---------- pointer chains ---------------------------------------------------

type ptrChainHolder struct {
	Deep ***string `json:"deep,omitempty"`
}

func TestSchema_PointerChainsResolveToElem(t *testing.T) {
	raw, _ := GenerateSchema(ptrChainHolder{})
	m := decodeSchema(t, raw)
	deep := props(t, m)["deep"].(map[string]any)
	if deep["type"] != "string" {
		t.Errorf("triple pointer to string should be string, got %v", deep["type"])
	}
}

// ---------- omitempty / required parsing ------------------------------------

type omitFields struct {
	Always  string `json:"always"`
	Maybe   string `json:"maybe,omitempty"`
	Hidden  string `json:"-"`
	Renamed string `json:",omitempty"`
}

func TestSchema_TagParsingIsRobust(t *testing.T) {
	raw, err := GenerateSchema(omitFields{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)

	if _, leaked := p["Hidden"]; leaked {
		t.Errorf("json:\"-\" field leaked into schema: %v", p)
	}
	if _, ok := p["always"]; !ok {
		t.Errorf("expected 'always' in props: %v", p)
	}
	if _, ok := p["maybe"]; !ok {
		t.Errorf("expected 'maybe' in props: %v", p)
	}
	// `json:",omitempty"` falls back to the Go field name "Renamed".
	if _, ok := p["Renamed"]; !ok {
		t.Errorf("expected fallback name 'Renamed' (json:\",omitempty\"): %v", p)
	}
	if !hasRequired(t, m, "always") {
		t.Errorf("'always' should be required, required=%v", required(t, m))
	}
	if hasRequired(t, m, "maybe") {
		t.Errorf("'maybe' has omitempty and must NOT be required, required=%v", required(t, m))
	}
	if hasRequired(t, m, "Renamed") {
		t.Errorf("'Renamed' has omitempty and must NOT be required, required=%v", required(t, m))
	}
}

type requiredOverride struct {
	Maybe string `json:"maybe,omitempty" jsonschema:"required"`
}

// TestSchema_JsonschemaRequiredOverridesOmitempty: an explicit
// `jsonschema:"required"` wins over `,omitempty`.
func TestSchema_JsonschemaRequiredOverridesOmitempty(t *testing.T) {
	raw, _ := GenerateSchema(requiredOverride{})
	m := decodeSchema(t, raw)
	if !hasRequired(t, m, "maybe") {
		t.Errorf("jsonschema:required must override omitempty, required=%v", required(t, m))
	}
}

// ---------- private fields ---------------------------------------------------

type withPrivate struct {
	Public  string `json:"public"`
	private string //nolint:unused
}

func TestSchema_PrivateFieldsExcluded(t *testing.T) {
	raw, _ := GenerateSchema(withPrivate{})
	m := decodeSchema(t, raw)
	p := props(t, m)
	if _, leaked := p["private"]; leaked {
		t.Errorf("private field leaked into schema: %v", p)
	}
	if _, ok := p["public"]; !ok {
		t.Errorf("expected 'public' in props: %v", p)
	}
}

// ---------- description with commas / examples ------------------------------

type descriptionWithCommas struct {
	// Use jsonschema_description for content containing commas — the
	// canonical way to ship descriptions with embedded JSON examples.
	Sections json.RawMessage `json:"sections" jsonschema:"required" jsonschema_description:"JSON array of section objects: [{\"id\":\"objetivo\",\"children\":[{\"id\":\"sub\",\"body\":\"…\"}]}]"`
	// `jsonschema:"description=..."` supports backslash-escaped commas for
	// the inline case (kept here to pin invopop's escape behaviour). Each
	// enum value is its own `enum=X` attribute — the pipe-separated style
	// of the legacy hand-rolled generator is no longer supported.
	Status string `json:"status,omitempty" jsonschema:"description=Lifecycle state. Use draft\\, then approved\\, finally published.,enum=draft,enum=approved,enum=published"`
}

// TestSchema_DescriptionPreservesCommas asserts both escape paths preserve
// commas inside descriptions: the dedicated jsonschema_description tag for
// arbitrary content, and the inline `description=...` with backslash-
// escaped commas.
func TestSchema_DescriptionPreservesCommas(t *testing.T) {
	raw, err := GenerateSchema(descriptionWithCommas{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)

	sections := p["sections"].(map[string]any)
	desc, _ := sections["description"].(string)
	wantSubstr := `"children":[{"id":"sub"`
	if !strings.Contains(desc, wantSubstr) {
		t.Errorf("jsonschema_description should preserve commas inside JSON example, got %q", desc)
	}
	if !hasRequired(t, m, "sections") {
		t.Errorf("jsonschema:\"required\" must still be picked up alongside description, required=%v", required(t, m))
	}

	status := p["status"].(map[string]any)
	statusDesc, _ := status["description"].(string)
	if !strings.Contains(statusDesc, "draft, then approved, finally published") {
		t.Errorf("backslash-escaped commas in description= should unescape, got %q", statusDesc)
	}
	enum, ok := status["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Errorf("enum should still be parsed alongside comma-bearing description, got %v", status["enum"])
	}
}

// ---------- helpers ----------------------------------------------------------

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

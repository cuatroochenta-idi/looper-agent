package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

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

// ---------- json.RawMessage --------------------------------------------------

type rawMessageHolder struct {
	Sections json.RawMessage `json:"sections" jsonschema:"description=Array of section objects (recursive)"`
}

// TestSchema_RawMessageIsPermissive asserts json.RawMessage maps to a permissive
// schema (NOT array-of-integer). The bug this fixes: the generator descended
// into the underlying []byte and emitted {type:array, items:{type:integer}},
// which prompted gpt-5-mini to send `[1,2,3,4,5,6,7]` as the value.
func TestSchema_RawMessageIsPermissive(t *testing.T) {
	raw, err := GenerateSchema(rawMessageHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	sections := props(t, m)["sections"].(map[string]any)

	if _, hasType := sections["type"]; hasType {
		t.Errorf("json.RawMessage must NOT declare a type (saw %v)", sections["type"])
	}
	if _, hasItems := sections["items"]; hasItems {
		t.Errorf("json.RawMessage must NOT carry items (saw %v)", sections["items"])
	}
	if sections["description"] != "Array of section objects (recursive)" {
		t.Errorf("description tag lost on RawMessage field: %v", sections["description"])
	}
}

// TestSchema_RawMessageAcceptsAnyJSON ensures the compiled schema accepts the
// shapes a caller would realistically want to pass through (array of objects,
// nested objects, primitives) — confirming the relaxation is end-to-end.
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
	Heading  string             `json:"heading"`
	Children []recursiveSection `json:"children,omitempty"`
}

type recursiveInput struct {
	Root recursiveSection `json:"root" jsonschema:"required"`
}

// TestSchema_RecursiveTypeUsesDefs asserts the generator handles a struct that
// references itself via a slice field by emitting $defs + $ref instead of
// blowing the stack.
func TestSchema_RecursiveTypeUsesDefs(t *testing.T) {
	raw, err := GenerateSchema(recursiveInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)

	defs, ok := m["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("expected $defs on recursive schema, got %v", m)
	}
	// Find the recursiveSection def by exact-suffix match on the key.
	var defKey string
	for k := range defs {
		if strings.HasSuffix(k, "_recursiveSection") {
			defKey = k
			break
		}
	}
	if defKey == "" {
		t.Fatalf("expected a $defs entry for recursiveSection, got keys %v", keysOf(defs))
	}

	def := defs[defKey].(map[string]any)
	defProps := def["properties"].(map[string]any)
	children := defProps["children"].(map[string]any)
	if children["type"] != "array" {
		t.Errorf("children should be an array, got %v", children["type"])
	}
	inner := children["items"].(map[string]any)
	if ref, ok := inner["$ref"].(string); !ok || !strings.HasSuffix(ref, defKey) {
		t.Errorf("children.items should $ref the recursiveSection def, got %v", inner)
	}
}

// TestSchema_RecursiveSchemaCompiles validates the recursive schema with a
// real document. Without $defs/$ref the generator would either stack-overflow
// or produce a finite-depth schema; with $defs the compiler honours arbitrary
// nesting depth.
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

// TestSchema_MutualRecursionUsesDefs covers A → B → A cycle handling.
func TestSchema_MutualRecursionUsesDefs(t *testing.T) {
	raw, err := GenerateSchema(mutualInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	defs, ok := m["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("expected $defs on mutually-recursive schema, got %v", m)
	}
	hasA, hasB := false, false
	for k := range defs {
		if strings.HasSuffix(k, "_nodeA") {
			hasA = true
		}
		if strings.HasSuffix(k, "_nodeB") {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Fatalf("expected both nodeA and nodeB in $defs, got %v", keysOf(defs))
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

// TestSchema_EmbeddedAnonymousIsPromoted asserts that an embedded struct whose
// fields are all `json:"-"` produces a schema that exposes ONLY the parent's
// own fields — not a leaked "runtimeContext" required object.
func TestSchema_EmbeddedAnonymousIsPromoted(t *testing.T) {
	raw, err := GenerateSchema(promotedInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)

	for forbidden := range map[string]struct{}{"runtimeContext": {}, "RuntimeContext": {}, "AppID": {}, "OrgID": {}, "DBName": {}} {
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

// TestSchema_EmbeddedPromotesRealFields asserts that an embedded struct with
// real JSON-visible fields has its fields flattened into the parent (same as
// encoding/json behaviour).
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
	// `width` and `title` use omitempty → must NOT be required; `open` and
	// `user_id` lack omitempty → must be required.
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

// TestSchema_EmbeddedWithExplicitJSONNameStaysNested asserts that an embedded
// struct WITH a json tag name is NOT promoted — it nests under that name,
// matching encoding/json.
func TestSchema_EmbeddedWithExplicitJSONNameStaysNested(t *testing.T) {
	raw, err := GenerateSchema(namedEmbed{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	p := props(t, m)
	sb, ok := p["sidebar"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested 'sidebar' object, got %v", p)
	}
	if sb["type"] != "object" {
		t.Errorf("nested sidebar should be object, got %v", sb["type"])
	}
	if _, leaked := p["open"]; leaked {
		t.Errorf("nested sidebar fields must not promote when json tag is set: %v", p)
	}
}

// ---------- interface{} ------------------------------------------------------

type anyHolder struct {
	Payload any `json:"payload"`
}

func TestSchema_InterfaceIsPermissive(t *testing.T) {
	raw, err := GenerateSchema(anyHolder{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}
	m := decodeSchema(t, raw)
	payload := props(t, m)["payload"].(map[string]any)
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

func TestSchema_MapNonStringKeyFallsBackToNeutralObject(t *testing.T) {
	raw, _ := GenerateSchema(intMapHolder{})
	m := decodeSchema(t, raw)
	lookup := props(t, m)["lookup"].(map[string]any)
	if lookup["type"] != "object" {
		t.Errorf("non-string-keyed map should still be an object schema, got %v", lookup)
	}
	if _, has := lookup["additionalProperties"]; has {
		t.Errorf("non-string-keyed map must not declare additionalProperties shape, got %v", lookup)
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
// `jsonschema:"required"` wins over `,omitempty` (matches the original
// generator's documented behaviour).
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
	Sections json.RawMessage `json:"sections" jsonschema:"description=JSON array of section objects: [{\"id\":\"objetivo\",\"children\":[{\"id\":\"sub\",\"body\":\"…\"}]}], required"`
	Status   string          `json:"status,omitempty" jsonschema:"description=Lifecycle state. Use draft, then approved, finally published.,enum=draft|approved|published"`
}

// TestSchema_DescriptionPreservesCommas asserts the tag parser does NOT split
// the description at internal commas — the canonical bug that hid the actual
// shape of the value from the LLM.
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
		t.Errorf("description should preserve commas inside JSON example, got %q", desc)
	}
	if !hasRequired(t, m, "sections") {
		t.Errorf("trailing `, required` must still be recognised, required=%v", required(t, m))
	}

	status := p["status"].(map[string]any)
	statusDesc, _ := status["description"].(string)
	if !strings.Contains(statusDesc, "draft, then approved, finally published") {
		t.Errorf("status description should preserve prose commas, got %q", statusDesc)
	}
	enum, ok := status["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Errorf("status enum should still be parsed alongside comma-bearing description, got %v", status["enum"])
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

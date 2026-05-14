package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// timeType is captured once so typeToSchema can fast-path time.Time instead
// of walking it as a struct (which would otherwise expose unexported
// wall/ext/loc fields to the LLM as garbage properties).
var timeType = reflect.TypeFor[time.Time]()

// rawMessageType lets typeToSchema treat json.RawMessage as "any JSON value"
// rather than the underlying []byte. Without this the generator would emit
// "array of integer" for RawMessage fields and trick the LLM into sending
// indices (e.g. [1,2,3]) instead of the structured payload the description
// describes.
var rawMessageType = reflect.TypeFor[json.RawMessage]()

// GenerateSchema generates a JSON Schema from a Go struct value.
// It reads jsonschema struct tags to produce descriptions, enums,
// required fields, minimum/maximum constraints, default values, etc.
//
// Supported jsonschema tags:
//   - description: field description
//   - enum: pipe-separated allowed values
//   - minimum, maximum: numeric constraints
//   - required: marks field as required regardless of omitempty
//   - default: default value (string-typed)
//
// Self-referential and mutually-recursive struct types are resolved by
// promoting their definition into "$defs" and referencing it via "$ref",
// so the generator never blows the stack on shapes like `type Node struct{
// Children []Node }`. Non-recursive named types stay inlined to keep the
// emitted schema flat for callers that expect the simpler shape.
func GenerateSchema(v any) (json.RawMessage, error) {
	b := newSchemaBuilder()
	root := b.structSchema(reflect.TypeOf(v))
	if len(b.defs) > 0 {
		root["$defs"] = b.defs
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("generate schema: %w", err)
	}
	return out, nil
}

// schemaBuilder carries the in-progress $defs map and a cycle-detection set
// across a single GenerateSchema call.
type schemaBuilder struct {
	// defs holds the schema for every named struct type that turned out to be
	// referenced more than once (or recursively). Keys are JSON-pointer-safe
	// "<pkgpath>_<name>" strings.
	defs map[string]map[string]any
	// inProgress is the set of named struct types whose definition is mid
	// construction. A nested encounter of one of these types is the cycle
	// signal — we emit a $ref and the outer call finalises the definition.
	inProgress map[reflect.Type]bool
}

func newSchemaBuilder() *schemaBuilder {
	return &schemaBuilder{
		defs:       map[string]map[string]any{},
		inProgress: map[reflect.Type]bool{},
	}
}

// defName returns a JSON-pointer-safe identifier for a named struct type.
// Empty for anonymous structs (which cannot recurse and so never need $defs).
func (b *schemaBuilder) defName(t reflect.Type) string {
	name := t.Name()
	if name == "" {
		return ""
	}
	pkg := t.PkgPath()
	if pkg == "" {
		return name
	}
	// JSON pointer fragments treat '/' as separator and '~' as escape — strip
	// anything path-ish from the package path so the $ref is unambiguous.
	sanitized := strings.NewReplacer("/", "_", "~", "_", ".", "_", "-", "_").Replace(pkg)
	return sanitized + "_" + name
}

// structSchema builds the object schema for a struct type, recursing into
// its fields. Anonymous (embedded) struct fields without an explicit JSON
// name are promoted: their fields are flattened into the parent — mirroring
// encoding/json semantics.
func (b *schemaBuilder) structSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	props := schema["properties"].(map[string]any)
	var required []string

	b.collectFields(t, props, &required)

	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// collectFields walks t's exported fields into props/required, promoting
// anonymous struct fields per encoding/json conventions.
func (b *schemaBuilder) collectFields(t reflect.Type, props map[string]any, required *[]string) {
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// encoding/json's "amended visibility rule": an embedded struct field
		// of an unexported type is still walked for its EXPORTED subfields.
		// Only non-anonymous unexported fields are dropped here.
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		name, jsonOpts := parseJSONTag(f.Tag.Get("json"), f.Name)
		if jsonOpts.skip {
			// `json:"-"` (and only that) excludes the field outright.
			continue
		}

		// Anonymous (embedded) struct with no explicit JSON name → promote
		// its fields into the parent. This matches encoding/json behavior
		// and is how callers expect markers like `tools.AppContext` (all
		// fields tagged json:"-") to vanish from the schema entirely.
		if f.Anonymous && !jsonOpts.nameExplicit {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != timeType {
				b.collectFields(ft, props, required)
				continue
			}
			// Anonymous non-struct field falls through to normal handling
			// using the type's Name as the JSON key (Go's own default).
		}

		prop := b.typeToSchema(f.Type)
		applyTagAnnotations(prop, f.Tag.Get("jsonschema"))
		if desc := f.Tag.Get("jsonschema_description"); desc != "" {
			prop["description"] = desc
		}

		props[name] = prop
		if isFieldRequired(f.Tag, jsonOpts) {
			*required = append(*required, name)
		}
	}
}

// typeToSchema produces the JSON Schema fragment for an arbitrary Go type.
// Recursion is bounded by $defs/$ref for self-referential named types.
func (b *schemaBuilder) typeToSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// Well-known stdlib overrides — must precede the kind switch.
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == rawMessageType {
		// Any JSON value is acceptable. Field-level description (set by
		// the caller) is what actually pins the expected shape.
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Slice, reflect.Array:
		return map[string]any{
			"type":  "array",
			"items": b.typeToSchema(t.Elem()),
		}
	case reflect.Map:
		// JSON objects only allow string keys. Maps keyed on other types
		// would not round-trip, so we emit a neutral object schema and
		// drop additionalProperties to avoid claiming a shape we can't
		// validate.
		if t.Key().Kind() != reflect.String {
			return map[string]any{"type": "object"}
		}
		return map[string]any{
			"type":                 "object",
			"additionalProperties": b.typeToSchema(t.Elem()),
		}
	case reflect.Struct:
		return b.structOrRef(t)
	case reflect.Interface:
		// Open interface accepts any JSON value.
		return map[string]any{}
	default:
		return map[string]any{"type": "string"}
	}
}

// structOrRef inlines the struct schema on the first visit to a non-recursive
// type, and switches to $ref/$defs the moment the type is seen mid-construction
// (self-referential or mutually recursive case). This keeps the common case
// flat while making any recursive shape representable.
func (b *schemaBuilder) structOrRef(t reflect.Type) map[string]any {
	name := b.defName(t)
	if name == "" {
		// Anonymous struct types can't recurse onto themselves — inline.
		return b.structSchema(t)
	}
	if b.inProgress[t] {
		// Mid-construction encounter — emit a $ref. The outer call will
		// finalise the entry in $defs once it completes.
		if _, ok := b.defs[name]; !ok {
			b.defs[name] = nil // reserve the slot
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	}
	if existing, ok := b.defs[name]; ok && existing != nil {
		// Already finalised once and referenced again — reuse via $ref.
		return map[string]any{"$ref": "#/$defs/" + name}
	}

	b.inProgress[t] = true
	schema := b.structSchema(t)
	delete(b.inProgress, t)

	if _, refed := b.defs[name]; refed {
		// Some descendant emitted a $ref to us during construction, so we
		// must publish the full definition. Parent receives the $ref.
		b.defs[name] = schema
		return map[string]any{"$ref": "#/$defs/" + name}
	}
	// Non-recursive — inline the definition. Matches the pre-recursion
	// behavior of this generator.
	return schema
}

// jsonTagOptions captures the parsed pieces of a json:"name,opt1,opt2" tag
// so callers don't have to keep re-parsing the raw string.
type jsonTagOptions struct {
	// nameExplicit reports whether the json tag set a name part (even an
	// empty one, as in `json:",omitempty"`). Anonymous embeds with such tags
	// must NOT be promoted — encoding/json treats them as named fields.
	nameExplicit bool
	// skip is `json:"-"` exactly (no options after it). `json:"-,"` is the
	// escape hatch encoding/json supports for keeping a literal "-" name.
	skip bool
	// omitempty is set when ",omitempty" appears in the options.
	omitempty bool
}

// parseJSONTag returns the effective field name plus the parsed options.
// fallback is used when the tag is absent or its name part is empty.
func parseJSONTag(tag, fallback string) (string, jsonTagOptions) {
	if tag == "" {
		return fallback, jsonTagOptions{}
	}
	parts := strings.Split(tag, ",")
	opts := jsonTagOptions{}
	name := parts[0]
	if name == "-" && len(parts) == 1 {
		opts.skip = true
		return name, opts
	}
	if name != "" || len(parts) > 1 {
		opts.nameExplicit = true
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			opts.omitempty = true
		}
	}
	if name == "" {
		name = fallback
	}
	return name, opts
}

// isFieldRequired honors `jsonschema:"required"` first, then falls back to
// "not omitempty" via the parsed tag options. A previous string-contains
// implementation would mis-classify any field whose JSON name contained the
// substring "omitempty".
func isFieldRequired(tags reflect.StructTag, jsonOpts jsonTagOptions) bool {
	for _, p := range splitTag(tags.Get("jsonschema")) {
		if strings.TrimSpace(p) == "required" {
			return true
		}
	}
	return !jsonOpts.omitempty
}

// jsonschemaKeys is the recognised set of attribute names in jsonschema struct
// tags. splitTag uses it to decide which commas terminate an attribute and
// which belong to the attribute's value (descriptions full of JSON examples
// routinely contain commas; splitting blindly truncated them at the first one).
var jsonschemaKeys = []string{
	"required",
	"description=",
	"enum=",
	"minimum=",
	"maximum=",
	"default=",
}

// splitTag breaks a jsonschema tag into its attributes. It only consumes a
// comma as a separator when the next non-space token is a known key, so
// values like `description=Use [{"a":1, "b":2}]` survive intact.
func splitTag(tag string) []string {
	var parts []string
	current := strings.Builder{}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if c == ',' {
			rest := strings.TrimLeft(tag[i+1:], " ")
			isKey := false
			for _, k := range jsonschemaKeys {
				if strings.HasPrefix(rest, k) {
					// "required" is a bare key; require the next char to be a
					// comma, end of tag, or anything non-alphanumeric so a
					// description like `required, just kidding` is NOT cut.
					if k == "required" {
						after := len(k)
						if after >= len(rest) || rest[after] == ',' {
							isKey = true
						}
					} else {
						isKey = true
					}
					break
				}
			}
			if isKey {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteByte(c)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// applyTagAnnotations layers jsonschema tag attributes (description, enum,
// minimum, maximum, default) on top of an existing property schema. No-op
// when the tag is empty.
func applyTagAnnotations(prop map[string]any, tag string) {
	if tag == "" {
		return
	}
	for _, part := range splitTag(tag) {
		part = strings.TrimSpace(part)
		switch {
		case part == "" || part == "required":
			// required is handled at the struct level
		case strings.HasPrefix(part, "description="):
			prop["description"] = strings.TrimPrefix(part, "description=")
		case strings.HasPrefix(part, "enum="):
			enumStr := strings.TrimPrefix(part, "enum=")
			prop["enum"] = interfaceSlice(strings.Split(enumStr, "|"))
		case strings.HasPrefix(part, "minimum="):
			if v := parseNumber(strings.TrimPrefix(part, "minimum=")); v != nil {
				prop["minimum"] = v
			}
		case strings.HasPrefix(part, "maximum="):
			if v := parseNumber(strings.TrimPrefix(part, "maximum=")); v != nil {
				prop["maximum"] = v
			}
		case strings.HasPrefix(part, "default="):
			prop["default"] = strings.TrimPrefix(part, "default=")
		}
	}
}

func interfaceSlice(vals []string) []any {
	result := make([]any, len(vals))
	for i, v := range vals {
		result[i] = v
	}
	return result
}

func parseNumber(s string) any {
	var i int64
	if n, err := fmt.Sscanf(s, "%d", &i); n == 1 && err == nil {
		return i
	}
	var f float64
	if n, err := fmt.Sscanf(s, "%f", &f); n == 1 && err == nil {
		return f
	}
	return nil
}

// newToolFromAny creates a Tool from untyped parameters using reflection
// to match the schema type with the function signature. Used by the
// reflection-based registry helpers; returns a structured error for callers
// that load tools dynamically (MCP, plugins) instead of panicking.
func newToolFromAny(schema any, fn any, cfg ToolConfig) (*Tool, error) {
	fnType := reflect.TypeOf(fn)
	if fnType == nil || fnType.Kind() != reflect.Func {
		return nil, fmt.Errorf("tool %s: fn must be a function, got %T", cfg.Name, fn)
	}

	schemaType := reflect.TypeOf(schema)
	if schemaType == nil {
		return nil, fmt.Errorf("tool %s: schema must be a concrete type, got nil", cfg.Name)
	}

	// Build the execution wrapper via reflection. json.Unmarshal already
	// decodes nested struct shapes recursively, so the same exec handles
	// arbitrary depth (matching the recursive schemas generated above).
	exec := func(ctx context.Context, args json.RawMessage) (string, error) {
		inputPtr := reflect.New(schemaType)
		if err := json.Unmarshal(args, inputPtr.Interface()); err != nil {
			return "", fmt.Errorf("tool %s: unmarshal args: %w", cfg.Name, err)
		}
		fnVal := reflect.ValueOf(fn)
		results := fnVal.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			inputPtr.Elem(),
		})
		var result string
		var err error
		if len(results) > 0 {
			result = results[0].String()
		}
		if len(results) > 1 && !results[1].IsNil() {
			err = results[1].Interface().(error)
		}
		return result, err
	}

	rawSchema, err := GenerateSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: generate schema: %w", cfg.Name, err)
	}
	compiled, err := compileSchema(cfg.Name, rawSchema)
	if err != nil {
		return nil, err
	}

	return &Tool{
		config:         cfg,
		schema:         rawSchema,
		compiledSchema: compiled,
		execute:        exec,
	}, nil
}

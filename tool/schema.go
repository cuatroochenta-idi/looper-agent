package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

// rawMessageType lets the Mapper recognise json.RawMessage and emit a
// permissive schema for it (any JSON value). invopop's default treats
// json.RawMessage as a string because []byte implements MarshalJSON — that
// would mislead the LLM into stringifying its payload.
var rawMessageType = reflect.TypeFor[json.RawMessage]()

// anyType lets the Mapper recognise interface{} fields and emit `{}` so the
// model can pass any JSON value through. Without the mapper invopop would
// emit nothing useful for an empty interface.
var anyType = reflect.TypeFor[any]()

// reflector is the shared invopop/jsonschema configuration used by every
// tool. The settings combine strict-tool-call ergonomics (no extra
// properties, no schema id) with sensible defaults for the LLM consumers:
//
//   - Anonymous:                 suppresses the `$id` URL invopop would
//                                otherwise embed; provider validators want
//                                a pure schema document.
//   - AllowAdditionalProperties: kept false so OpenAI's strict tool mode
//                                and Anthropic's tool_use validator
//                                accept the result without rewriting.
//   - ExpandedStruct:            the input type is inlined at the root, so
//                                consumers that decode the top-level `type`
//                                and `properties` keep working. Nested
//                                named types still use $defs/$ref so
//                                self-referential shapes (e.g. recursive
//                                section trees) are representable.
//   - Mapper:                    overrides for json.RawMessage and
//                                interface{} → permissive `{}` so field
//                                descriptions describe the actual shape.
// anyValueSchema is the marshaled form returned by the Mapper for
// json.RawMessage and interface{} fields. invopop serializes a literal
// `&jsonschema.Schema{}` as the bare JSON Schema boolean `true` (spec-valid
// but rejected by some strict provider validators); the non-nil empty Extras
// map breaks invopop's "zero schema" shortcut so the field marshals to the
// safer `{}` instead.
func anyValueSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Extras: map[string]any{}}
}

// mapper is the shared mapping override used by every Reflector instance —
// json.RawMessage and interface{} fields both resolve to the canonical
// "any JSON value" schema (`{}`).
func mapper(t reflect.Type) *jsonschema.Schema {
	if t == rawMessageType || t == anyType {
		return anyValueSchema()
	}
	return nil
}

// reflector handles input types that have a Go type name. ExpandedStruct=true
// inlines the root struct so consumers can decode the top-level `type` /
// `properties` keys directly while nested named types still resolve through
// $defs/$ref for proper recursion support.
var reflector = &jsonschema.Reflector{
	Anonymous:                 true,
	AllowAdditionalProperties: false,
	ExpandedStruct:            true,
	Mapper:                    mapper,
}

// anonReflector handles anonymous struct inputs (e.g. `struct{}{}` markers
// used by tools that take no parameters). invopop's ExpandedStruct path
// panics on anonymous types because it keys its Definitions map by
// Type.Name() — empty for anonymous types — so we use the non-expanded
// reflector for them: the root is already serialised inline when the type
// has no name to register in Definitions.
var anonReflector = &jsonschema.Reflector{
	Anonymous:                 true,
	AllowAdditionalProperties: false,
	ExpandedStruct:            false,
	Mapper:                    mapper,
}

// GenerateSchema generates a JSON Schema from a Go struct value.
//
// Supported tags:
//   - `json:"name,omitempty"` — controls field name and required-ness
//     (omitempty/omitzero opt out of required).
//   - `jsonschema:"required,enum=a|b,minimum=N,maximum=N,default=..."`
//     — schema attributes. Note that commas separate attributes, so
//     descriptions containing commas should use the dedicated
//     `jsonschema_description:"..."` tag instead of the description=
//     attribute.
//   - `jsonschema_description:"..."` — free-form description tag.
//     Anything (including commas) survives intact.
//
// Self-referential and mutually-recursive struct types resolve via $defs +
// $ref automatically so the generator does not blow the stack on shapes
// like `type Node struct{ Children []Node }`.
//
// json.RawMessage and interface{} fields are described by their
// jsonschema_description tag only — the schema imposes no type, so the LLM
// can pass arbitrarily nested JSON through them.
func GenerateSchema(v any) (json.RawMessage, error) {
	r := reflector
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t != nil && t.Kind() == reflect.Struct && t.Name() == "" {
		r = anonReflector
	}
	schema := r.Reflect(v)
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("generate schema: %w", err)
	}
	return out, nil
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
	// decodes nested struct shapes recursively, so this exec handles any
	// depth — matching the recursive schemas generated above.
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

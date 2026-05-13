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
// of walking it as a struct (which would expose unexported wall/ext/loc fields
// to the LLM as garbage properties).
var timeType = reflect.TypeFor[time.Time]()

// GenerateSchema generates a JSON Schema from a Go struct value.
// It reads jsonschema struct tags to produce descriptions, enums,
// required fields, minimum/maximum constraints, default values, etc.
//
// Supported jsonschema tags:
//   - description: field description
//   - enum: comma-separated allowed values
//   - minimum, maximum: numeric constraints
//   - required: marks field as required (parent struct collects these)
//   - default: default value
func GenerateSchema(v any) (json.RawMessage, error) {
	schema := generateStructSchema(reflect.TypeOf(v))
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("generate schema: %w", err)
	}
	return b, nil
}

// generateStructSchema builds a JSON Schema object from a Go struct type.
// Emits "additionalProperties": false on every object schema so OpenAI strict
// mode and Anthropic tool_use validators accept the result without rewriting.
func generateStructSchema(t reflect.Type) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}

	props := schema["properties"].(map[string]any)
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)

		name := jsonFieldName(f)
		if name == "-" {
			continue
		}

		prop := generatePropertySchema(f)

		// Collect required fields
		if isFieldRequired(f) {
			required = append(required, name)
		}

		props[name] = prop
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

// jsonFieldName extracts the JSON field name from struct tags.
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		return f.Name
	}
	return parts[0]
}

// isFieldRequired checks if a field has `jsonschema:"required"` or if
// the JSON tag doesn't include `omitempty`.
func isFieldRequired(f reflect.StructField) bool {
	jsTag := f.Tag.Get("jsonschema")
	if strings.Contains(jsTag, "required") {
		return true
	}
	jsonTag := f.Tag.Get("json")
	return !strings.Contains(jsonTag, "omitempty")
}

// generatePropertySchema builds a JSON Schema for a single struct field.
func generatePropertySchema(f reflect.StructField) map[string]any {
	prop := typeToSchema(f.Type)

	// Read jsonschema tag
	tag := f.Tag.Get("jsonschema")
	parseJSTag(prop, tag)

	// Read description from jsonschema tag
	if desc := f.Tag.Get("jsonschema_description"); desc != "" {
		prop["description"] = desc
	}

	return prop
}

// typeToSchema produces a JSON-Schema fragment for any Go type. For slices /
// arrays it includes an "items" sub-schema describing the element type — OpenAI
// (and other strict validators) reject `{"type":"array"}` without it.
//
// Well-known stdlib types (time.Time, json.RawMessage) are intercepted before
// the kind switch so we don't walk their unexported fields as struct properties.
func typeToSchema(t reflect.Type) map[string]any {
	// Unwrap pointers.
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// Well-known type overrides — must happen before the kind switch.
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
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
			"items": typeToSchema(t.Elem()),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": typeToSchema(t.Elem()),
		}
	case reflect.Struct:
		return generateStructSchema(t)
	default:
		return map[string]any{"type": "string"}
	}
}


// parseJSTag parses jsonschema tag attributes into the property schema.
func parseJSTag(prop map[string]any, tag string) {
	if tag == "" {
		return
	}

	parts := strings.Split(tag, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch {
		case part == "required":
			// Handled at struct level
		case strings.HasPrefix(part, "description="):
			prop["description"] = strings.TrimPrefix(part, "description=")
		case strings.HasPrefix(part, "enum="):
			enumStr := strings.TrimPrefix(part, "enum=")
			enumVals := strings.Split(enumStr, "|")
			prop["enum"] = interfaceSlice(enumVals)
		case strings.HasPrefix(part, "minimum="):
			val := parseNumber(strings.TrimPrefix(part, "minimum="))
			if val != nil {
				prop["minimum"] = val
			}
		case strings.HasPrefix(part, "maximum="):
			val := parseNumber(strings.TrimPrefix(part, "maximum="))
			if val != nil {
				prop["maximum"] = val
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

	// Build the execution wrapper via reflection
	exec := func(ctx context.Context, args json.RawMessage) (string, error) {
		// Create a new instance of the schema type
		inputPtr := reflect.New(schemaType)
		if err := json.Unmarshal(args, inputPtr.Interface()); err != nil {
			return "", fmt.Errorf("tool %s: unmarshal args: %w", cfg.Name, err)
		}

		// Call fn(ctx, input)
		fnVal := reflect.ValueOf(fn)
		results := fnVal.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			inputPtr.Elem(),
		})

		// Extract (string, error) from results
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

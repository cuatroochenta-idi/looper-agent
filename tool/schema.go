package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

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
func generateStructSchema(t reflect.Type) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
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
	prop := map[string]any{}

	// Determine JSON Schema type from Go type
	prop["type"] = goTypeToJSONType(f.Type)

	// Read jsonschema tag
	tag := f.Tag.Get("jsonschema")
	parseJSTag(prop, tag)

	// Read description from jsonschema tag
	if desc := f.Tag.Get("jsonschema_description"); desc != "" {
		prop["description"] = desc
	}

	return prop
}

// goTypeToJSONType maps Go types to JSON Schema types.
func goTypeToJSONType(t reflect.Type) string {
	// Handle pointers
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		return "string"
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
// to match the schema type with the function signature.
func newToolFromAny(schema any, fn any, cfg ToolConfig) *Tool {
	fnType := reflect.TypeOf(fn)
	if fnType.Kind() != reflect.Func {
		panic(fmt.Sprintf("tool %s: fn must be a function, got %T", cfg.Name, fn))
	}

	schemaType := reflect.TypeOf(schema)

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
		panic(fmt.Sprintf("tool %s: failed to generate schema: %v", cfg.Name, err))
	}

	return &Tool{
		config:  cfg,
		schema:  rawSchema,
		execute: exec,
	}
}

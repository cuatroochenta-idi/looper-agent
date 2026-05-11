package tool

import (
	"encoding/json"
	"fmt"
)

// Validate checks tool input arguments against the tool's JSON schema.
// Returns nil if valid, or an error describing the validation failure.
// Validation errors are typed so the agent can interpret them as
// feedback for self-correction rather than fatal exceptions.
func Validate(t *Tool, args json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("cannot validate nil tool")
	}

	// Parse the schema for validation
	var schema map[string]any
	if err := json.Unmarshal(t.schema, &schema); err != nil {
		return fmt.Errorf("invalid tool schema: %w", err)
	}

	// Parse the arguments
	var input any
	if err := json.Unmarshal(args, &input); err != nil {
		return &ValidationError{
			ToolName: t.config.Name,
			Message:  fmt.Sprintf("invalid JSON: %v", err),
		}
	}

	// Validate against schema
	if err := validateAgainstSchema(schema, input, ""); err != nil {
		return &ValidationError{
			ToolName: t.config.Name,
			Message:  err.Error(),
		}
	}

	return nil
}

// ValidationError is a typed error returned when tool input validation fails.
// The agent interprets this as feedback for self-correction, not a crash.
type ValidationError struct {
	ToolName string
	Message  string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error for tool %q: %s", e.ToolName, e.Message)
}

// validateAgainstSchema performs basic JSON Schema validation.
// For full spec compliance, integrate with a dedicated JSON Schema library.
func validateAgainstSchema(schema map[string]any, input any, path string) error {
	schemaType, _ := schema["type"].(string)
	if schemaType == "" {
		return nil // no type constraint
	}

	switch schemaType {
	case "object":
		return validateObject(schema, input, path)
	case "array":
		return validateArray(schema, input, path)
	case "string":
		return validateString(schema, input, path)
	case "number":
		return validateNumber(schema, input, path)
	case "boolean":
		return validateBoolean(input, path)
	}
	return nil
}

func validateObject(schema map[string]any, input any, path string) error {
	obj, ok := input.(map[string]any)
	if !ok {
		return fmt.Errorf("%sexpected object, got %T", path, input)
	}

	// Extract required fields (JSON unmarshals arrays as []interface{})
	var required []string
	if reqRaw, ok := schema["required"]; ok {
		switch req := reqRaw.(type) {
		case []interface{}:
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		case []string:
			required = req
		}
	}

	props, _ := schema["properties"].(map[string]any)

	// Check required fields
	for _, req := range required {
		if _, exists := obj[req]; !exists {
			return fmt.Errorf("%smissing required field %q", pathPrefix(path), req)
		}
	}

	// Validate each property
	for key, propSchema := range props {
		if val, exists := obj[key]; exists {
			propPath := pathPrefix(path) + key
			if ps, ok := propSchema.(map[string]any); ok {
				if err := validateAgainstSchema(ps, val, propPath); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func validateArray(schema map[string]any, input any, path string) error {
	arr, ok := input.([]any)
	if !ok {
		return fmt.Errorf("%sexpected array, got %T", path, input)
	}
	// Items validation can be added for full spec compliance
	_ = arr
	return nil
}

func validateString(schema map[string]any, input any, path string) error {
	_, ok := input.(string)
	if !ok {
		return fmt.Errorf("%sexpected string, got %T", path, input)
	}

	// Enum validation (JSON unmarshals arrays as []interface{})
	if enum, exists := schema["enum"]; exists {
		s := input.(string)
		found := false
		switch enumList := enum.(type) {
		case []interface{}:
			for _, ev := range enumList {
				if es, ok := ev.(string); ok && es == s {
					found = true
					break
				}
			}
		case []string:
			for _, es := range enumList {
				if es == s {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("%svalue %q not in allowed values", path, s)
		}
	}

	return nil
}

func validateNumber(schema map[string]any, input any, path string) error {
	var val float64
	switch v := input.(type) {
	case float64:
		val = v
	case int:
		val = float64(v)
	case int64:
		val = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return fmt.Errorf("%sinvalid number: %v", path, v)
		}
		val = f
	default:
		return fmt.Errorf("%sexpected number, got %T", path, input)
	}

	if min, ok := schema["minimum"].(float64); ok && val < min {
		return fmt.Errorf("%svalue %v is less than minimum %v", path, val, min)
	}
	if max, ok := schema["maximum"].(float64); ok && val > max {
		return fmt.Errorf("%svalue %v is greater than maximum %v", path, val, max)
	}

	return nil
}

func validateBoolean(input any, path string) error {
	_, ok := input.(bool)
	if !ok {
		return fmt.Errorf("%sexpected boolean, got %T", path, input)
	}
	return nil
}

func pathPrefix(path string) string {
	if path == "" {
		return ""
	}
	return path + "."
}

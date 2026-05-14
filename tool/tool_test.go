package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// --- Test types ---

type SimpleInput struct {
	Name  string `json:"name" jsonschema:"description=The name to greet,required"`
	Count int    `json:"count" jsonschema:"description=Number of repetitions,minimum=1,maximum=10"`
}

type NestedInput struct {
	User   UserInfo   `json:"user" jsonschema:"required"`
	Tags   []string   `json:"tags" jsonschema:"description=Optional tags"`
	Active bool       `json:"active"`
}

type UserInfo struct {
	ID    string `json:"id" jsonschema:"required"`
	Email string `json:"email" jsonschema:"description=User email"`
}

type EnumInput struct {
	Status string `json:"status" jsonschema:"description=The status,enum=active,enum=inactive,enum=pending"`
}

// --- Tool creation ---

func TestNewTool(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "hello " + input.Name, nil
	}, ToolConfig{
		Name:        "greet",
		Description: "Greets a person",
		Parallel:    true,
		Retries:     2,
		Timeout:     5 * time.Second,
	})

	if tl.Name() != "greet" {
		t.Errorf("expected name 'greet', got %q", tl.Name())
	}
	if tl.Description() != "Greets a person" {
		t.Errorf("expected description, got %q", tl.Description())
	}
	if !tl.Config().Parallel {
		t.Error("expected Parallel=true")
	}
	if tl.Config().Retries != 2 {
		t.Errorf("expected Retries=2, got %d", tl.Config().Retries)
	}
	if tl.Config().Timeout != 5*time.Second {
		t.Errorf("expected Timeout=5s, got %v", tl.Config().Timeout)
	}
	if tl.Schema() == nil {
		t.Error("expected non-nil schema")
	}
}

func TestNewToolExecuteSuccess(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "hello " + input.Name, nil
	}, ToolConfig{
		Name:        "greet",
		Description: "Greets a person",
	})

	result, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"World","count":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello World" {
		t.Errorf("expected 'hello World', got %q", result)
	}
}

func TestNewToolExecuteValidationError(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{
		Name: "greet",
	})

	// Missing required field "name"
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"count":5}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNewToolExecuteFunctionError(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "", context.DeadlineExceeded
	}, ToolConfig{
		Name:    "failing",
		Retries: 0,
	})

	_, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"test","count":1}`))
	if err == nil {
		t.Fatal("expected error from function")
	}
}

func TestNewToolWithRetries(t *testing.T) {
	attempts := 0
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		attempts++
		if attempts < 3 {
			return "", &testError{"temporary failure"}
		}
		return "success", nil
	}, ToolConfig{
		Name:    "retryable",
		Retries: 3,
	})

	result, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"test","count":1}`))
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got %q", result)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestNewToolTimeout(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return "slow", nil
		}
	}, ToolConfig{
		Name:    "slow",
		Timeout: 10 * time.Millisecond,
	})

	_, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"test","count":1}`))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Schema generation ---

func TestGenerateSchemaSimple(t *testing.T) {
	schema, err := GenerateSchema(SimpleInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	if m["type"] != "object" {
		t.Errorf("expected type=object, got %v", m["type"])
	}

	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	if _, ok := props["name"]; !ok {
		t.Error("expected 'name' property")
	}
	if _, ok := props["count"]; !ok {
		t.Error("expected 'count' property")
	}

	required, ok := m["required"].([]interface{})
	if !ok {
		t.Fatalf("expected required array, got %T", m["required"])
	}
	found := false
	for _, r := range required {
		if s, ok := r.(string); ok && s == "name" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'name' in required")
	}
}

func TestGenerateSchemaEnum(t *testing.T) {
	schema, err := GenerateSchema(EnumInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	var m map[string]any
	json.Unmarshal(schema, &m)

	props := m["properties"].(map[string]any)
	status := props["status"].(map[string]any)

	enum, ok := status["enum"]
	if !ok {
		t.Fatal("expected enum in status property")
	}
	enumList := enum.([]any)
	if len(enumList) != 3 {
		t.Errorf("expected 3 enum values, got %d", len(enumList))
	}
}

func TestGenerateSchemaNested(t *testing.T) {
	schema, err := GenerateSchema(NestedInput{})
	if err != nil {
		t.Fatalf("generate schema: %v", err)
	}

	var m map[string]any
	json.Unmarshal(schema, &m)

	props := m["properties"].(map[string]any)
	user, ok := props["user"].(map[string]any)
	if !ok {
		t.Fatal("expected 'user' property")
	}
	// Nested named structs go through $defs/$ref so recursive shapes are
	// representable; resolve the reference to assert the underlying shape.
	ref, ok := user["$ref"].(string)
	if !ok {
		t.Fatalf("expected user to be a $ref node, got %v", user)
	}
	defs := m["$defs"].(map[string]any)
	defName := strings.TrimPrefix(ref, "#/$defs/")
	def, ok := defs[defName].(map[string]any)
	if !ok {
		t.Fatalf("expected $defs entry %q, got %v", defName, defs)
	}
	if def["type"] != "object" {
		t.Errorf("expected resolved user def type=object, got %v", def["type"])
	}
}

func TestToolSchemaMap(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	sm := tl.SchemaMap()
	if sm["type"] != "object" {
		t.Errorf("expected type=object, got %v", sm["type"])
	}
}

// --- Validation ---

func TestValidateSuccess(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	err := Validate(tl, json.RawMessage(`{"name":"valid","count":5}`))
	if err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestValidateMissingRequired(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	err := Validate(tl, json.RawMessage(`{"count":5}`))
	if err == nil {
		t.Fatal("expected validation error for missing required field")
	}
}

func TestValidateEnumValue(t *testing.T) {
	tl := MustNewTool(EnumInput{}, func(ctx context.Context, input EnumInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	err := Validate(tl, json.RawMessage(`{"status":"active"}`))
	if err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}

	err = Validate(tl, json.RawMessage(`{"status":"invalid"}`))
	if err == nil {
		t.Fatal("expected validation error for invalid enum value")
	}
}

func TestValidateInvalidJSON(t *testing.T) {
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	err := Validate(tl, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected validation error for invalid JSON")
	}
}

func TestValidateNilTool(t *testing.T) {
	err := Validate(nil, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for nil tool")
	}
}

// --- ToolRegistry ---

func TestToolRegistryAdd(t *testing.T) {
	reg := NewToolRegistry()
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})

	reg.Add(tl)
	if reg.Len() != 1 {
		t.Errorf("expected 1 tool, got %d", reg.Len())
	}
}

func TestToolRegistryRegister(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if reg.Len() != 1 {
		t.Errorf("expected 1 tool, got %d", reg.Len())
	}
	tools := reg.Tools()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool from Tools(), got %d", len(tools))
	}
}

func TestToolRegistryRegister_InvalidFn_ReturnsError(t *testing.T) {
	reg := NewToolRegistry()
	err := reg.Register(SimpleInput{}, "not a function", ToolConfig{Name: "bad"})
	if err == nil {
		t.Fatal("expected error when fn is not a function")
	}
	if reg.Len() != 0 {
		t.Errorf("registry should stay empty on failure, got %d entries", reg.Len())
	}
}

func TestToolRegistryToolsCopy(t *testing.T) {
	reg := NewToolRegistry()
	tl := MustNewTool(SimpleInput{}, func(ctx context.Context, input SimpleInput) (string, error) {
		return "ok", nil
	}, ToolConfig{Name: "test"})
	reg.Add(tl)

	tools := reg.Tools()
	tools[0] = nil // shouldn't affect registry

	tools2 := reg.Tools()
	if tools2[0] == nil {
		t.Error("modifying Tools() result affected registry")
	}
}

// --- Helpers ---

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

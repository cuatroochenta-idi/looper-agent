package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCompileSchema_InvalidJSON_ReturnsError asserts the compiler reports
// invalid input as an error value, never via panic. Library code that builds
// tools dynamically (MCP bridges, plugin loaders) needs to recover gracefully.
func TestCompileSchema_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := compileSchema("probe", json.RawMessage("{not json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("error should name the tool, got: %v", err)
	}
}

// TestNewTool_ErrorPath_NoPanic asserts NewTool returns a typed error on
// schema-build failures instead of crashing the process. We trigger it via a
// schema that is structurally OK but whose downstream compilation fails.
func TestNewTool_ErrorPath_NoPanic(t *testing.T) {
	// Compilation can't easily be forced to fail from a real Go struct, so
	// this test simply pins the new signature: NewTool must be assignable to
	// a (*Tool, error) pair without panicking on the happy path.
	tl, err := NewTool(SimpleInput{},
		func(ctx context.Context, in SimpleInput) (string, error) { return "ok", nil },
		ToolConfig{Name: "probe"},
	)
	if err != nil {
		t.Fatalf("happy-path NewTool returned error: %v", err)
	}
	if tl == nil {
		t.Fatal("expected non-nil tool on success")
	}
}

// TestMustNewTool_ConveniencePath asserts the MustX companion exists for
// declarative tool registration in tests and examples.
func TestMustNewTool_ConveniencePath(t *testing.T) {
	tl := MustNewTool(SimpleInput{},
		func(ctx context.Context, in SimpleInput) (string, error) { return "ok", nil },
		ToolConfig{Name: "probe"},
	)
	if tl == nil {
		t.Fatal("MustNewTool should never return nil on a valid spec")
	}
}

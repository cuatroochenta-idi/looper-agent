// Package tool provides the tool definition, configuration, and execution
// system for the Looper Agent framework.
//
// Tools are defined functionally: a Go struct for the input schema, a function
// for the execution logic, and a ToolConfig for metadata. The framework
// generates JSON Schema from the struct, validates inputs at runtime, and
// handles execution (sequential vs parallel) transparently.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ToolConfig groups all configuration for a tool.
type ToolConfig struct {
	// Name is the unique identifier the LLM uses to call this tool.
	Name string

	// Description tells the LLM what this tool does and when to use it.
	Description string

	// Retries is the number of automatic retries if the tool fails.
	Retries int

	// Parallel indicates whether this tool can execute concurrently
	// with other tools in the same turn.
	Parallel bool

	// Timeout is the per-execution timeout. Zero means no timeout.
	Timeout time.Duration
}

// Tool represents a registered tool with its schema, configuration, and
// execution function. Created via NewTool and used internally by AgentLoop.
type Tool struct {
	config ToolConfig
	schema json.RawMessage

	// compiledSchema is the parsed JSON Schema used by Validate. Built once
	// at Tool construction so the validation hot path is allocation-light.
	compiledSchema *jsonschema.Schema

	// execute is the internal execution function that receives parsed
	// JSON arguments and returns a string result.
	execute func(ctx context.Context, args json.RawMessage) (string, error)

	// preExecute optionally runs before execute; populated via
	// WithPreExecute. Returns a *RejectionError to surface corrective
	// feedback to the LLM, or any other error to fail the call.
	preExecute func(ctx context.Context, args json.RawMessage) error
}

// NewTool creates a tool from an input schema struct, an execution function,
// and configuration. The schema struct should use jsonschema tags for
// descriptions, enums, validation rules, etc.
//
// Optional behaviour (pre-execution validation, etc.) is supplied via
// variadic ToolOption values built with WithPreExecute and similar helpers.
//
// Returns an error if schema generation or compilation fails. Use MustNewTool
// when constructing tools declaratively in tests or examples where a
// build-time failure should be a hard stop.
func NewTool[I any](schema I, fn func(ctx context.Context, input I) (string, error), cfg ToolConfig, opts ...ToolOption) (*Tool, error) {
	rawSchema, err := GenerateSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: generate schema: %w", cfg.Name, err)
	}
	compiled, err := compileSchema(cfg.Name, rawSchema)
	if err != nil {
		return nil, err
	}

	// Wrap the typed function into the internal execute signature.
	exec := func(ctx context.Context, args json.RawMessage) (string, error) {
		var input I
		if err := json.Unmarshal(args, &input); err != nil {
			return "", fmt.Errorf("tool %s: unmarshal args: %w", cfg.Name, err)
		}
		return fn(ctx, input)
	}

	to := &toolOptions{}
	for _, opt := range opts {
		opt(to)
	}

	return &Tool{
		config:         cfg,
		schema:         rawSchema,
		compiledSchema: compiled,
		execute:        exec,
		preExecute:     to.preExecute,
	}, nil
}

// MustNewTool wraps NewTool and panics on error. Use in declarative tool
// registration where a malformed input schema is a programmer error, not a
// runtime condition — typically test fixtures and example code.
func MustNewTool[I any](schema I, fn func(ctx context.Context, input I) (string, error), cfg ToolConfig, opts ...ToolOption) *Tool {
	tl, err := NewTool(schema, fn, cfg, opts...)
	if err != nil {
		panic(err)
	}
	return tl
}

// NewToolFromRawSchema builds a *Tool from a pre-existing JSON Schema —
// used when the schema comes from an external source (MCP servers,
// plugins) instead of being generated from a Go type.
//
// The exec function receives the JSON arguments verbatim; callers are
// expected to do their own unmarshalling. Validation against the schema
// still runs through the framework's compiled-schema gate.
func NewToolFromRawSchema(
	name, description string,
	schema json.RawMessage,
	exec func(ctx context.Context, args json.RawMessage) (string, error),
) (*Tool, error) {
	compiled, err := compileSchema(name, schema)
	if err != nil {
		return nil, err
	}
	return &Tool{
		config:         ToolConfig{Name: name, Description: description},
		schema:         schema,
		compiledSchema: compiled,
		execute:        exec,
	}, nil
}

// Name returns the tool's name.
func (t *Tool) Name() string { return t.config.Name }

// Config returns the tool's configuration.
func (t *Tool) Config() ToolConfig { return t.config }

// Schema returns the tool's JSON Schema as raw JSON.
func (t *Tool) Schema() json.RawMessage { return t.schema }

// Description returns the tool's description.
func (t *Tool) Description() string { return t.config.Description }

// SchemaMap returns the tool's JSON Schema as a map, suitable for
// passing directly to provider SDKs (e.g., OpenAI's FunctionParameters).
func (t *Tool) SchemaMap() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(t.schema, &m); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return m
}

// Execute runs the tool with the given JSON arguments.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.config.Timeout)
		defer cancel()
	}

	// Validate args before execution
	if err := Validate(t, args); err != nil {
		return "", fmt.Errorf("tool %s validation failed: %w", t.config.Name, err)
	}

	// PreExecute hook: business validation declared at tool-construction
	// time. Rejections route corrective feedback to the LLM without
	// burning a retry attempt; other errors fail the call immediately.
	if t.preExecute != nil {
		if err := t.preExecute(ctx, args); err != nil {
			return "", err
		}
	}

	// Execute with retries
	var lastErr error
	maxAttempts := t.config.Retries + 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := t.execute(ctx, args)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("tool %s: all %d attempts failed: %w", t.config.Name, maxAttempts, lastErr)
}

package tool

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks tool input arguments against the tool's compiled JSON
// Schema. Returns nil if valid, or a *ValidationError describing the failure
// in a form the agent can feed back to the LLM for self-correction.
//
// The schema is compiled once at Tool construction (NewTool) so Validate is
// cheap on the hot path — only JSON parsing + a tree walk happens per call.
func Validate(t *Tool, args json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("cannot validate nil tool")
	}

	// Parse arguments preserving number precision so int/float ranges aren't
	// silently truncated before validation runs.
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return &ValidationError{
			ToolName: t.config.Name,
			Message:  fmt.Sprintf("invalid JSON: %v", err),
		}
	}

	if t.compiledSchema == nil {
		// Defensive: a tool built without going through NewTool (e.g. tests
		// constructing &Tool{} directly) won't have a compiled schema.
		return nil
	}

	if err := t.compiledSchema.Validate(parsed); err != nil {
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

// CompileSchema is the exported, re-usable form of compileSchema. The
// agent package uses it to compile structured-output schemas at agent
// construction so output validation has the same machinery as tool-input
// validation. Internal callers still use the lowercase compileSchema for
// consistency.
func CompileSchema(label string, raw json.RawMessage) (*jsonschema.Schema, error) {
	return compileSchema(label, raw)
}

// compileSchema builds a *jsonschema.Schema from a raw schema document. It is
// called once per Tool from NewTool / newToolFromAny so per-call validation
// stays cheap. Errors are returned to the caller — library users who load
// schemas dynamically (MCP, plugins) need to recover instead of crash.
func compileSchema(toolName string, raw json.RawMessage) (*jsonschema.Schema, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("tool %s: invalid schema JSON: %w", toolName, err)
	}
	c := jsonschema.NewCompiler()
	const loc = "mem://tool-schema.json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, fmt.Errorf("tool %s: add schema resource: %w", toolName, err)
	}
	sch, err := c.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("tool %s: compile schema: %w", toolName, err)
	}
	return sch, nil
}

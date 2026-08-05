package anthropic

import "github.com/anthropics/anthropic-sdk-go"

// buildInputSchema converts a tool's full JSON Schema into Anthropic's
// ToolInputSchemaParam.
//
// The distinction that matters: ToolInputSchemaParam IS the schema object,
// not a wrapper around one. Its Properties field is the schema's
// `properties` map, and Required is its `required` list. Assigning the
// whole schema to Properties nests it one level too deep and produces:
//
//	"input_schema": {"type":"object","properties":{
//	    "type":"object","properties":{"sku":{…}},"required":["sku"]}}
//
// The model then sees a tool whose parameters are named `type`,
// `properties`, `required` and `$schema`, with the real ones buried and no
// top-level `required` at all. It cannot call the tool correctly — and for
// the framework-injected final_response tool that means an unusable
// structured-output close.
//
// Keys with no home on the struct (`additionalProperties`, `$defs`, and
// anything else a schema generator emits) are forwarded through
// ExtraFields so constraints survive the trip. `$schema` and `type` are
// dropped: the former is metadata Anthropic rejects, the latter is a
// constant on the struct that always marshals as "object".
func buildInputSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	out := anthropic.ToolInputSchemaParam{
		// A tool with no parameters still needs an object here; a nil
		// Properties would be omitted and Anthropic rejects that.
		Properties: map[string]any{},
	}

	for k, v := range schema {
		switch k {
		case "properties":
			if v != nil {
				out.Properties = v
			}
		case "required":
			out.Required = toStringSlice(v)
		case "$schema", "type":
			// Metadata / constant — see the doc comment.
		default:
			if out.ExtraFields == nil {
				out.ExtraFields = map[string]any{}
			}
			out.ExtraFields[k] = v
		}
	}

	return out
}

// toStringSlice normalises a JSON-decoded `required` list. It arrives as
// []any from encoding/json, but a caller building the map by hand may
// already have []string.
func toStringSlice(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

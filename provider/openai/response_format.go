package openai

import (
	"encoding/json"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// SupportsResponseFormat advertises this provider's native structured-
// output support. The agent loop reads it via the
// provider.ResponseFormatCapable interface check and skips the
// final_response tool injection when true.
func (p *Provider) SupportsResponseFormat() bool { return true }

// buildResponseFormatParams maps a JSON Schema (as raw bytes) into the
// OpenAI ChatCompletion response_format union, honoring the caller's
// requested mode.
//
// Modes:
//   - "" / "json_schema": json_schema with strict=false (historical default).
//     We don't enable strict because our internal schema generator emits
//     properties like minimum/maximum/format that OpenAI rejects in strict.
//   - "json_object": OpenAI's looser json_object — model is told to emit
//     JSON, schema body is NOT sent. For providers like DeepSeek and Qwen
//     that reject json_schema with "response_format type unavailable".
//     Caller is expected to embed the schema in the system prompt and
//     validate client-side.
//   - "none": no response_format wrapper even when schema is provided.
//
// name defaults to "result" when empty (OpenAI requires a non-empty name
// for json_schema mode).
func buildResponseFormatParams(schema []byte, name string, mode provider.ResponseFormatMode) (*openai.ChatCompletionNewParamsResponseFormatUnion, error) {
	if mode == provider.ResponseFormatNone {
		return nil, nil
	}
	if mode == provider.ResponseFormatJSONObject {
		return &openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		}, nil
	}
	// Auto / JSONSchema → schema body required.
	if len(schema) == 0 {
		return nil, nil
	}
	var raw any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return nil, err
	}
	if name == "" {
		name = "result"
	}
	return &openai.ChatCompletionNewParamsResponseFormatUnion{
		OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name:   name,
				Schema: raw,
			},
		},
	}, nil
}

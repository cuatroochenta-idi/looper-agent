package openai

import (
	"encoding/json"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// SupportsResponseFormat advertises this provider's native structured-
// output support. The agent loop reads it via the
// provider.ResponseFormatCapable interface check and skips the
// final_response tool injection when true.
func (p *Provider) SupportsResponseFormat() bool { return true }

// buildResponseFormatParams maps a JSON Schema (as raw bytes) into the
// OpenAI ChatCompletion response_format union. We use json_schema mode
// (strict=false) because our internal schema generator emits properties
// like minimum / maximum / format that OpenAI's strict mode rejects.
// json_schema-without-strict still produces structured JSON outputs,
// it just doesn't error on the API side if the model deviates.
//
// name defaults to "result" when empty (OpenAI requires a non-empty name).
func buildResponseFormatParams(schema []byte, name string) (*openai.ChatCompletionNewParamsResponseFormatUnion, error) {
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

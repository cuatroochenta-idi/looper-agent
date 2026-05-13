package google

import (
	"encoding/json"
	"fmt"

	"google.golang.org/genai"
)

// SupportsResponseFormat advertises this provider's native structured-
// output support. The agent loop uses this to skip the final_response
// tool injection when the user requested structured output.
func (p *Provider) SupportsResponseFormat() bool { return true }

// applyResponseSchema mutates the genai GenerateContentConfig in place to
// enable Gemini's native JSON output: sets ResponseMIMEType to
// application/json and converts our internal JSON Schema into a *genai.Schema.
// No-op when schema is empty.
//
// The conversion reuses convertSchema (in google.go) so the same logic
// that maps tool input schemas is shared with response_format here.
func applyResponseSchema(schema []byte, config *genai.GenerateContentConfig) error {
	if len(schema) == 0 || config == nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return fmt.Errorf("response_schema: parse: %w", err)
	}
	config.ResponseMIMEType = "application/json"
	config.ResponseSchema = convertSchema(raw)
	return nil
}

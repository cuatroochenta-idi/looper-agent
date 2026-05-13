package provider

// ResponseFormatCapable is the optional interface a provider implements
// when it can natively constrain the model's output to a JSON schema —
// without the framework having to inject a final_response tool as a
// proxy. Providers that don't implement it fall back to the tool-
// injection path inside the agent loop.
//
// Native paths today:
//   - OpenAI: response_format = {type: json_schema, json_schema: {schema:…}}
//   - Gemini: config.ResponseSchema + ResponseMIMEType = application/json
//
// Anthropic has no first-class structured output endpoint, so its
// Provider does not implement this interface.
type ResponseFormatCapable interface {
	SupportsResponseFormat() bool
}

// SupportsNativeResponseFormat returns true when p implements
// ResponseFormatCapable AND reports support. Centralised so the agent
// loop only does one type assertion in one place.
func SupportsNativeResponseFormat(p LLMProvider) bool {
	c, ok := p.(ResponseFormatCapable)
	return ok && c.SupportsResponseFormat()
}

package provider

// APIKeySuffix returns the last 4 characters of an API key prefixed with
// "****" (e.g. "****a2Fn"). Empty input returns an empty string so callers
// can pass it through unconditionally — keyless providers (LM Studio,
// Ollama, vLLM) get the zero value without a special case. Keys shorter
// than 4 chars also return empty: they are almost certainly placeholders /
// misconfiguration, and surfacing them in traces would leak the entire
// value.
//
// Used by concrete LLMProvider implementations (openai, anthropic, google)
// to stamp the suffix on LLMResponse.APIKeySuffix / StreamChunk.APIKeySuffix
// so the trace UI can show which of several rotating or chained keys
// answered each turn.
func APIKeySuffix(key string) string {
	if len(key) < 4 {
		return ""
	}
	return "****" + key[len(key)-4:]
}

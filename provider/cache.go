package provider

// CacheStrategy controls how the provider handles prompt caching.
type CacheStrategy int

const (
	// CacheAuto lets the provider decide caching based on heuristics
	// (e.g., cache system prompt and tool definitions automatically).
	CacheAuto CacheStrategy = iota

	// CacheAlways forces caching for compatible content.
	CacheAlways

	// CacheDisabled prevents all prompt caching.
	CacheDisabled
)

// CacheConfig configures prompt caching behavior at the provider level.
type CacheConfig struct {
	// Strategy determines the caching behavior.
	Strategy CacheStrategy

	// MinTokens is the minimum prompt size in tokens to activate caching.
	MinTokens int

	// MaxTokens is the maximum tokens to cache (provider-specific limit).
	MaxTokens int
}

// DefaultCacheConfig returns a sensible default cache configuration.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		Strategy:  CacheAuto,
		MinTokens: 1024,
		MaxTokens: 0, // no limit
	}
}

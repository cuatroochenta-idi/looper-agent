package telemetry

import (
	"os"
	"strconv"
)

// OTelConfig holds OpenTelemetry configuration, populated from environment
// variables or code options. When OTEL is not enabled, the no-op provider
// is used (zero overhead).
type OTelConfig struct {
	// Enabled activates OTel export. Default: false (no-op).
	Enabled bool

	// Endpoint is the OTLP gRPC endpoint (e.g., "localhost:4317").
	Endpoint string

	// Insecure disables TLS (for local development).
	Insecure bool

	// Verbose includes full prompt/completion content in spans.
	// Default: false (production-safe).
	Verbose bool
}

// OTelConfigFromEnv reads OTel configuration from environment variables:
//
//	LOOPER_OTEL_ENABLED=true|false
//	LOOPER_OTEL_ENDPOINT=localhost:4317
//	LOOPER_OTEL_INSECURE=true|false
//	LOOPER_OTEL_VERBOSE=true|false
func OTelConfigFromEnv() OTelConfig {
	return OTelConfig{
		Enabled:  boolEnv("LOOPER_OTEL_ENABLED", false),
		Endpoint: stringEnv("LOOPER_OTEL_ENDPOINT", "localhost:4317"),
		Insecure: boolEnv("LOOPER_OTEL_INSECURE", true),
		Verbose:  boolEnv("LOOPER_OTEL_VERBOSE", false),
	}
}

func boolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func stringEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

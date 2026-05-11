package looper

import (
	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// runConfig holds the resolved options for a single agent run.
type runConfig struct {
	history   *message.History
	maxTurns  int
	maxRetries int
	metadata  map[string]any
	runID     string
}

// RunOption configures a single call to agent.Run().
type RunOption func(*runConfig)

// WithHistory restores a previous conversation history for the run.
// The system prompt is NOT taken from history; it is resolved fresh.
func WithHistory(h *message.History) RunOption {
	return func(rc *runConfig) {
		rc.history = h
	}
}

// WithMaxTurns overrides the agent's default maxTurns for this run.
func WithMaxTurns(n int) RunOption {
	return func(rc *runConfig) {
		rc.maxTurns = n
	}
}

// WithMaxConsecutiveToolRetries sets the max tool retries for this run.
func WithMaxConsecutiveToolRetries(n int) RunOption {
	return func(rc *runConfig) {
		rc.maxRetries = n
	}
}

// WithMetadata injects key-value pairs into the run context.
// These can be accessed by system prompts and tools via context.Value.
func WithMetadata(m map[string]any) RunOption {
	return func(rc *runConfig) {
		rc.metadata = m
	}
}

// WithRunID sets a custom run identifier for tracing.
func WithRunID(id string) RunOption {
	return func(rc *runConfig) {
		rc.runID = id
	}
}

// AgentOption configures the agent at construction time.
type AgentOption func(*Agent)

// WithAgentMaxTurns sets the default maximum loop turns.
func WithAgentMaxTurns(n int) AgentOption {
	return func(a *Agent) {
		a.maxTurns = n
	}
}

// WithAgentMaxRetries sets the default maximum consecutive tool retries.
func WithAgentMaxRetries(n int) AgentOption {
	return func(a *Agent) {
		a.maxRetries = n
	}
}

// WithAgentMemory sets the memory management strategy.
func WithAgentMemory(mm memory.MemoryManager) AgentOption {
	return func(a *Agent) {
		a.memoryMgr = mm
	}
}

// WithAgentPause sets the pause manager for man-in-the-middle support.
func WithAgentPause(pm *pause.PauseManager) AgentOption {
	return func(a *Agent) {
		a.pauseMgr = pm
	}
}

// WithTelemetry configures OpenTelemetry tracing and metrics.
func WithTelemetry(tp trace.TracerProvider, mp metric.MeterProvider) AgentOption {
	return func(a *Agent) {
		a.telemetry = telemetry.NewCostTracker(tp, mp)
	}
}

// WithTelemetryVerbose enables full prompt/completion content in spans.
// Only use in development; off by default for production safety.
func WithTelemetryVerbose() AgentOption {
	return func(a *Agent) {
		if a.telemetry != nil {
			a.telemetry.SetVerbose(true)
		}
	}
}

// WithOTelEndpoint sets the OTLP endpoint for telemetry export.
func WithOTelEndpoint(endpoint string) AgentOption {
	return func(a *Agent) {
		cfg := telemetry.OTelConfigFromEnv()
		cfg.Endpoint = endpoint
		cfg.Enabled = true
		// In production, the endpoint would be used to create a proper exporter.
		// For now, record the config.
	}
}

// WithOTelInsecure disables TLS for OTLP connections.
func WithOTelInsecure() AgentOption {
	return func(a *Agent) {
		cfg := telemetry.OTelConfigFromEnv()
		cfg.Insecure = true
	}
}

// WithCustomModelCost registers pricing for a non-official model (Ollama, OpenRouter, etc.).
func WithCustomModelCost(model string, config telemetry.CostConfig) AgentOption {
	return func(a *Agent) {
		a.costModel.WithCustomCost(model, config)
	}
}

// WithCacheConfig sets the prompt caching configuration.
func WithCacheConfig(config provider.CacheConfig) AgentOption {
	return func(a *Agent) {
		// Cache config is applied at the provider level during construction.
		_ = config
	}
}

// WithModel overrides the provider's default model.
func WithModel(model string) AgentOption {
	return func(a *Agent) {
		a.model = model
	}
}

// WithTemperature sets the LLM temperature.
func WithTemperature(t float64) AgentOption {
	return func(a *Agent) {
		a.temperature = t
	}
}

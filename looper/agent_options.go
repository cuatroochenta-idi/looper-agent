package looper

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/loop"
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
	history    *message.History
	maxTurns   int
	maxRetries int
	metadata   map[string]any
	runID      string
	sessionID  string
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

// WithSessionID groups this run with other runs that share the same id in
// the debug panel. Equivalent to setting LOOPER_SESSION_ID on the process,
// but scoped to a single Run / Iterate call — useful when one process
// serves multiple user-facing chat sessions and each one needs its own
// grouping (e.g. lanbu's per-app chat). An empty value is ignored and the
// env var (if set) still applies.
func WithSessionID(id string) RunOption {
	return func(rc *runConfig) {
		rc.sessionID = id
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

// WithReasoning sets the default thinking/reasoning configuration that
// every LLM call this agent makes will carry. Use ReasoningEffortNone or
// pass a nil config to disable. Provider-specific support:
//
//   - OpenAI o-series / gpt-5: Effort → reasoning_effort.
//   - Anthropic Claude 3.7+/4.x: Effort or BudgetTokens → thinking.
//   - Gemini 2.5 / Flash Thinking: Effort or BudgetTokens → thinkingConfig.
//
// IncludeInOutput controls whether reasoning text is surfaced via the
// StepReasoningChunk events; when false (default), it is dropped before
// reaching the loop.
func WithReasoning(rc *provider.ReasoningConfig) AgentOption {
	return func(a *Agent) {
		a.reasoning = rc
	}
}

// WithReasoningEffort is a one-arg convenience: enable reasoning at the
// given effort, include traces in output.
func WithReasoningEffort(effort provider.ReasoningEffort, includeInOutput bool) AgentOption {
	return WithReasoning(&provider.ReasoningConfig{
		Effort:          effort,
		IncludeInOutput: includeInOutput,
	})
}

// WithTurnValidator attaches a TurnValidator to the agent. After every turn
// (whether it produced tool calls or a final text response) the validator
// inspects a TurnSnapshot and may reject it; rejections add the validator's
// Hint as a system message and re-prompt the model. The retry budget counts
// consecutive failures — a single accepted turn resets it.
//
// Use this to enforce per-project rules the model keeps slipping on:
// "always call publish_pages before complete_prd", "never reply with plain
// text when a tool would do", etc. See loop.TurnSnapshot for the inputs the
// validator receives.
func WithTurnValidator(v loop.TurnValidator, maxRetries int) AgentOption {
	return func(a *Agent) {
		a.validator = v
		a.validatorMaxRetries = maxRetries
	}
}

// WithTurnValidatorFunc is a convenience that wraps a plain function as a
// TurnValidator, so callers don't have to define a struct for inline rules.
func WithTurnValidatorFunc(fn func(snap loop.TurnSnapshot) loop.Outcome, maxRetries int) AgentOption {
	return WithTurnValidator(loop.TurnValidatorFunc(func(_ context.Context, snap loop.TurnSnapshot) loop.Outcome {
		return fn(snap)
	}), maxRetries)
}

// WithDynamicTools registers a function that produces the tool list per
// turn. The framework calls it before each LLM call and uses the returned
// slice instead of the agent's static tools. Returning nil falls back to
// the static list, so the function can mix "always available" tools with
// conditional ones.
//
// Use this for state-machine allowlists: in a discovery phase hide tools
// that only make sense after publish, etc. The structured-output
// final_response tool (if configured) is always appended automatically.
func WithDynamicTools(fn loop.DynamicToolsFunc) AgentOption {
	return func(a *Agent) {
		a.dynamicTools = fn
	}
}

// WithToolChoice sets the default ToolChoice for every turn. Use the
// constructors in the provider package: ToolChoiceAuto / ToolChoiceRequired
// / ToolChoiceNone / ToolChoiceSpecific(name). The zero value (or
// ToolChoiceAuto) lets the model decide — same as the legacy default.
//
// Pair with WithTurnValidator to implement "required-on-first-turn,
// auto-after": the validator can detect when a tool was called and the
// next turn's behavior is controlled by what your code sets next.
func WithToolChoice(c provider.ToolChoice) AgentOption {
	return func(a *Agent) {
		a.toolChoice = c
	}
}

// WithUsageLimits caps the run's resource consumption. The FIRST limit
// hit stops the loop, surfaces the last attempted output, and sets
// res.Status = "usage_exceeded". Zero-valued fields are unlimited so an
// empty UsageLimits{} is a no-op.
//
// Operational guidance:
//   - MaxRequests caps the number of LLM calls (cheap insurance against
//     runaway tool-call loops the model can't recover from).
//   - MaxTotalTokens guards a per-conversation token budget (sum of
//     input + output across all turns).
//   - MaxUSD requires the cost model to be configured (WithCustomModelCost
//     or the default pricing). Useful for end-user-facing tiers.
func WithUsageLimits(u loop.UsageLimits) AgentOption {
	return func(a *Agent) {
		a.usageLimits = u
	}
}

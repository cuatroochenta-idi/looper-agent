// Package telemetry provides OpenTelemetry-based observability and
// integrated cost tracking for the Looper Agent framework.
//
// Cost tracking is NOT a separate module but is natively embedded in
// each span of the agentic loop. Every LLM call, tool execution, and
// hook invocation emits spans with cost attributes.
//
// OpenTelemetry is optional; if not configured, the no-op provider is
// used (zero overhead). Configuration can be done via code options or
// environment variables (LOOPER_OTEL_ENABLED, LOOPER_OTEL_ENDPOINT, etc.).
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// CostTracker manages OpenTelemetry spans and integrated cost tracking
// for agent runs. It creates spans at each level of the agentic loop
// and records cost attributes on them.
type CostTracker struct {
	tracer    trace.Tracer
	meter     metric.Meter
	costModel *CostModel
	verbose   bool
}

// NewCostTracker creates a new cost tracker with the given OTel providers.
// Pass nil for either to use the global no-op providers.
func NewCostTracker(tp trace.TracerProvider, mp metric.MeterProvider) *CostTracker {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	return &CostTracker{
		tracer:    tp.Tracer("looper-agent"),
		meter:     mp.Meter("looper-agent"),
		costModel: NewCostModel(),
	}
}

// Tracer returns the OpenTelemetry tracer.
func (ct *CostTracker) Tracer() trace.Tracer { return ct.tracer }

// SetVerbose enables or disables verbose mode (prompt/completion content in spans).
func (ct *CostTracker) SetVerbose(v bool) { ct.verbose = v }

// CostModel returns the cost model registry.
func (ct *CostTracker) CostModel() *CostModel { return ct.costModel }

// StartAgentRun begins a new trace for an agent execution.
func (ct *CostTracker) StartAgentRun(ctx context.Context, agentID, runID string) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "agent.run",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	span.SetAttributes(
		attribute.String("looper.agent.id", agentID),
		attribute.String("looper.agent.run_id", runID),
	)
	return ctx, span
}

// StartTurn begins a span for a single loop turn.
func (ct *CostTracker) StartTurn(ctx context.Context, turn, maxTurns int) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "agent.loop.turn")
	span.SetAttributes(
		attribute.Int("looper.agent.turn", turn),
		attribute.Int("looper.agent.max_turns", maxTurns),
	)
	return ctx, span
}

// StartLLMCall begins a span for an LLM API call.
func (ct *CostTracker) StartLLMCall(ctx context.Context, provider, model string, stream bool) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "llm.call")
	span.SetAttributes(
		attribute.String("looper.llm.provider", provider),
		attribute.String("looper.llm.model", model),
		attribute.Bool("looper.llm.stream", stream),
	)
	return ctx, span
}

// StartToolCall begins a span for a tool execution.
func (ct *CostTracker) StartToolCall(ctx context.Context, toolName string, parallel bool) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "tool.call")
	span.SetAttributes(
		attribute.String("looper.tool.name", toolName),
		attribute.Bool("looper.tool.parallel", parallel),
	)
	return ctx, span
}

// StartToolValidate begins a span for tool input validation.
func (ct *CostTracker) StartToolValidate(ctx context.Context, toolName string) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "tool.validate")
	span.SetAttributes(
		attribute.String("looper.tool.name", toolName),
	)
	return ctx, span
}

// StartHook begins a span for hook execution.
func (ct *CostTracker) StartHook(ctx context.Context, hookName, phase string) (context.Context, trace.Span) {
	ctx, span := ct.tracer.Start(ctx, "hook.execute")
	span.SetAttributes(
		attribute.String("looper.hook.name", hookName),
		attribute.String("looper.hook.phase", phase),
	)
	return ctx, span
}

// RecordCost sets cost attributes on a span.
func (ct *CostTracker) RecordCost(span trace.Span, cost CostBreakdown) {
	span.SetAttributes(
		attribute.Float64("looper.cost.total_usd", cost.TotalUSD),
		attribute.Float64("looper.cost.input_usd", cost.InputUSD),
		attribute.Float64("looper.cost.output_usd", cost.OutputUSD),
		attribute.Float64("looper.cost.cached_usd", cost.CachedUSD),
		attribute.Float64("looper.cost.cache_write_usd", cost.CacheWriteUSD),
		attribute.Float64("looper.cost.savings_usd", cost.SavingsUSD),
		attribute.Bool("looper.cost.estimated", cost.Estimated),
	)
}

// RecordUsage sets token usage attributes on a span.
func (ct *CostTracker) RecordUsage(span trace.Span, usage Usage) {
	span.SetAttributes(
		attribute.Int("looper.tokens.prompt", usage.InputTokens),
		attribute.Int("looper.tokens.completion", usage.OutputTokens),
		attribute.Int("looper.tokens.cached", usage.CachedTokens),
		attribute.Int("looper.tokens.cache_write", usage.CacheWriteTokens),
		attribute.Int("looper.tokens.total", usage.InputTokens+usage.OutputTokens),
	)
}

// RecordCacheHit sets cache hit attributes on a span.
func (ct *CostTracker) RecordCacheHit(span trace.Span, cachedTokens int, savingsUSD float64) {
	span.SetAttributes(
		attribute.Bool("looper.llm.cache.hit", true),
		attribute.Int("looper.llm.cache.cached_tokens", cachedTokens),
		attribute.Float64("looper.llm.cache.savings_usd", savingsUSD),
	)
}

// Usage mirrors provider.Usage in the telemetry package. Same normalisation
// contract: InputTokens is the inclusive prompt total; CachedTokens (reads)
// and CacheWriteTokens (writes) are subsets of it.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	CacheWriteTokens int

	// Cost is the USD cost reported by the upstream API for this usage, when
	// the provider returns it (e.g. OpenRouter's usage.cost). Zero means the
	// API reported no cost and CostModel.Calculate estimates from its pricing
	// tables (custom overrides first, then the built-in matrix).
	Cost float64
}

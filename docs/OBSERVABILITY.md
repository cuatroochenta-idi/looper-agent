# Observability & operations

How to see what your agent is doing in production: cost, latency, traces,
debugging.

## 1. Run-level result

Every `agent.Run` returns a `*RunResult` that includes everything you'd
log to your APM / billing system:

```go
type RunResult struct {
    Output  string
    History *message.History
    Cost    CostBreakdown
    Usage   Usage
    Turns   int
    Status  string
}

type CostBreakdown struct {
    TotalUSD     float64
    InputUSD     float64
    OutputUSD    float64
    CachedUSD    float64
    SavingsUSD   float64    // (fresh_cost - actual_cost) — how much caching saved you
    InputTokens  int
    OutputTokens int
    CachedTokens int
}
```

**`res.Status`** is the single source of truth for run outcome:

| Status | Meaning |
|--------|---------|
| `"completed"` | Final response returned. |
| `"max_turns_exceeded"` | Loop hit `WithMaxTurns(n)` without a final response. |
| `"validation_exhausted"` | `TurnValidator` rejected all turns up to its budget. |
| `"output_validation_exhausted"` | Structured-output validator + retry budget exhausted. |
| `"usage_exceeded"` | `WithUsageLimits` cap hit (requests / tokens / USD). |
| `"cancelled"` | `context.Cancel` or `Iterator.Close` while running. |
| `"error"` | Underlying provider error not classified as cancellation. |

Log all six fields per request. They're enough to build a dashboard:
cost-per-tenant, p95 turns, p95 latency (wrap `Run` with `time.Now`),
status histogram.

## 2. Step-level traces (live UI / debugging)

For per-turn introspection, use `Iterate` instead of `Run`:

```go
iter := agent.Iterate(ctx, "...")
for step := range iter.Next() {
    switch step.Type {
    case loop.StepLLMCall:
        log.Printf("turn %d: → LLM", step.Turn)
    case loop.StepStreamingChunk:
        fmt.Print(step.Content)
    case loop.StepReasoningChunk:
        // model's hidden thinking (only when WithIncludeReasoning(true))
    case loop.StepToolCall:
        log.Printf("turn %d: → %s %s", step.Turn, step.ToolName, step.ToolArgs)
    case loop.StepToolResult:
        log.Printf("turn %d: ← %s", step.Turn, step.Content)
    case loop.StepFinalResponse:
        log.Printf("turn %d: final %d chars", step.Turn, len(step.Content))
    case loop.StepError:
        log.Printf("turn %d: ERROR %v", step.Turn, step.Error)
    }
}
res := iter.Result()
```

Every `Run` call internally drains an `Iterator` — there's no overhead
to using `Iterate` directly when you want the granularity.

## 3. OpenTelemetry

```go
import (
    "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/sdk/trace"
)

tp := trace.NewTracerProvider(/*...*/)
mp := metric.NewMeterProvider(/*...*/)

agent := looper.MustNewAgent(p, sysPrompt,
    looper.WithTelemetry(tp, mp),
)
```

Spans emitted per run:
- `looper-agent.run` (root) with attributes `model`, `provider`, `run_id`.
- One child span per LLM call.
- One child per tool execution.

Counters / histograms:
- `looper.tokens.input` / `output` / `cached`
- `looper.cost.usd` (total per run)
- `looper.tool.duration` (per tool)
- `looper.turn.duration` (per turn)
- `looper.run.duration` (per run)

For free-form prompt + completion content in spans (dev only — these can
get big and contain user PII):

```go
looper.WithTelemetryVerbose()
```

Don't ship verbose mode to production unless you've sanitised the inputs.

## 4. HTTP trace endpoint (live web UI)

The framework's `cmd/looper serve` panel reads from a custom HTTP trace
endpoint instead of OTel. Point the agent at your own endpoint:

```bash
LOOPER_TRACE_ENDPOINT=http://localhost:9090/api/trace \
LOOPER_SESSION_ID=user-42-2026-05-13 \
go run your-agent
```

Inside the framework, `tracer.go` opens a per-`Iterate` queue and posts:

```jsonc
// per run start
{"type": "run_start", "run_id": "...", "data": {"input": "...", "system_prompt": "...", "model": "...", "provider": "..."}}
// per step
{"type": "step", "run_id": "...", "data": {"type": "tool_call", "turn": 2, "tool_name": "search", ...}}
// per run end
{"type": "run_end", "run_id": "...", "data": {"output": "...", "status": "completed", "turns": 3, "total_usd": 0.012, ...}}
```

The TUI under `cmd/looper serve` consumes this and renders a live
SSE / htmx dashboard at `http://localhost:9090`.

## 5. Cost model

Default pricing for the three first-party providers lives in
`telemetry/modelcosts.go`. Override for a custom model:

```go
looper.WithCustomModelCost("ollama/llama3", telemetry.CostConfig{
    InputPerMTokens:  0,    // self-hosted
    OutputPerMTokens: 0,
})
```

For pure-token tracking without USD figures, just don't set
`WithCustomModelCost`; `res.Cost.TotalUSD` will be zero but token counts
still populate.

## 6. Concurrency-safe logging

The framework logs to `log.Default()` (stdlib). For structured logging:

```go
// At process start, replace the default logger.
log.SetOutput(myStructuredWriter)
log.SetFlags(0)
```

Hooks are a clean place to do per-turn structured logging without
touching framework internals:

```go
agent.On("AfterCall", func(ctx context.Context, p *loop.CallParams) error {
    slog.InfoContext(ctx, "turn",
        "turn", p.Turn,
        "history_len", p.History.Len(),
    )
    return nil
})
```

All hooks run on the same goroutine as the loop, so order is
deterministic. The `*CallParams.History` is the same pointer the loop
uses — don't mutate it from a hook unless you mean to.

## 7. Debugging a stuck run

Three quick checks:

```
1. Is res.Status one of "*_exhausted" or "max_turns_exceeded"?
   → A validator or budget is rejecting. Inspect res.History for the
     last MessageSystem entries — those are the framework's hints.

2. Is the loop spinning on the same tool?
   → Add OnBeforeToolExecution with a per-(name,args) counter (see
     RECIPES.md §3) to confirm. If it's a real loop, you need a
     TurnValidator OR a better system prompt OR ToolChoice constraints.

3. Empty output from Gemini?
   → Thinking-capable Gemini models burn output budget on hidden
     reasoning. Either raise MaxTokens or set WithThinkingBudget(0).
     See provider/google/google.go::WithMaxTokens godoc.
```

For real-time inspection, set `LOOPER_TRACE_ENDPOINT` and watch the
panel at `cmd/looper serve` — every step the agent emits shows up
live.

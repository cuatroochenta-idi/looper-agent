# Looper Agent (Nautilus)

A minimalist, production-oriented LLM agent framework for Go. Functional-first
API, three first-party providers (OpenAI, Anthropic, Google), and the
operational primitives you actually need in production: typed structured
output with retry, validators, concurrent-session-safe state, native prompt
caching, circuit breakers, MCP integration.

```go
agent := looper.MustNewAgent(
    openai.NewProvider(os.Getenv("OPENAI_API_KEY")),
    "You are a precise assistant.",
)
res, _ := agent.Run(ctx, "What's the capital of France?")
fmt.Println(res.Output, res.Cost.TotalUSD)
```

---

## Table of contents

1. [Install & quick start](#install--quick-start)
2. [Core concepts](#core-concepts)
3. [Providers](#providers)
4. [Tools](#tools)
5. [Structured output with auto-validation](#structured-output-with-auto-validation)
6. [Lifecycle hooks](#lifecycle-hooks)
7. [Turn validator (corrective re-prompt)](#turn-validator-corrective-re-prompt)
8. [Tool choice, dynamic tools, BeforeToolExecution](#tool-choice-dynamic-tools-beforetoolexecution)
9. [Memory](#memory)
10. [Pause / resume](#pause--resume)
11. [Multi-modal](#multi-modal)
12. [Production primitives](#production-primitives)
13. [MCP integration](#mcp-integration)
14. [Telemetry & cost tracking](#telemetry--cost-tracking)
15. [Concurrent sessions](#concurrent-sessions)
16. [Examples](#examples)
17. [Testing](#testing)

---

## Install & quick start

```bash
go get github.com/cuatroochenta-idi/looper-agent@latest
```

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/cuatroochenta-idi/looper-agent/looper"
    "github.com/cuatroochenta-idi/looper-agent/provider/openai"
    "github.com/cuatroochenta-idi/looper-agent/tool"
)

type SearchIn struct {
    Query string `json:"query" jsonschema:"description=What to search for,required"`
}

func main() {
    ctx := context.Background()

    search := tool.MustNewTool(SearchIn{},
        func(ctx context.Context, in SearchIn) (string, error) {
            return "Mock results for: " + in.Query, nil
        },
        tool.ToolConfig{
            Name:        "search",
            Description: "Search the web for a query.",
        },
    )

    agent := looper.MustNewAgent(
        openai.NewProvider(os.Getenv("OPENAI_API_KEY")),
        "You are a research assistant.",
        search,
    )

    res, err := agent.Run(ctx, "What's new in Go generics?")
    if err != nil {
        panic(err)
    }
    fmt.Printf("%s\n(turns=%d, cost=$%.6f)\n", res.Output, res.Turns, res.Cost.TotalUSD)
}
```

---

## Core concepts

| Type | Purpose |
|------|---------|
| `looper.Agent` | The runnable agent — combines provider, system prompt, tools, options. |
| `provider.LLMProvider` | Interface every LLM implements. First-party: `openai`, `anthropic`, `google`. |
| `*tool.Tool` | A single callable tool. Built from a Go struct (input schema) + a function. |
| `skill.Skill` | A group of tools plus a prompt fragment. |
| `toolkit.Toolkit` | A group of tools that share internal state (DB handle, rate limiter, …). |
| `*message.History` | The conversation log. Thread-safe, JSON-serializable. |
| `loop.AgentLoop` | The internal iterative engine. Most users won't touch it directly. |
| `loop.Iterator` | Step-by-step control over a run (used internally by `Agent.Run`). |

Construction is variadic: `NewAgent(provider, systemPrompt, components...)` accepts
`*tool.Tool`, `skill.Skill`, `toolkit.Toolkit`, and any `AgentOption` in any order.

**Two construction styles**:
- `NewAgent(...)` returns `(*Agent, error)` — recommended for libraries / runtime configs.
- `MustNewAgent(...)` returns `*Agent` and panics on misconfiguration — use in tests / declarative `main()`s.

Same pattern for `tool.NewTool` / `tool.MustNewTool`.

---

## Providers

Three first-party implementations:

```go
import (
    "github.com/cuatroochenta-idi/looper-agent/provider/openai"
    "github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
    "github.com/cuatroochenta-idi/looper-agent/provider/google"
)

openai.NewProvider(apiKey, openai.WithModel("gpt-4o-mini"))
anthropic.NewProvider(apiKey, anthropic.WithModel("claude-sonnet-4-..."))
google.NewProvider(apiKey, google.WithModel("gemini-flash-latest"))
```

Common per-provider options: `WithModel`, `WithMaxTokens`, `WithTemperature`.

**Provider-specific options**:

| Option | Provider | Effect |
|--------|----------|--------|
| `WithCacheBreakpoints("system", "tools")` | anthropic | Real `cache_control` markers on the last system block + last tool definition. Hit rate visible via `res.Cost.CachedTokens`. |
| `WithThinkingBudget(n)` / `WithIncludeThoughts(b)` | anthropic, google | Extended-thinking config. Gemini caveat: budget covers both hidden reasoning AND visible tokens — use `WithThinkingBudget(0)` if you're capping output and want all of it to be visible. |
| `WithBaseURL(url)` | openai | Point at LM Studio / Ollama / Azure / any OpenAI-compatible endpoint. |
| `WithIncludeReasoning(b)` | all 3 | Surface reasoning deltas via `StepReasoningChunk`. |

**Cross-provider config** (works on any provider through `LLMRequest`):

```go
looper.WithModel("gpt-4o")              // override the model for this agent
looper.WithTemperature(0.3)
looper.WithReasoning(&provider.ReasoningConfig{
    Effort:          provider.ReasoningEffortMedium,
    IncludeInOutput: false,
})
looper.WithToolChoice(provider.ToolChoiceRequired())
```

---

## Tools

A tool = (Go input struct) + (function body) + (config). The framework
generates JSON Schema from the struct, validates LLM-supplied arguments
against it on every call (via `santhosh-tekuri/jsonschema/v6`), and surfaces
validation errors back to the model as feedback.

```go
type ReadFileIn struct {
    Path string `json:"path" jsonschema:"description=Absolute path,required"`
    Lines int   `json:"lines,omitempty" jsonschema:"description=Max lines,minimum=1,maximum=2000"`
}

readFile := tool.MustNewTool(ReadFileIn{},
    func(ctx context.Context, in ReadFileIn) (string, error) {
        return readFromDisk(in.Path, in.Lines)
    },
    tool.ToolConfig{
        Name:        "read_file",
        Description: "Read up to N lines of a file from disk.",
        Parallel:    true,        // can run alongside other parallel tools in the same turn
        Retries:     2,           // automatic retries on transient errors
        Timeout:     5 * time.Second,
    },
)
```

### Supported `jsonschema:` struct tags

| Tag | Meaning |
|-----|---------|
| `required` | Field is mandatory. Inferred automatically when `json` tag lacks `omitempty`. |
| `description=...` | Sent to the model. |
| `enum=a\|b\|c` | Restrict to listed values. |
| `minimum=N` / `maximum=N` | Numeric bounds. |

`time.Time` fields are emitted as `{"type":"string","format":"date-time"}`. Every
object schema carries `additionalProperties: false` so OpenAI strict mode and
Anthropic's tool validator accept them as-is.

### Tool-level business validation (`PreExecute`)

Run a typed pre-check before the tool body. Reject cleanly with a hint the
model can react to:

```go
completePRD := tool.MustNewTool(CompleteIn{}, completeBody,
    tool.ToolConfig{Name: "complete_prd", Description: "..."},
    tool.WithPreExecute(func(ctx context.Context, in CompleteIn) error {
        if !state.IsPublished(in.PRDID) {
            return tool.RejectWithHint(
                "publish_pages must run before complete_prd. Call it first.",
            )
        }
        return nil
    }),
)
```

`RejectWithHint(reason)` is the canonical sentinel: the framework surfaces
the reason to the LLM as a `tool_result` error so the next turn can adapt.
Other errors fail the call immediately (no retry).

### Tools from external schemas (MCP, plugins)

When the schema isn't a Go type (e.g. it arrives from an MCP server), use:

```go
tool.NewToolFromRawSchema(name, description, rawJSONSchema,
    func(ctx context.Context, args json.RawMessage) (string, error) { ... },
)
```

---

## Structured output with auto-validation

The agent emits a typed JSON object on its final turn. The framework
generates the schema from `T`, drives the model to fill it, validates the
result, and (optionally) re-prompts when invalid.

```go
type Analysis struct {
    Sentiment string  `json:"sentiment" jsonschema:"enum=positive|negative|neutral,required"`
    Score     float64 `json:"score" jsonschema:"minimum=0,maximum=1"`
}

agent := looper.MustNewAgent(p, "Analyze text precisely.",
    looper.WithStructuredOutput[Analysis](),
    looper.WithOutputRetries(3),                                  // up to 3 re-prompts on invalid JSON
    looper.WithOutputValidator(func(a Analysis) error {           // optional business rule
        if a.Sentiment == "neutral" {
            return looper.ErrOutputInvalid("pick a strong signal — neutral isn't accepted")
        }
        return nil
    }),
)

res, _ := agent.Run(ctx, "I love this product!")
var out Analysis
if err := looper.Decode(res, &out); err != nil {
    log.Fatal(err)
}
```

**How it works (and why it's the right shape)**

| Provider | Path | Notes |
|----------|------|-------|
| OpenAI | `response_format: {type: json_schema, ...}` natively | Non-strict (so `min/max/format` survive). Tool injection is skipped. |
| Gemini | `config.ResponseMIMEType=application/json` + `config.ResponseSchema` | Same. |
| Anthropic | Framework injects a `final_response` tool with the schema | The model "calls" it; the framework treats the call's `output` argument as the run's answer. |

**Validation pipeline** (runs after every candidate, both paths):

1. Schema check against the compiled schema (same engine that validates tool inputs).
2. Optional custom `WithOutputValidator[T](fn)`.
3. On failure → validator message added as a `MessageSystem` hint → re-prompt.
4. Budget exhausted → `res.Status == "output_validation_exhausted"`, last JSON preserved on `res.Output`.

Counter `outputRetriesUsed` lives on the `Iterator`, so concurrent runs on
the same `Agent` don't share it.

---

## Lifecycle hooks

```go
agent.On("BeforeCall", func(ctx context.Context, p *loop.CallParams) error {
    log.Printf("→ turn %d, prompt_len=%d", p.Turn, len(p.SystemPrompt))
    return nil
})

agent.On("AfterCall", func(ctx, p *loop.CallParams) error { ... })
agent.On("OnCancel", func(ctx, p *loop.CallParams) error { ... })
agent.On("BeforeFinalResponse", ...)
agent.On("AfterFinalResponse",  ...)
```

Returning an error aborts the run. Hooks see and may mutate `p.History`.

**BeforeToolExecution** has a different payload (the planned tool calls) and a
separate registration method:

```go
agent.OnBeforeToolExecution(func(ctx context.Context, p *loop.ToolExecutionParams) error {
    for _, c := range p.Calls {
        if looksLikeALoop(c, p.Calls) {
            p.Cancel(c.ID, "you've called this tool with these args 3 times in a row, try something else")
        }
        if needsSanitisation(c) {
            p.Replace(c.ID, sanitise(c))
        }
    }
    return nil
})
```

`Cancel(callID, reason)` skips execution and inserts a synthetic error
`tool_result` so the model gets the feedback. `Replace(callID, newCall)`
swaps the call body — the call ID is preserved so the assistant ↔ tool
bookkeeping stays consistent. Hooks compose: each one sees the cumulative
mutations from earlier hooks via `p.Cancellations()` / `p.Replacements()`.

---

## Turn validator (corrective re-prompt)

A `TurnValidator` inspects every completed turn (final text OR tool calls +
results) and can reject with a `Hint`. On rejection the framework adds the
hint as a `MessageSystem` and re-prompts the model up to the retry budget.
The budget resets after every accepting turn — a validator can recover
after a streak of rejections.

```go
agent := looper.MustNewAgent(p, sysPrompt,
    looper.WithTurnValidatorFunc(func(snap loop.TurnSnapshot) loop.Outcome {
        // Only judge final-text turns; tool-call turns pass through.
        if len(snap.ToolCalls) > 0 {
            return loop.Outcome{OK: true}
        }
        if len(strings.TrimSpace(snap.LastAssistantContent)) < 80 {
            return loop.Outcome{
                OK:     false,
                Reason: "too-short",
                Hint:   "Your reply was too short — give a proper explanation in at least 3 sentences.",
            }
        }
        return loop.Outcome{OK: true}
    }, 2 /* maxRetries */),
)
```

Exhaust the budget → `res.Status == "validation_exhausted"`, last attempt
preserved on `res.Output`.

`TurnValidator` is one level higher than `WithOutputValidator`: it can act
on tool calls + results, not just on the structured output. Use both when
you have process-level invariants AND output-shape invariants.

---

## Tool choice, dynamic tools, BeforeToolExecution

### `WithToolChoice` — force / forbid tool use per turn

```go
looper.WithToolChoice(provider.ToolChoiceAuto())             // default
looper.WithToolChoice(provider.ToolChoiceRequired())         // model MUST call SOME tool
looper.WithToolChoice(provider.ToolChoiceNone())             // no tool calls — text only
looper.WithToolChoice(provider.ToolChoiceSpecific("publish_pages"))
```

The mapping uses each provider's native shape (OpenAI bare strings,
Anthropic `{type:"any"}`, Gemini `FunctionCallingConfig.Mode`).

### `WithDynamicTools` — per-turn allowlist

The function is called before every LLM call with the current `*message.History`.
Return a different tool slice based on state — e.g. a phase machine that hides
`publish_pages` until research is done:

```go
looper.WithDynamicTools(func(ctx context.Context, h *message.History) []*tool.Tool {
    if researchDone(h) {
        return []*tool.Tool{publish, completePRD}
    }
    return []*tool.Tool{search, summarize}
})
```

The `final_response` tool (when structured output is configured) is
always appended automatically — the dynamic function can't accidentally
hide it.

### `OnBeforeToolExecution` — see [Lifecycle hooks](#lifecycle-hooks).

These three primitives compose: the dynamic-tools function decides what's
*available*, `ToolChoice` decides whether the model can / must call one,
and `OnBeforeToolExecution` polices what actually executes.

---

## Memory

Long sessions need to keep token usage bounded. Three strategies:

```go
import "github.com/cuatroochenta-idi/looper-agent/memory"

looper.WithAgentMemory(&memory.SlidingWindow{MaxMessages: 30})

looper.WithAgentMemory(&memory.TokenBudget{
    Budget:     8000,
    Summarizer: memory.NewSummarizer(myLLMSummarizeFunc, memory.WithKeepLast(6)),
})
```

`memory.NewSummarizer(fn, opts...)` takes a user-supplied
`SummarizeFunc(ctx, []Message) (string, error)`. The framework replaces
older messages with a single `MessageSystem` carrying the summary, while
keeping the last `KeepLast` messages verbatim. You decide *how* to
summarize — typically a cheap LLM call:

```go
mem := memory.NewSummarizer(
    func(ctx context.Context, msgs []message.Message) (string, error) {
        // call openai.NewProvider("gpt-4o-mini").Chat(...) here
        return summary, nil
    },
    memory.WithKeepLast(6),
    memory.WithSummaryPrompt("[Summary up to here]"),
)
```

### `History.TruncateByTurns(n)`

If you need raw truncation, `TruncateByTurns` is tool-pair-aware — the cut
point is always a user message, so a `tool_use` is never separated from
its `tool_result` (which would crash an Anthropic call with a 400).

---

## Pause / resume

Inject human-in-the-loop approval gates per tool:

```go
import "github.com/cuatroochenta-idi/looper-agent/pause"

pm := pause.NewPauseManager()
pm.SetPausePoint("send_email", pause.PauseToolConfirm, 5*time.Minute)

agent := looper.MustNewAgent(p, "...",
    sendEmailTool,
    looper.WithAgentPause(pm),
)

// In another goroutine — when a UI surfaces the pending approval and the
// human clicks "ok":
pm.Resume(&pause.PauseResponse{RequestID: callID, Action: "ok"})
```

The `RequestID` is set to the tool call ID by the framework — so concurrent
runs on the same `PauseManager` route each Resume to the right waiter.

---

## Multi-modal

User messages can carry multiple typed parts:

```go
import "github.com/cuatroochenta-idi/looper-agent/message"

hist := message.NewHistory()
hist.AddUserMessageParts(
    message.TextPart("What objects do you see in this image?"),
    message.ImageURLPart("https://example.com/cat.png"),
)
res, _ := agent.Run(ctx, "", looper.WithHistory(hist))
```

Part constructors:
- `TextPart(s)` — plain text. Multiple text parts are concatenated for legacy `Content` field.
- `ImageURLPart(url)` — remote image.
- `ImagePart(mime, data)` — inline bytes (base64 on the wire).
- `FilePart(name, mime, data)` — documents (PDF, CSV).
- `AudioPart(mime, data)` — audio inputs (gpt-4o-audio, Gemini).

Each provider's Translator emits the right native shape — OpenAI content
arrays, Anthropic content blocks, Gemini Parts. Pure-text messages use the
fast path so the wire shape doesn't change for legacy callers.

---

## Production primitives

### Usage / cost limits

```go
looper.WithUsageLimits(loop.UsageLimits{
    MaxRequests:    20,        // hard cap on LLM calls
    MaxTotalTokens: 50_000,    // input + output across the run
    MaxUSD:         0.50,      // requires the cost model to be configured
})
```

The first cap to trip stops the loop with `Status="usage_exceeded"` and
preserves the last output on `res.Output`. Zero-valued fields are unlimited
so the legacy default (no caps) is `UsageLimits{}`.

### Retry + circuit breaker middleware

Wrap any provider:

```go
inner := openai.NewProvider(key)
retryProv := provider.NewRetryProvider(inner, provider.RetryConfig{
    MaxAttempts:             4,
    InitialBackoff:          500 * time.Millisecond,
    MaxBackoff:              30 * time.Second,
    BackoffFactor:           2.0,
    Jitter:                  0.2,
    CircuitFailureThreshold: 5,
    CircuitCooldown:         30 * time.Second,
})
agent := looper.MustNewAgent(retryProv, sysPrompt)
```

The default classifier identifies 5xx, 429, connection-reset, EOF, timeouts
as transient. Override via `Classify func(err error) RetryDecision` for
SDK-specific errors. Returns `provider.ErrCircuitOpen` when the breaker is
tripped — easy to compose with `provider.ProviderQueue` for cross-provider
failover.

The wrapper transparently propagates `SupportsResponseFormat` so it
composes with structured output without losing the native path.

### Typed deps (Pydantic-AI's `RunContext[Deps]` equivalent)

```go
type Deps struct {
    DB     *sql.DB
    UserID string
}

ctx := looper.WithRunDeps(ctx, Deps{DB: db, UserID: "u-42"})
res, _ := agent.Run(ctx, "...")

// Inside a tool body / hook / validator:
deps, ok := looper.Deps[Deps](ctx)
```

Each goroutine carries its own deps via `context.Context` — concurrent
agent runs see only their own values.

---

## MCP integration

Connect to an MCP server, register every remote tool as a native looper tool:

```go
import "github.com/cuatroochenta-idi/looper-agent/mcp"

// Stdio (subprocess) — most common for local MCP servers shipped as binaries:
mcpClient, err := mcp.NewStdioToolProvider(ctx,
    "my-mcp-server", []string{"FOO=bar"}, "--mode=stdio")
if err != nil { ... }
defer mcpClient.Close()

components := make([]any, 0)
for _, t := range mcpClient.Tools() {
    components = append(components, t)
}
agent := looper.MustNewAgent(p, sysPrompt, components...)
```

In-process for tests:

```go
import mcpgo "github.com/mark3labs/mcp-go/client"

c, _ := mcpgo.NewInProcessClient(myServer)
provider, _ := mcp.NewToolProvider(ctx, c)
```

The framework handles `Initialize` + `tools/list` and turns each MCP tool
into a `*tool.Tool` whose `Execute` calls `tools/call`. Tool schemas are
patched to add `additionalProperties:false` so they pass downstream strict
validators.

---

## Telemetry & cost tracking

Every run reports usage + cost on `res`:

```go
res.Cost.TotalUSD
res.Cost.InputUSD / OutputUSD / CachedUSD / SavingsUSD
res.Cost.InputTokens / OutputTokens / CachedTokens
res.Turns
res.Status   // "completed" | "error" | "cancelled" | "validation_exhausted" | "output_validation_exhausted" | "usage_exceeded" | "max_turns_exceeded"
```

For step-level traces and OTel:

```go
looper.WithTelemetry(tracerProvider, meterProvider)
```

Or trace to a custom HTTP endpoint:

```bash
LOOPER_TRACE_ENDPOINT=http://localhost:9090/api/trace LOOPER_SESSION_ID=my-session go run ./...
```

The agent emits `Step` events (`StepLLMCall`, `StepStreamingChunk`,
`StepReasoningChunk`, `StepToolCall`, `StepToolResult`, `StepFinalResponse`,
`StepError`) — drive a live UI by ranging over `agent.Iterate(...)`.

---

## Concurrent sessions

A single `*Agent` instance is safe to run from N goroutines:

```go
agent := looper.MustNewAgent(p, sysPrompt)

for _, user := range users {
    go func(u User) {
        ctx := looper.WithRunDeps(ctx, Deps{UserID: u.ID})
        res, _ := agent.Run(ctx, u.Question)
        // res is isolated per-goroutine — no cross-talk
    }(user)
}
```

Per-run state (validator counter, output-retry counter, history) lives on
the `Iterator`, never on `Agent` or `AgentLoop`. The 50-concurrent stress
test (`concurrency_test.go`) passes under `-race`. `PauseManager` routes
`Resume` calls by `RequestID` (tool call ID) so concurrent approvals don't
cross-contaminate.

**What to avoid**: hooks that close over mutable shared state without
synchronization. The framework can't police what user code does inside a
hook closure — wrap your own state in `sync.Mutex` / atomics.

---

## Examples

The `examples/` folder is a graduated tour:

| # | Folder | Demonstrates |
|---|--------|--------------|
| 01 | `01_basic` | Smallest agent — provider + system prompt + one user input |
| 02 | `02_structured` | `WithStructuredOutput[T]` + `Decode[T]` end-to-end |
| 03 | `03_tools_streaming` | Multiple tools + step-by-step `Iterate` consumption |
| 04 | `04_multi_provider` | Same agent, swappable provider via `LOOPER_PROVIDER=...` |
| 05 | `05_hooks_lifecycle` | Every hook type registered + observed |
| 06 | `06_skill_and_toolkit` | `Skill` (tools + prompt fragment) and `Toolkit` (shared state) |
| 07 | `07_history_resume` | Persist `*message.History` to disk, reload, continue |
| 08 | `08_presentation_builder` | Long, multi-tool flow exercising history growth + parallel calls |
| 09 | `09_pause_resume` | Pause-point gating with approval loop |
| 10 | `10_nested_agents` | Parent agent delegates to child agents via a tool |
| 11 | `11_dev_cli` | Bubbletea TUI driving the framework |
| 12 | `12_multimodal` | Text + image via `NewUserMessageWithParts` |
| 13 | `13_turn_validator` | Validator-driven re-prompt with hint |
| 14 | `14_dynamic_tools` | `WithDynamicTools` phase machine |
| 15 | `15_before_tool_hook` | Loop-detector cancelling repeated tool calls |
| 16 | `16_history_truncate` | `TruncateByTurns` preserving tool pairs |
| 17 | `17_tool_choice` | `ToolChoiceRequired` forcing tool use on a turn |
| 18 | `18_preexecute` | `tool.WithPreExecute` + `RejectWithHint` for business validation |

Run any of them with `go run ./examples/12_multimodal` (after sourcing
`.env.local` with the required API keys).

---

## Testing

### Default suite — `go test ./...`

192 tests, runs in ~3 seconds, hits no network. Cover every primitive (loop,
hooks, validators, structured output, retries, MCP via in-process server,
50-concurrent stress under `-race`).

### E2E suite — gated by build tag `e2e`

```bash
set -a && source .env.local && set +a
go test -tags e2e ./tests/e2e/... -v
```

Hits real APIs (gpt-4o-mini, gemini-flash-latest, claude-...). Tests skip
when a provider's key env var is missing. Covers multi-modal, structured
output across providers, tool calling, validator retry.

See `tests/e2e/README.md` for details.

---

## License & status

Internal framework, status: feature-complete v1. The PRD in
`docs/prds/looper-agent_2026-05-11.md` was the original kickoff;
`docs/CHANGELOG.md` (if you keep one) is the canonical "what landed when".

For deeper architecture notes, see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

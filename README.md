# Looper Agent

A minimalist LLM agent framework for Go, built on each provider's
official SDK. Ships with typed structured output and auto-retry, real
prompt caching, circuit breakers, concurrent-session-safe state, and a
native MCP client.

```go
agent := looper.MustNewAgent(
    openai.NewProvider(os.Getenv("OPENAI_API_KEY")),
    "You are a precise assistant.",
)
res, _ := agent.Run(context.Background(), "What's the capital of France?")
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
17. [Debug CLI (`looper` command)](#debug-cli-looper-command)
18. [Testing](#testing)

---

## Install & quick start

### CLI tool

```bash
go install github.com/cuatroochenta-idi/looper-agent/cmd/looper@latest
```

The `looper` binary lands in `$(go env GOPATH)/bin`. Ensure that
directory is on your `PATH`, then:

```bash
looper version    # confirms the install
looper            # prints usage
```

Upgrade later with the same `go install` command. Pin a release with
`@v0.0.3`. See [Debug CLI](#debug-cli-looper-command) for what each
subcommand does.

### Go library

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
| `skill.Skill` | A group of tools plus a prompt fragment. Embed `skill.Lazy` to load it on demand via `load_skill` — see [Skills](#skills-eager-and-lazy). |
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

## Skills (eager and lazy)

A **skill** groups related tools with a prompt fragment under one API:

```go
type Skill interface {
    Name() string                       // stable id (used by load_skill)
    Title() string                      // short label for the skills index
    Summary() string                    // one-liner: when/why to use it
    RegisterTools(reg *tool.ToolRegistry)
    PromptFragment() string             // the full, detailed instructions
}
```

**Eager skill** — pass any `Skill` to `NewAgent`. Its tools and full
`PromptFragment` are in the system prompt and tool list from the first turn.

**Lazy skill (`load_skill`)** — embed `skill.Lazy` to make the same skill
load-on-demand. Until the model loads it, only its `Title` + `Summary` appear
in a compact `## Skills (load on demand …)` index in the system prompt; its
tools stay hidden and its full `PromptFragment` is withheld. This keeps the
base context small — heavy, situational instructions only enter the window when
the model decides the skill is relevant.

```go
type TranslatorSkill struct {
    skill.Lazy        // ← marker: same Skill API, now loaded on demand
    TargetLang string
}
// Name/Title/Summary/RegisterTools/PromptFragment as for any Skill.

agent := looper.MustNewAgent(provider, systemPrompt,
    CalculatorSkill{},                       // eager
    TranslatorSkill{TargetLang: "Catalan"},  // lazy
)
```

When any lazy skill is present, `NewAgent` auto-injects a native `load_skill`
tool. The model calls `load_skill skill="translator"`; the tool result carries
that skill's full `PromptFragment` plus the list of unlocked tools, and from
that turn on its tools are exposed. Activation is read structurally from the
conversation history (the recorded tool-call, not text), so it survives resume.
A user-supplied [`WithDynamicTools`](#withdynamictools--per-turn-allowlist)
takes precedence over the built-in gating. See `examples/19_lazy_skills`.

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

### Multi-provider chains (failover + key rotation)

Two LLMProvider middlewares for redundancy. Both implement `LLMProvider`
so they slot directly into `looper.NewAgent` — no wrapper-driven dispatch
loop, no caller-side fan-out.

**`FailoverProvider`** — tries inner providers in declared order, switching
on any non-context error. Use it across provider types (OpenAI ↔ Gemini ↔
Anthropic) so the agent stays up when one upstream returns 5xx / 429.

```go
openai := openai.NewProvider(openaiKey)
google := google.NewProvider(googleKey)

chain, _ := provider.NewFailover(
    []provider.LLMProvider{openai, google},
    provider.WithFailoverNames([]string{"openai", "google"}),
)
agent := looper.MustNewAgent(chain, sysPrompt)
```

When every inner fails, the call returns an error satisfying
`errors.Is(err, provider.ErrAllProvidersFailed)` — surface that to end
users as "service unavailable" instead of a raw API error.
`Context.Canceled` / `DeadlineExceeded` from any inner short-circuits the
iteration; we don't fire the next request after the caller has given up.

**`KeyRotationProvider`** — tries inner providers (same SDK, different
API keys) sequentially with a configurable delay between attempts. Use
it inside a single provider type to spread quota across keys or dodge
per-key 429s.

```go
geminiInners := []provider.LLMProvider{
    google.NewProvider(geminiKey1),
    google.NewProvider(geminiKey2),
    google.NewProvider(geminiKey3),
}
geminiPool, _ := provider.NewKeyRotation(
    geminiInners,
    750*time.Millisecond,
    provider.WithKeyRotationLabel("gemini-pool"),
)
```

Compose them naturally — `KeyRotationProvider` inside each slot of a
`FailoverProvider`:

```go
chain, _ := provider.NewFailover(
    []provider.LLMProvider{openaiPool, geminiPool},
    provider.WithFailoverNames([]string{"openai", "gemini"}),
)
```

Streaming follows the same rule as `RetryProvider`: failover only
happens before the stream opens. Once the channel is being drained,
mid-stream errors bubble up — restarting on a different inner would
duplicate already-emitted tokens.

Compared to `ProviderQueue` (the older `Execute(ctx, fn)` primitive):
`FailoverProvider` is what you want when failover should be invisible
to the agent loop. `ProviderQueue` is still the right tool when you
need a caller-driven dispatch (e.g. running the same prompt against
every provider for comparison).

### Per-provider cost & fallback telemetry

When a run is served by several providers (FailoverProvider switch, a
multi-model chain, or any wrapper that mixes `LLMResponse.ProviderID`),
the framework attributes tokens to the right cost-table entry instead
of billing the whole run against a single rate. The `RunResult` carries:

```go
type RunResult struct {
    // ... existing fields (Output, History, Cost, Usage, Turns, Status)

    // Per-(Provider, Model) breakdown in first-seen order.
    Providers []ProviderStats

    // Count of LLM calls that hit the FailoverProvider fallback branch.
    FallbackCalls int
}

type ProviderStats struct {
    Provider      string
    Model         string
    Calls         int
    FallbackCalls int
    Usage         Usage
    Cost          CostBreakdown
}
```

`Cost.TotalUSD` is the sum across entries so the existing field stays
the canonical "what did this run cost?" answer. `Providers` lets you
break it down for telemetry / billing / per-tenant attribution.

Wire shape (when `LOOPER_TRACE_ENDPOINT` is set):

- Every LLM call emits a `step` event of `kind=llm_response` carrying
  `provider`, `model`, `fallback`, and the call's `Usage`. The existing
  `kind=llm_call` event still fires BEFORE the call (it's the
  "thinking…" spinner anchor); `llm_response` fires AFTER and carries
  the provenance.
- `run_end` events grow a `providers` array with the per-entry
  breakdown and a `fallback_calls` counter.

The bundled debug panel (`looper serve`) reads these and renders:

- A `fallback` metric in the run header when `FallbackCalls > 0`.
- A "providers" table under the metrics row when the run used more than
  one (provider, model), or when a single-provider run hit fallback.
- A `↪ fallback` badge next to `llm_call` on every turn served via the
  failover branch, plus a `provider/model` chip on every turn.

All additive — pre-multiprovider traces still render identically.

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

**Cost precision cascade.** The run's cost prefers the API-reported cost per
call (e.g. OpenRouter's `usage.cost`), including for failed/partial calls —
partial usage is still attributed. When no API cost is reported, it falls back
to pricing-table estimation per `(provider, model)`. A custom cost dictionary
(`looper.json` `model_costs`, or `telemetry.CostModel.WithCustomCosts`) overrides
the built-in matrix during that estimation. Tokens bucket into `input`,
`output`, `cached` (cache reads) and `cache_write` (cache writes), each priced
separately. When any contributing call was estimated, `cost_estimated` is true
and the panel renders the figure with a `~` (Estimated marker).

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

### Run identifiers — grouping calls into conversations

Every emitted trace event carries three ids, in widening scope:

| field | source | meaning |
|---|---|---|
| `run_id` | one per `agent.Run` / `Iterate` call | the smallest unit — one full agentic loop from input to final response. Auto-generated UUID, or supplied via `looper.WithRunID(id)`. |
| `parent_run_id` | `ParentRunIDFromContext(ctx)` | set automatically when a tool function spawns a sub-agent — the child reads the parent's id off `ctx`. Empty for top-level runs. |
| `session_id` | `LOOPER_SESSION_ID` env var | groups N independent `agent.Run` calls (or a long-lived chat) into one conversation in the debug panel. |

The dashboard renders these as a tree: each session contains its runs,
sub-agents nest under the tool call that spawned them, and the cost / token
roll-ups aggregate at every level.

```go
// Pin a stable run id when you need to correlate with your own logs.
res, _ := agent.Run(ctx, input, looper.WithRunID("chat-msg-42"))

// Sub-agents inherit parenting automatically — just forward ctx.
tool := tool.MustNewTool(struct{}{},
    func(ctx context.Context, _ struct{}) (string, error) {
        sub, _ := looper.NewAgent(p, "you are a sub-agent")
        r, err := sub.Run(ctx, "do the thing") // r.run_id ⇒ parent_run_id = outer.run_id
        if err != nil {
            return "", err
        }
        return r.Output, nil
    },
    tool.ToolConfig{Name: "delegate"})
```

```bash
# Group every run in this process under one panel "session" card.
export LOOPER_SESSION_ID="$(uuidgen)"
export LOOPER_TRACE_ENDPOINT="http://localhost:9090/api/trace"
go run ./examples/01_basic
go run ./examples/03_tools_streaming   # both runs land in the same session
```

`looper serve -- <cmd>` auto-generates a `LOOPER_SESSION_ID` and forwards it
to the wrapped child, so anything you exec under the panel is already
grouped without manual setup.

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
| 19 | `19_lazy_skills` | Eager `Skill` + lazy `LazySkill` (`skill.Lazy`) loaded via `load_skill` |
| 20 | `20_server_panel` | Deploy the core as a supervision/control-panel server (auth, ingest token, custom costs, folder/postgres persistence) |

Run any of them with `go run ./examples/12_multimodal` (after sourcing
`.env.local` with the required API keys).

---

## Debug CLI (`looper` command)

A small CLI lives in `cmd/looper`. Two install paths depending on whether
you want a shareable tool or a local dev build.

**Install globally as a CLI** (recommended for everyday use):

```bash
go install github.com/cuatroochenta-idi/looper-agent/cmd/looper@latest

looper version          # → looper v0.0.3
looper                  # prints usage
```

The binary lands in `$(go env GOBIN)` — falls back to `$(go env GOPATH)/bin`,
which is typically `~/go/bin`. Make sure that's on your `$PATH`:

```bash
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.zshrc  # or ~/.bashrc
```

Pin to a specific release if you need reproducibility:

```bash
go install github.com/cuatroochenta-idi/looper-agent/cmd/looper@v0.0.3
```

Upgrade later with the same command + `@latest`.

**Build from a local checkout** (recommended when contributing):

```bash
go build -o ./bin/looper ./cmd/looper
./bin/looper version    # → looper (devel a1b2c3d4e5f6)
```

Local builds report `(devel)` with the short commit SHA when Go embedded
VCS info. Installs from a tagged module print the tag verbatim.

Three subcommands. Each one targets a different debugging surface — a
live web UI for running agents, a child-process wrapper that tees
traces into that UI, and an MCP debug server that exposes
framework-level tools to MCP-aware clients (Claude Code, Cursor, Zed,
…).

### `looper serve` — live web UI + trace ingest

```bash
looper serve [--config looper.json] [--port 9090] [--store .looper] [--db <dsn>] [-- <child-cmd> [args...]]
```

| Flag | Default | Effect |
|------|---------|--------|
| `--config` | _(auto)_ | Path to a `looper.json` config file. When unset, `./looper.json` (or `$LOOPER_CONFIG`) is auto-discovered. See [Config file](#config-file). |
| `--port` | `9090` | HTTP port the dashboard binds to. |
| `--store` | `.looper` | Directory where streamed runs are persisted (created if missing — gitignored by default). |
| `--db` | _(none)_ | PostgreSQL DSN for run persistence. Overrides `--store` (folder store) when set; also via `LOOPER_DB`. Schema is Atlas-authored — see `internal/store/postgres/migrations` (`make db-diff` authors a migration). |
| `-- <cmd ...>` | _(none)_ | Anything after `--` is launched as a child process inheriting stdio. Child auto-receives `LOOPER_TRACE_ENDPOINT`, `LOOPER_SESSION_ID`, and `LOOPER_DEBUG=true` so every step it emits streams into the panel. |

The panel is an embedded SolidJS single-page app (Bun+Vite, in `ui/`), bundled
into the binary via `//go:embed` (`internal/web/ui/dist`). Build the real UI
into the binary with `make release` (runs `ui-build` then `build`); iterate on
it with `make ui-dev`, which runs the Vite dev server proxying `/api`, `/ingest`
and `/sse` to the Go server on `:9090`. The built bundle is committed at release
time, so `go install` and plain `go build` ship the real UI with no JS
toolchain; `make ui-build` (Bun) regenerates it after UI changes.

Pattern A — UI only, drive the demo agent from the browser / curl:

```bash
looper serve --port 9090
# open http://localhost:9090
curl -s -X POST localhost:9090/api/run -H 'Content-Type: application/json' --data '{"input":"hello"}'
```

Pattern B — wrap any Go program so its runs flow through the panel:

```bash
looper serve -- go run ./examples/05_hooks_lifecycle
```

The child's first `agent.Run` posts to the panel within ~150 ms of
startup, and the dashboard renders the live step stream. When the
child exits the panel stays alive (Ctrl-C to stop).

**Routes exposed by the server**:

| Route | What you get |
|-------|--------------|
| `GET  /`                        | The embedded SolidJS SPA (dashboard, runs, chats — client-rendered). |
| `GET  /api/state/summary`       | Aggregate totals (runs, cost, tokens, turns). |
| `GET  /api/state/runs` / `/api/state/runs/{id}` | Flat run list (incl. subagents) / per-run detail. |
| `GET  /api/state/chats`         | Conversation summaries. |
| `GET  /api/state/costs`         | Cost breakdown by model. |
| `GET  /api/events?topics=...`   | Single multiplexed JSON SSE stream (topics: `runs`, `chats`, `run:{id}`, `summary`). |
| `POST /api/run`                 | Kick off the built-in demo agent with JSON `{input}` → `{id}`. |
| `POST /ingest`                  | Where the agent's `LOOPER_TRACE_ENDPOINT` posts. This is the contract: any external agent pointing at this URL shows up in the panel. Requires `Authorization: Bearer <ingest_token>` when auth is on. |
| `POST /api/login` / `POST /api/logout` / `GET /api/me` | Auth endpoints (login gate, session cookie, current-user probe). |

The full REST + SSE contract lives in `docs/tasks/2026-07-10_api_contract.md`.

**Demo provider** (used by `POST /api/run`) is picked by env:

```bash
LOOPER_PROVIDER=openai   looper serve   # default, needs OPENAI_API_KEY
LOOPER_PROVIDER=anthropic looper serve  # needs ANTHROPIC_API_KEY
LOOPER_PROVIDER=google   looper serve   # needs GOOGLE_API_KEY or GEMINI_API_KEY
```

#### Config file

`serve` reads an optional `looper.json` (auto-discovered as `./looper.json`, or
via `--config` / `$LOOPER_CONFIG`):

```jsonc
{
  "port": 9090,
  "db": "postgres://user:pass@localhost:5432/looper?sslmode=disable", // empty ⇒ folder store
  "store_dir": ".looper",
  "auth": {
    "username": "admin",
    "password": "secret",           // set to enable the login gate
    "session_secret": "…",          // set to persist sessions across restarts
    "ingest_token": "…"             // bearer token external agents must send
  },
  "model_costs": {                  // override the built-in price matrix
    "anthropic/claude-sonnet-4": { "input": 3e-6, "output": 15e-6, "cached": 0.3e-6, "cache_write": 3.75e-6 }
  }
}
```

Every field has a `LOOPER_*` env override (`LOOPER_PORT`, `LOOPER_DB`,
`LOOPER_STORE_DIR`, `LOOPER_AUTH_USERNAME`, `LOOPER_AUTH_PASSWORD`,
`LOOPER_SESSION_SECRET`, `LOOPER_INGEST_TOKEN`, `LOOPER_CONFIG`). Precedence is
highest-wins: **flags > env > file > defaults** (port `9090`, store_dir
`.looper`). Unknown fields in `looper.json` are a hard error. `model_costs` keys
are `"provider/model"` or a bare model id; values are per-token USD config
(`input`, `output`, `cached`, `cache_write`) — the same thing
`telemetry.CostModel.WithCustomCosts` does programmatically.

#### Auth for production

Setting `auth.password` (in `looper.json` or via `LOOPER_AUTH_PASSWORD`) turns on
a login gate: a `looper_session` HMAC cookie (HttpOnly, SameSite=Lax, 7-day)
guards the panel, and `/ingest` starts requiring `Authorization: Bearer
<ingest_token>`. The effective ingest token is printed/logged at boot so you can
configure external agents by setting `LOOPER_INGEST_TOKEN` in their environment.
Set `auth.session_secret` to persist sessions across restarts (otherwise the key
is ephemeral). With no `auth` block the panel is open — everything is nil-safe.

### `looper mcp` — MCP debug server over stdio

```bash
looper mcp
```

JSON-RPC over stdio, ready to be wired into any MCP-aware client:

```jsonc
// tools/list response
{ "tools": [
  { "name": "looper_run",          "description": "Launch a Looper Agent execution" },
  { "name": "looper_analyze_trace", "description": "Analyze a Looper Agent trace for issues" },
  { "name": "looper_replay",       "description": "Re-run a Looper Agent execution with changes" },
  { "name": "looper_list_history", "description": "List conversation history for a run" }
]}
// resources/list response
{ "resources": [
  { "uri": "looper://runs",  "name": "Recent runs",  "mimeType": "application/json" },
  { "uri": "looper://costs", "name": "Cost summary", "mimeType": "application/json" }
]}
```

Quick smoke test from the shell:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | looper mcp
```

For Claude Code / Cursor / Zed: add `looper mcp` as an MCP server in
your client config — its tools + resources become available alongside
your other MCP integrations.

### `looper run` — wrapped debug runner

> ⚠ Placeholder. The command parses arguments but prints
> `(Debug runner not yet implemented)`. Use `looper serve -- <cmd>`
> instead for the wrap-and-trace flow.

### Environment variables consumed by the CLI

| Var | Default | Effect |
|-----|---------|--------|
| `LOOPER_PROVIDER` | `openai` | Picks the demo provider for `serve`. |
| `LOOPER_OTEL_ENABLED` | `false` | Master switch for OpenTelemetry. |
| `LOOPER_OTEL_ENDPOINT` | `localhost:4317` | OTLP/gRPC endpoint. |
| `LOOPER_OTEL_INSECURE` | `true` | Disable TLS for local collectors. |
| `LOOPER_OTEL_VERBOSE` | `false` | Include full prompt / completion text in spans (dev only). |
| `LOOPER_DEBUG` | `false` | Free-form debug toggle the framework hooks read on startup. |
| `LOOPER_TRACE_ENDPOINT` | _(unset)_ | When set on a child process by `serve`, the framework's tracer posts every Step here. |
| `LOOPER_SESSION_ID` | _(uuid)_ | Groups multiple runs under one session in the panel sidebar. |

### Wiring an external OTel collector

The panel and OTel are independent — you can run both. The framework's
`telemetry` package emits spans for every loop turn, every LLM call,
every tool execution:

```bash
# Local Jaeger all-in-one with OTLP enabled
docker run --rm -p 4317:4317 -p 16686:16686 \
  -e COLLECTOR_OTLP_ENABLED=true \
  jaegertracing/all-in-one:latest

LOOPER_OTEL_ENABLED=true looper serve -- go run ./examples/05_hooks_lifecycle
# Jaeger UI → http://localhost:16686
```

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

## Further reading

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — internal runtime
  layout, design principles, where to extend.
- [`docs/RECIPES.md`](docs/RECIPES.md) — copy-pasteable patterns for
  the most common production scenarios.
- [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) — telemetry, traces,
  cost tracking, debugging tips.

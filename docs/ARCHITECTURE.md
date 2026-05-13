# Architecture

Internal map of Nautilus for contributors and curious users. Follows the
runtime path from `agent.Run` down through providers.

## Top-down: a single Run

```
agent.Run(ctx, input, opts...)
  └─ agent.Iterate(ctx, input, opts...)            // streaming path used by default
       └─ loop.Iterator.run(ctx)                    // goroutine emitting Steps
            ├─ resolveSystemPrompt(ctx)             // base + skill fragments + structured-output instruction (path-aware)
            ├─ for turn := 0; turn < maxTurns; turn++ {
            │    ├─ HookBeforeCall
            │    ├─ memoryMgr.Manage(history)       // optional Summarizer / SlidingWindow / TokenBudget
            │    ├─ build LLMRequest                 // tools (static or dynamic) + ToolChoice + ResponseSchema (if native)
            │    ├─ provider.ChatStream(ctx, req)    // falls back to Chat when streaming returns an error
            │    ├─ drain chunks → fullContent + ToolCalls
            │    ├─ on IsFinal:
            │    │    ├─ structured-output short-circuit → validateStructuredOrAbort → emit / retry / abort
            │    │    ├─ if ToolCalls: applyBeforeToolExecutionHooks → executeToolCallsInternal → step events
            │    │    └─ TurnValidator (post-turn) → emit / retry / abort
            │    └─ UsageLimits check
            ├─ HookAfterCall
            └─ }
```

The non-streaming `AgentLoop.Run` path is structurally identical but
inlines token accumulation + final-response decisions in one function.
Both share the same helpers (`validateStructuredOutput`, `validateTurn`,
`applyBeforeToolExecutionHooks`).

## Why Iterator AND AgentLoop.Run?

- `Iterator` powers `agent.Run` / `agent.Iterate`. It emits `Step` events
  the way an HTTP/SSE UI needs them: chunks as they stream, tool calls as
  they happen, final response once.
- `AgentLoop.Run` is the older synchronous interface, still exposed for
  callers that don't need step granularity.

They duplicate some control flow but share every helper (validators,
limits, hook firing). The duplication is intentional and small —
factoring it out would tangle the iteration pattern with the
synchronous one. New features need to be wired in BOTH places; tests
typically exercise both paths.

## Per-run state lives on Iterator

Anything that varies per `Agent.Run` call sits on the `Iterator`:

- `inputTokens`, `outputTokens`, `cachedTokens` — running totals
- `output` — last (or final) text
- `turns`, `status` — outcome
- `validatorFails`, `outputRetriesUsed` — retry budgets (declared local to `run`)
- `history` — the conversation
- `done`, `proxy` — lifecycle plumbing

The `AgentLoop` itself is read-only on the hot path: it holds provider,
hooks, costModel, memoryMgr, pauseMgr, structuredOutput, tools — all
construction-time fields. Concurrent runs share them safely (verified by
`concurrency_test.go` under `-race`).

The PauseManager is the one shared mutable surface; its concurrency
hazard was closed by RequestID routing in
`pause/concurrent_test.go::TestPauseManager_ConcurrentPausesRouteByRequestID`.

## Provider abstraction

```go
type LLMProvider interface {
    Model() string
    Chat(ctx, req LLMRequest) (*LLMResponse, error)
    ChatStream(ctx, req LLMRequest) (<-chan StreamChunk, error)
    Translator() Translator
}
```

Optional capability interfaces:

```go
type ResponseFormatCapable interface {
    SupportsResponseFormat() bool
}
```

Probed via `provider.SupportsNativeResponseFormat(p)`. OpenAI + Google
return true; Anthropic doesn't implement the interface (so the loop
injects a `final_response` tool instead).

`Translator.ToNative` returns provider-specific request types (`openai.ChatCompletionNewParams`,
`anthropic.MessageNewParams`, `*genaiRequest`). Each provider's `Chat` /
`ChatStream` extracts the translated payload, applies request-level
config (`MaxTokens`, `Temperature`, `Reasoning`, `ToolChoice`,
`ResponseSchema`), and dispatches.

`RetryProvider` is a thin wrapper implementing `LLMProvider` itself, so
it composes with `ProviderQueue` and any other middleware.

## Tool authoring path

```
tool.NewTool[I any](schema I, fn func(ctx, I) (string, error), cfg ToolConfig, opts ...ToolOption)
  ├─ GenerateSchema(reflect.TypeOf(I))               // → json.RawMessage with additionalProperties:false, time.Time → date-time
  ├─ CompileSchema(name, rawSchema)                  // santhosh-tekuri/jsonschema/v6 — one compile per tool
  ├─ wrap fn into func(ctx, json.RawMessage) (string, error)
  └─ apply opts (WithPreExecute, ...)
```

Per-call validation in `Tool.Execute`:

1. `Validate(t, args)` — schema check against the compiled schema (cheap).
2. `preExecute(ctx, args)` — business validation (optional).
3. Retry loop around `execute(ctx, args)` per `cfg.Retries`.

`RejectionError` returned from `preExecute` propagates out — the loop
turns it into a `tool_result` with `IsError=true`, so the LLM sees the
hint. Plain errors fail the call without retry.

## Structured output: two paths, one validator gate

Both paths land at the same validation gate:

- **Native** (OpenAI / Gemini): `LLMRequest.ResponseSchema` is sent → provider returns `Content` = JSON. Loop sees no tool calls, branches to "final-text" → `validateStructuredOrAbort(final)`.
- **Tool-injection** (Anthropic): loop adds a synthetic `final_response` tool whose schema matches T. Model "calls" it. Loop's `extractFinalResponseOutput` short-circuits → `validateStructuredOrAbort(out)`.

`validateStructuredOrAbort` returns `(ok, abort)`:

- `ok=true` → commit (recordFinal + emit step + return)
- `ok=false, abort=false` → hint added to history, break → outer loop re-prompts
- `ok=false, abort=true` → exhausted, recordFinal with `status="output_validation_exhausted"` and return

Counter `outputRetriesUsed` is local to `run`, so per-Iterator and
per-run, not per-Agent.

## Hooks: two payload shapes

| Type | Payload | Use |
|------|---------|-----|
| `BeforeCall`, `AfterCall`, `OnCancel`, `BeforeFinalResponse`, `AfterFinalResponse` | `*CallParams` | History inspection, telemetry, mutation between turns |
| `BeforeToolExecution` | `*ToolExecutionParams` | Inspect / cancel / replace planned tool calls |

Two registries in `HookManager`: a `map[HookType][]Hook` for the
CallParams family, plus a separate `[]ToolCallHook` slice for tool-call
hooks. Locked under one RWMutex; reads are cheap, writes are
construction-time so contention is low.

## Memory

Three strategies under one interface:

```go
type MemoryManager interface {
    Manage(ctx context.Context, history *message.History) error
}
```

- `SlidingWindow` — keep last N messages. Cheap, lossy.
- `TokenBudget` — when len > Budget/4, delegate to a Summarizer (or fallback truncate).
- `Summarizer` — user-supplied `SummarizeFunc(ctx, []Message) (string, error)`. Replaces older messages with a single `MessageSystem`, preserves last `KeepLast` verbatim.

`History.TruncateByTurns(n)` is the surgical primitive: cuts at a user-message
boundary so tool_use ↔ tool_result pairs aren't separated (which
Anthropic rejects with HTTP 400).

## File layout

```
.
├── agent.go              # NewAgent, Run, Iterate
├── agent_options.go      # WithXxx AgentOptions
├── structured_output.go  # WithStructuredOutput[T], Decode[T], WithOutputRetries / Validator
├── deps.go               # WithRunDeps[T], Deps[T]
├── tracer.go             # OTel + LOOPER_TRACE_ENDPOINT writer
├── result.go             # Public types (RunResult, CostBreakdown, Usage)
├── loop/
│   ├── loop.go           # AgentLoop, Iterator, Run, run()
│   ├── validator.go      # TurnValidator + validateTurn helper
│   ├── before_tool_hook.go
│   ├── dynamic_tools.go
│   ├── limits.go         # UsageLimits
│   └── hooks.go          # HookManager + HookType constants
├── provider/
│   ├── provider.go       # LLMProvider / Translator interfaces
│   ├── response_format.go # ResponseFormatCapable opt-in
│   ├── tool_choice.go    # ToolChoice union
│   ├── retry.go          # RetryProvider middleware
│   ├── queue.go          # ProviderQueue failover
│   ├── cache.go          # CacheStrategy / CacheConfig
│   ├── openai/
│   ├── anthropic/
│   └── google/
├── tool/
│   ├── tool.go           # NewTool, MustNewTool, NewToolFromRawSchema
│   ├── schema.go         # GenerateSchema
│   ├── validate.go       # Validate, CompileSchema
│   ├── preexecute.go     # WithPreExecute, RejectWithHint
│   └── registry.go       # ToolRegistry (used by Skill / Toolkit)
├── message/
│   ├── message.go        # Message, ToolCall, ToolResult
│   ├── part.go           # Part, PartType, constructors
│   └── history.go        # History (thread-safe)
├── memory/
│   ├── memory.go         # MemoryManager interface
│   └── strategies.go     # SlidingWindow, TokenBudget, Summarizer
├── pause/
│   ├── pause.go          # PauseManager (RequestID-routed)
│   └── state.go
├── skill/
├── toolkit/
├── telemetry/
│   ├── telemetry.go      # OTel CostTracker
│   ├── cost.go           # CostModel + per-provider pricing
│   ├── modelcosts.go     # Default price table
│   └── otelconfig.go
├── mcp/
│   └── client.go         # ToolProvider over mark3labs/mcp-go
├── internal/web/          # Dashboard / SSE / templ panel
├── cmd/looper/            # CLI (serve, run, mcp)
├── examples/              # 18 self-contained examples
└── tests/e2e/             # Real-network suite gated by //go:build e2e
```

## Design principles

1. **Functional-first.** Tools, hooks, validators are functions with
   config structs. Interfaces are escape hatches for users who need
   them.
2. **Errors as values.** No panics in production paths. `MustX`
   companions exist for declarative test / example code where a
   misconfiguration is a programmer error.
3. **Per-run state on Iterator, never on Agent.** Concurrent sessions
   on the same Agent are safe by construction.
4. **Provider opt-in capabilities.** Native response_format, cache
   breakpoints, reasoning configs are surfaced via interfaces that
   providers can choose to implement.
5. **Validation everywhere it matters.** Tool inputs validated against
   schema. Structured output validated against T's schema. Both with
   auto-retry + LLM-visible hints.
6. **Composable middleware.** `RetryProvider`, `ProviderQueue`, the
   MCP `ToolProvider` all implement the standard interfaces so they
   stack without special-casing.

## Where to add new features

| Adding... | Touch... |
|-----------|----------|
| A new provider | `provider/<name>/` with `Translator.ToNative` + `Chat` + `ChatStream`. Optionally implement `ResponseFormatCapable`. |
| A new LLMRequest field (request-level config) | `provider.LLMRequest` + each provider's Chat/ChatStream + `loop.AgentLoop` (forward from agent option). |
| A new lifecycle hook with the same shape | `HookType` constant in `loop/hooks.go` + `Trigger` site in `loop.go`. |
| A new hook with a different payload | Add a separate slice + method on `HookManager` (see `OnBeforeToolExecution`). |
| A new validator class | A helper on `AgentLoop` returning `(ok, abort)` and a counter local to `run()`. See `validateTurn` / `validateStructuredOrAbort` as templates. |
| A new memory strategy | Implement `memory.MemoryManager`. |
| A new option | `WithXxx` in `agent_options.go` that writes a field on `*Agent`; mirrored `WithLoopXxx` in `loop/` that writes the corresponding field on `*AgentLoop`; wire it inside `agent.go`'s NewAgent. |
| Tests | Default suite stays no-network. Real-API checks go under `tests/e2e/` with the `e2e` build tag. |

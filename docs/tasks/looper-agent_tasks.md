# Tasks: Looper Agent — Implementation Plan

**Date:** 2026-05-11  
**Depends on:** [PRD: looper-agent](./prds/looper-agent_2026-05-11.md)  

---

## Dependency Graph

```
T01_init
 │
 ├─▶ T02_message
 │    │
 │    └─▶ T03_tool
 │         │
 │         ├─▶ T04_provider_iface ──▶ T05_openai_provider
 │         ├─▶ T06_skill_toolkit
 │         ├─▶ T07_memory
 │         ├─▶ T08_mcp_client
 │         └─▶ T09_pause
 │
 ├─▶ T10_loop_hooks ──▶ T11_loop_engine
 │
 ├─▶ T12_telemetry_cost ──▶ T13_telemetry_otel
 │
 └─▶ T14_agent_core ──▶ T15_agent_options ──▶ T16_result
      │
      ├─▶ T17_structured_output
      ├─▶ T18_streaming
      └─▶ T19_examples

 T20_anthropic_provider
 T21_google_provider
 T22_memory_impl
 T23_pause_impl
 T24_hooks_impl
 T25_cost_tracker_impl

 T26_cli_serve ──▶ T27_web_ui
 T28_cli_mcp_debug

 T29_tests
 T30_docs
```

---

## Phase 2: Skeleton (Current Phase)

### T01 — Module initialization
**ID:** T01  
**Package:** root  
**Files:** `go.mod`, `go.sum`  
**Description:** Initialize Go module `github.com/cuatroochenta-idi/looper-agent` with Go 1.22. Add minimal dependencies: `google/uuid`, `invopop/jsonschema`, `otel`, `x/sync/errgroup`.  
**Dependencies:** None  
**Acceptance:** `go mod tidy` succeeds, `go build ./...` compiles (empty files OK)  
**Effort:** S

---

### T02 — Message system
**ID:** T02  
**Package:** `message/`  
**Files:** `message/message.go`, `message/history.go`  
**Description:** Implement `Message`, `MessageType`, `ToolCall`, `ToolResult` types. Implement `History` struct with thread-safe CRUD operations (`AddUserMessage`, `AddAssistantMessage`, `AddToolResult`, `AddSystemMessage`, `Update`, `Remove`, `InsertBefore`, `Truncate`, `TurnCount`). JSON serialization (`MarshalJSON`/`UnmarshalJSON`). `UnmarshalHistory()` constructor.  
**Key design:** System prompt NOT stored in History. Provider injects it at translation time. System messages from hooks DO persist.  
**Dependencies:** T01  
**Acceptance:** Types compile. History roundtrip `MarshalJSON → UnmarshalJSON` preserves all fields. Thread-safe (use `sync.RWMutex`).  
**Effort:** M

---

### T03 — Tool system
**ID:** T03  
**Package:** `tool/`  
**Files:** `tool/tool.go`, `tool/registry.go`, `tool/schema.go`, `tool/validate.go`  
**Description:**  
- `ToolConfig` struct: Name, Description, Retries, Parallel, Timeout  
- `NewTool[I any](schema I, fn func(context.Context, I) (string, error), cfg ToolConfig) *Tool` — generic constructor that auto-generates JSON schema  
- `Tool` struct: config, schema (json.RawMessage), execute closure  
- `ToolRegistry`: thread-safe registry with `Register(schema any, fn any, cfg ToolConfig)` and `Tools() []*Tool`  
- `GenerateSchema(v any) (json.RawMessage, error)` — generates JSON schema from Go struct with `jsonschema` tags  
- `Validate(t *Tool, args json.RawMessage) error` — validates args against tool's JSON schema  
**Dependencies:** T02  
**Acceptance:** `NewTool` with a struct input generates valid JSON schema. Validation rejects malformed args.  
**Effort:** L

---

### T04 — Provider interface
**ID:** T04  
**Package:** `provider/`  
**Files:** `provider/provider.go`, `provider/cache.go`, `provider/queue.go`  
**Description:**  
- `LLMProvider` interface: `Model()`, `Chat()`, `ChatStream()`, `Translator()`  
- `Translator` interface: `ToNative(systemPrompt, messages, tools)`, `FromNative(response)`  
- `LLMRequest`: SystemPrompt, Messages, Tools, Stream, Model, MaxTokens, Temperature  
- `LLMResponse`: Content, ToolCalls, Usage, IsFinal  
- `Usage`: InputTokens, OutputTokens, CachedTokens  
- `StreamChunk`: Content, ToolCalls, IsFinal, Usage, Error  
- `CacheStrategy`: CacheAuto, CacheAlways, CacheDisabled  
- `CacheConfig`: Strategy, MinTokens, MaxTokens  
- `ProviderQueue`: queue of providers with failover, `Execute(ctx, fn)` that tries each provider in order  
**Dependencies:** T02, T03  
**Acceptance:** Interfaces compile. ProviderQueue iterates providers on failure.  
**Effort:** M

---

### T05 — OpenAI provider stub
**ID:** T05  
**Package:** `provider/openai/`  
**Files:** `provider/openai/openai.go`  
**Description:** Skeleton implementation of `LLMProvider` for OpenAI. `NewProvider(apiKey string, opts ...Option)` constructor. `Translator` stub that maps `message.Message` to OpenAI format.  
**Dependencies:** T04  
**Acceptance:** Compiles. Implements `LLMProvider` interface.  
**Effort:** S (stub only in Phase 2)

---

### T06 — Skill & Toolkit interfaces
**ID:** T06  
**Package:** `skill/`, `toolkit/`  
**Files:** `skill/skill.go`, `toolkit/toolkit.go`  
**Description:**  
- `Skill` interface: `Name() string`, `RegisterTools(reg *ToolRegistry)`, `PromptFragment() string`  
- `Toolkit` interface: `RegisterTools(reg *ToolRegistry)`  
**Dependencies:** T03  
**Acceptance:** Interfaces compile.  
**Effort:** XS

---

### T07 — Memory manager interface
**ID:** T07  
**Package:** `memory/`  
**Files:** `memory/memory.go`, `memory/strategies.go`  
**Description:**  
- `MemoryManager` interface: `Manage(ctx, history) error`  
- `SlidingWindow` struct: MaxMessages, MaxTokens  
- `Summarizer` struct (stub)  
- `TokenBudget` struct: Budget, Summarizer  
- Strategy constants: sliding_window, summarization, token_budget  
**Dependencies:** T02  
**Acceptance:** Compiles. Strategy structs satisfy MemoryManager interface.  
**Effort:** S

---

### T08 — MCP client interface
**ID:** T08  
**Package:** `mcp/`  
**Files:** `mcp/client.go`  
**Description:** `MCPToolProvider` struct with `NewMCPToolProvider(serverCommand, args...)(*MCPToolProvider, error)`. Method `Tools(ctx)([]*tool.Tool, error)`. Method `Close() error`.  
**Dependencies:** T03  
**Acceptance:** Compiles.  
**Effort:** S

---

### T09 — Pause & Resume skeleton
**ID:** T09  
**Package:** `pause/`  
**Files:** `pause/pause.go`, `pause/state.go`  
**Description:**  
- `PausePointType`: tool_confirm, tool_input, final_response, manual  
- `PauseRequest`, `PauseResponse` structs  
- `PauseManager` struct: `SetPausePoint()`, `Pause()`, `Serialize()`, `Restore()`  
- `SerializedState`: ID, History, CurrentTurn, MaxTurns, PendingTools, Context map  
**Dependencies:** T02  
**Acceptance:** Compiles.  
**Effort:** S

---

### T10 — Loop hooks
**ID:** T10  
**Package:** `loop/`  
**Files:** `loop/hooks.go`  
**Description:**  
- `HookType`: BeforeCall, AfterCall, OnCancel, BeforeFinalResponse, AfterFinalResponse  
- `CallParams`: History, Turn, MaxTurns, SystemPrompt, RunID  
- `Hook` type: `func(context.Context, *CallParams) error`  
- `HookManager` struct: thread-safe, `On(hookType, hook)`, `Trigger(ctx, hookType, params) error`  
**Dependencies:** T02  
**Acceptance:** Compiles. Multiple hooks per type execute in registration order.  
**Effort:** M

---

### T11 — Loop engine
**ID:** T11  
**Package:** `loop/`  
**Files:** `loop/loop.go`  
**Description:**  
- `StepType`: system_prompt, llm_call, tool_call, tool_result, final_response  
- `Step` struct: Type, Content, ToolName, ToolArgs, Turn, Error  
- `AgentLoop` struct with `NewAgentLoop(provider, systemPrompt, tools, opts...)`  
- `Run(ctx, input, opts...) (*RunResult, error)` — full execution  
- `Iterate(ctx, input, opts...) *Iterator` — manual iteration  
- `Iterator` struct with `Next() <-chan Step` channel  
- Tool execution logic: parallel (errgroup) + sequential, error-as-feedback  
- Structured output detection: inject `final_response` tool  
**Dependencies:** T04, T03, T10, T07, T09  
**Acceptance:** Compiles. Types and method signatures are correct.  
**Effort:** L (skeleton in Phase 2, full impl in Phase 3)

---

### T12 — Cost model
**ID:** T12  
**Package:** `telemetry/`  
**Files:** `telemetry/cost.go`, `telemetry/modelcosts.go`  
**Description:**  
- `CostConfig`: InputCostPer1MTokens, OutputCostPer1MTokens, CachedCostPer1MTokens  
- `CostBreakdown`: TotalUSD, InputUSD, OutputUSD, CachedUSD, SavingsUSD, token counts  
- `CostModel` struct with registry: `UpdateCost(provider, model, config)`, `WithCustomCost(model, config)`, `Calculate(provider, model, usage) CostBreakdown`  
- `modelcosts.go`: default costs for OpenAI, Anthropic, Google models (base prices)  
**Dependencies:** T04 (for Usage type)  
**Acceptance:** Cost calculation for gpt-4o with 1000 input + 500 output tokens returns correct USD.  
**Effort:** M

---

### T13 — Telemetry skeleton
**ID:** T13  
**Package:** `telemetry/`  
**Files:** `telemetry/telemetry.go`  
**Description:**  
- `CostTracker` struct: `NewCostTracker(tp, mp)`  
- Span lifecycle: `StartAgentRun()`, `StartTurn()`, `StartLLMCall()`, `StartToolCall()`, `StartHook()`  
- Cost recording: `RecordCost()`, `RecordUsage()`, `RecordCacheHit()`  
- Span attributes: looper.agent.id, looper.llm.provider, looper.tool.name, looper.cost.*, etc.  
- Metrics: counters and histograms for runs, tokens, tool calls, costs  
**Dependencies:** T12  
**Acceptance:** Compiles. Creates spans with correct attributes.  
**Effort:** L

---

### T14 — Agent core
**ID:** T14  
**Package:** root (`looper`)  
**Files:** `agent.go`  
**Description:**  
- `Agent` struct aggregating: provider, systemPrompt func, tools, skills, loop, hooks, memory, telemetry, pause  
- `NewAgent(provider LLMProvider, systemPrompt any, components ...any) *Agent` — systemPrompt can be `string` or `func(ctx) string`. Components can be `*Tool`, `Skill`, `Toolkit`.  
- `Run(ctx, input, opts...) (*RunResult, error)` — delegates to AgentLoop.Run  
- `Iterate(ctx, input, opts...) *Iterator` — delegates to AgentLoop.Iterate  
- `On(hookType string, hook loop.Hook)` — registers hook  
- `WithCustomModelCost(model, config)` — registers custom cost  
**Dependencies:** T04, T03, T06, T07, T11, T09, T12, T13  
**Acceptance:** Compiles. NewAgent accepts all component types.  
**Effort:** M

---

### T15 — Agent options  
**ID:** T15  
**Package:** root  
**Files:** `agent_options.go`  
**Description:** Functional options pattern:  
- `WithHistory(h *message.History)`  
- `WithMaxTurns(n int)`  
- `WithMaxConsecutiveToolRetries(n int)`  
- `WithMetadata(m map[string]any)`  
- `WithTelemetry(tp, mp)`  
- `WithTelemetryVerbose()`  
- `WithMemory(m MemoryManager)`  
- `WithPause(pm *PauseManager)`  
- `WithCustomModelCost(model, config)`  
- `WithCacheConfig(config)`  
**Dependencies:** T14, T02, T07, T09, T12, T13  
**Acceptance:** All option functions compile and apply correctly.  
**Effort:** M

---

### T16 — Result types
**ID:** T16  
**Package:** root  
**Files:** `result.go`  
**Description:** `RunResult` struct: Output, History, Cost (CostBreakdown), Usage (Usage), Turns, Status.  
**Dependencies:** T02, T12  
**Acceptance:** Compiles.  
**Effort:** XS

---

## Phase 3: MVP Implementation

### T17 — Structured output
**ID:** T17  
**Package:** `loop/` (integration in loop engine)  
**Files:** already created, implementation  
**Description:** Detect response type: if `Run` is called with a struct type (via generics or reflection), inject `final_response` tool with instructions in English. Validate struct output against schema before returning.  
**Dependencies:** T11, T14  
**Acceptance:** `agent.Run(ctx, "analyze data")` with `*MyStruct` return type populates struct fields from LLM response.  
**Effort:** M

---

### T18 — Streaming
**ID:** T18  
**Package:** `loop/` + `provider/openai/`  
**Files:** Implementation  
**Description:** Streaming text (thinking), streaming tool calls in progress, streaming final_response. OpenAI provider uses `ChatStream` → channel of `StreamChunk`. Iterator yields partial steps.  
**Dependencies:** T11, T05  
**Acceptance:** Iterator emits StepToolCall and StepToolResult as they happen.  
**Effort:** M

---

### T19 — Examples
**ID:** T19  
**Package:** `examples/`  
**Files:** `examples/01_basic/main.go`, `examples/02_structured/main.go`, `examples/03_tools_streaming/main.go`  
**Description:** Three runnable examples: basic agent, structured output agent, tools + streaming agent. Each with inline tool definitions.  
**Dependencies:** T14, T17, T18  
**Acceptance:** All three examples compile and run (with valid API key).  
**Effort:** M

---

## Phase 4: Advanced Features

### T20 — Anthropic provider
**ID:** T20  
**Package:** `provider/anthropic/`  
**Description:** Full Anthropic provider with Translator. `AnthropicCacheConfig` for `cache_control` breakpoints.  
**Dependencies:** T04  
**Effort:** L

---

### T21 — Google provider
**ID:** T21  
**Package:** `provider/google/`  
**Description:** Full Google GenAI provider with Translator. Context caching support.  
**Dependencies:** T04  
**Effort:** L

---

### T22 — Memory strategies implementation
**ID:** T22  
**Package:** `memory/`  
**Description:** Full implementations: SlidingWindow (truncate old messages), Summarizer (summarize via LLM call), TokenBudget (mixed approach).  
**Dependencies:** T02  
**Effort:** M

---

### T23 — Pause & Resume implementation
**ID:** T23  
**Package:** `pause/`  
**Description:** Full PauseManager with serialization. `agent.OnTool("name").RequireConfirmation()`, `agent.OnTool("name").RequireInput("field")`. Timeout handling.  
**Dependencies:** T09, T14  
**Effort:** L

---

### T24 — Hooks full implementation  
**ID:** T24  
**Package:** `loop/`  
**Description:** Hook chaining with abort semantics. Error returns from hooks cancel the run. Pre/post hook data sharing via context.  
**Dependencies:** T10, T11  
**Effort:** M

---

## Phase 5: Observability

### T25 — Cost tracker full implementation
**ID:** T25  
**Package:** `telemetry/`  
**Description:** Full CostTracker with span attribute injection at each level. Metrics recording (counters, histograms). Cost accumulation across nested agents.  
**Dependencies:** T12, T13, T11  
**Effort:** L

---

## Phase 6: CLI & Web UI

### T26 — CLI serve command
**ID:** T26  
**Package:** `cmd/looper/`  
**Files:** `cmd/looper/main.go`, `cmd/looper/serve.go`  
**Description:** CLI entrypoint with `serve` subcommand. Parses `--port`, starts HTTP server.  
**Dependencies:** T14  
**Effort:** M

---

### T27 — Web UI (templ + htmx)
**ID:** T27  
**Package:** `internal/web/`  
**Files:** `internal/web/server.go`, `internal/web/handlers.go`, templates  
**Description:** Dashboard, run history, live view (SSE), run detail. Templ templates with htmx for interactivity. SSE endpoint streams agent loop steps in real time.  
**Dependencies:** T26, T14, T18  
**Effort:** L

---

### T28 — CLI MCP debug server
**ID:** T28  
**Package:** `cmd/looper/`  
**Files:** `cmd/looper/mcp.go`  
**Description:** `looper mcp` subcommand. Starts MCP server over stdio. Exposes:  
- Resources: `looper://runs`, `looper://runs/{id}`, `looper://costs`  
- Tools: `looper_run`, `looper_analyze_trace`, `looper_replay`, `looper_list_history`  
- Prompts: `looper_debug`  
**Dependencies:** T14, T25  
**Effort:** M

---

## Phase 7: Tests & Docs

### T29 — Tests
**ID:** T29  
**Files:** `*_test.go` in each package  
**Description:** Table-driven tests for:  
- message: History CRUD, serialization roundtrip, thread safety  
- tool: schema generation, validation, parallel/sequential execution  
- provider: Translator roundtrip (openai format)  
- loop: hook ordering, tool execution, error-as-feedback  
- telemetry: cost calculation correctness  
- agent: integration test with mock provider  
**Dependencies:** All implementation tasks  
**Effort:** L

---

### T30 — Documentation
**ID:** T30  
**Files:** `docs/architecture.md`, `docs/getting-started.md`, `docs/provider-setup.md`  
**Description:** Architecture overview, quickstart guide, provider configuration per platform. Package-level godoc comments on all exported types.  
**Dependencies:** All implementation tasks  
**Effort:** M

---

## Summary

| Phase | Tasks | Effort | Status |
|---|---|---|---|
| Phase 2: Skeleton | T01–T16 | 3 days | **In progress** |
| Phase 3: MVP | T17–T19 | 2 days | Pending |
| Phase 4: Advanced | T20–T24 | 3 days | Pending |
| Phase 5: Observability | T25 | 1 day | Pending |
| Phase 6: CLI & Web | T26–T28 | 2 days | Pending |
| Phase 7: Tests & Docs | T29–T30 | 2 days | Pending |

**Effort scale:** XS = <1h, S = 1-2h, M = 2-4h, L = 4-8h

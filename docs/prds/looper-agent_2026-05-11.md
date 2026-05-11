# PRD: Looper Agent — LLM Agent Framework for Go

**Date:** 2026-05-11 | **Status:** Phase 2 — Skeleton | **Author:** @jaumebarrios

---

## 1. Executive Summary

**Looper Agent** es un framework de agentes LLM en Go diseñado para ser lo opuesto a langchain-go: minimalista, transparente y construido directamente sobre las librerías oficiales de cada provider. No impone capas de abstracción opacas; el desarrollador ve exactamente qué mensajes se envían, qué tools se ejecutan, y cuánto cuesta cada llamada.

El framework sigue un **enfoque funcional por defecto**: tools, hooks, providers y cost trackers se definen como funciones + structs de configuración, sin necesidad de implementar interfaces en el 90% de los casos. Para el 10% restante (providers custom, middleware complejo, estrategias de memoria propias), se exponen interfaces bien definidas como escape hatch.

### ¿Por qué otro framework?

| Problema | langchain-go | Looper Agent |
|---|---|---|
| Capas de abstracción | 5+ capas entre el usuario y la API real | 1 capa: el Translator del provider |
| Formato de mensajes | Acoplado al provider | Provider-agnostic, serializable a JSON |
| Tool schema | Definición verbosa, acoplada a langchain | Struct Go con tags `jsonschema` |
| Debugging | Opaco, difícil trazar una llamada | OpenTelemetry nativo en cada span |
| Cost tracking | Módulo externo, opcional | Integrado en cada span del bucle |
| System prompt | Almacenado en History, se duplica al reanudar | Fuera del History, inyectado por provider |
| Dependencias | Pesadas, muchas transitivas | Solo SDKs oficiales + OTel + jsonschema |

---

## 2. Vision & Principles

### 2.1 Filosofía de diseño

El framework sigue una filosofía **functional-first**: el 90% de los casos de uso se resuelven con funciones y structs de configuración. Las interfaces existen solo como escape hatch para casos avanzados.

```
┌─────────────────────────────────────────────────────────────────────┐
│                     FUNCTIONAL API (90% uso)                         │
│                                                                     │
│  NewTool(schema, fn, config)        → define una tool               │
│  NewAgent(provider, prompt, tools)  → crea un agente                │
│  agent.Run(ctx, input, opts...)     → ejecuta el bucle              │
│  agent.On("BeforeCall", hook)       → registra un hook              │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│                     INTERFACES (10% uso)                             │
│                                                                     │
│  LLMProvider     → provider custom                                  │
│  Translator      → formato de API no estándar                       │
│  MemoryManager   → estrategia de memoria propia                     │
│  Skill           → agrupación temática de tools + prompt            │
│  Toolkit         → agrupación de tools con estado compartido        │
│  CacheStrategy   → caching custom para providers no oficiales       │
└─────────────────────────────────────────────────────────────────────┘
```

### 2.2 Principios de diseño

| Principio | Descripción |
|---|---|
| **Go idiomático** | Interfaces pequeñas (1-3 métodos), composición sobre herencia, errores como valores, sin reflection innecesaria |
| **Sin dependencias pesadas** | Solo las librerías de provider (`openai-go`, `anthropic-sdk-go`, `go-genai`), `jsonschema`, y `OpenTelemetry` |
| **Thread-safe** | Todo el framework es seguro para uso concurrente sin locks externos |
| **Zero opinions por defecto** | Cada decisión opinada (maxTurns, retries, cache strategy) tiene un default sensato pero puede sobreescribirse |
| **Documentación en código** | Cada tipo exportado y función pública tiene doc comments completos |
| **Transparencia total** | El usuario puede inspeccionar el History, los spans, y los costes en cualquier momento |

---

## 3. Context & Problem Space

### 3.1 Providers soportados

El framework se construye directamente sobre las librerías oficiales de cada provider, sin wrappers intermedios:

| Provider | Librería base | Notas |
|---|---|---|
| OpenAI | `github.com/openai/openai-go` | Streaming, structured output, cached tokens |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` | `cache_control` breakpoints, content blocks |
| Google | `github.com/googleapis/go-genai` | Context caching, system instruction |
| Custom (OpenRouter) | Compatible API OpenAI | Usa el provider OpenAI con base URL custom |
| Custom (Ollama) | Compatible API OpenAI | Modelos locales, zero cost |
| Custom (LMStudio) | Compatible API OpenAI | Modelos locales, zero cost |
| Custom (llama-server) | Compatible API OpenAI | Modelos locales, zero cost |

### 3.2 Casos de uso objetivo

1. **Agente conversacional básico**: chat con system prompt, sin tools
2. **Agente con tools**: búsqueda web, acceso a APIs, ejecución de código
3. **Agente con structured output**: extracción de datos estructurados de texto
4. **Agente multi-turno con memoria**: conversaciones largas con gestión de contexto
5. **Agentes anidados**: un agente orquesta a otros agentes como tools
6. **Agentes con intervención humana**: pause/resume para confirmación o input
7. **Agentes con MCP tools**: integración con el ecosistema MCP

---

## 4. Arquitectura Detallada

### 4.1 Package Structure

```
github.com/cuatroochenta-idi/looper-agent/
├── agent.go                   # Agent struct: NewAgent, Run, Iterate, On
├── agent_options.go           # Functional options: WithHistory, WithMaxTurns...
├── result.go                  # RunResult, StreamResult
│
├── message/                   # Provider-agnostic message system
│   ├── message.go             # Message, MessageType, ToolCall, ToolResult
│   └── history.go             # History (CRUD, serialization, editing)
│
├── tool/                      # Tool schema & validation
│   ├── tool.go                # Tool, ToolConfig, NewTool[T any]
│   ├── registry.go            # ToolRegistry (for toolkits/skills)
│   ├── schema.go              # JSON Schema generation from Go structs
│   └── validate.go            # Runtime validation of tool inputs
│
├── provider/                  # Provider abstraction layer
│   ├── provider.go            # LLMProvider, Translator, LLMRequest/Response
│   ├── cache.go               # CacheStrategy, CacheConfig
│   ├── queue.go               # ProviderQueue, key rotation (round-robin)
│   ├── openai/openai.go       # OpenAI provider (lib: openai-go)
│   ├── anthropic/anthropic.go # Anthropic provider (lib: anthropic-sdk-go)
│   └── google/google.go       # Google provider (lib: go-genai)
│
├── loop/                      # Agentic loop engine
│   ├── loop.go                # AgentLoop, Iterator, Step, StepType
│   └── hooks.go               # HookManager, HookType, CallParams
│
├── skill/skill.go             # Skill interface (tools + prompt fragment)
├── toolkit/toolkit.go         # Toolkit interface (tools + shared state)
│
├── memory/                    # Memory management
│   ├── memory.go              # MemoryManager interface
│   └── strategies.go          # SlidingWindow, Summarization, TokenBudget
│
├── telemetry/                 # Observability & cost tracking (integrated)
│   ├── telemetry.go           # CostTracker, OpenTelemetry span lifecycle
│   ├── cost.go                # CostModel, CostBreakdown, CostConfig
│   ├── modelcosts.go          # Base costs registry (OpenAI/Anthropic/Google)
│   └── otelconfig.go          # OTel server config (local endpoint + env vars)
│
├── pause/                     # Pause & Resume (man-in-the-middle)
│   ├── pause.go               # PauseManager, pause points declarativos
│   └── state.go               # SerializedAgentState (full state snapshot)
│
├── mcp/client.go              # MCPToolProvider (consume external MCP tools)
│
├── cmd/looper/                # CLI tool
│   ├── main.go                # Entry point + subcommands
│   ├── serve.go               # looper serve → web UI (templ + htmx + SSE)
│   ├── run.go                 # looper run main.go --args → debug mode
│   └── mcp.go                 # looper mcp → MCP server (stdio) for debugging
│
├── internal/web/              # Web UI implementation
│   ├── server.go              # HTTP server setup + SSE streaming
│   ├── handlers.go            # Route handlers
│   ├── templates/             # Templ files (server-side rendering)
│   │   ├── base.templ
│   │   ├── dashboard.templ
│   │   ├── live_run.templ
│   │   ├── run_detail.templ
│   │   └── costs.templ
│   └── static/htmx.min.js    # htmx (single file, no build step)
│
├── examples/
│   ├── 01_basic/main.go       # Agente conversacional mínimo
│   ├── 02_structured/main.go  # Structured output con struct Go
│   └── 03_tools_streaming/main.go # Tools + streaming + cost tracking
│
├── docs/
│   ├── prds/                  # PRDs históricos
│   ├── tasks/                 # Task breakdowns
│   ├── architecture.md        # Documentación de arquitectura
│   ├── getting-started.md     # Guía de inicio rápido
│   └── provider-setup.md      # Configuración por provider
│
├── go.mod
└── go.sum
```

### 4.2 Dependency Graph Between Packages

```
                     ┌──────────────────────┐
                     │   looper (root pkg)   │
                     │ Agent, RunResult, ... │
                     └──────────┬───────────┘
            ┌───────────────────┼───────────────────┐
   ┌────────▼───────┐  ┌───────▼────────┐  ┌───────▼────────┐
   │     loop       │  │     pause      │  │   telemetry    │
   │ AgentLoop      │  │ PauseManager   │  │ CostTracker    │
   │ HookManager    │  │ SerializedState│  │ CostModel      │
   └────────┬───────┘  └───────┬────────┘  └───────┬────────┘
        ┌───┼───┐              │                    │
        │   │   │              │                    │
        ▼   ▼   ▼              ▼                    ▼
   ┌────────┐┌────────┐┌──────────┐┌────────┐┌──────────┐
   │provider││  tool  ││ message  ││ memory ││  skill   │
   │LLMProv ││ Tool   ││ Message  ││MemMgr  ││ Skill    │
   │Transl  ││Registry││ History  ││Strategy││          │
   └───┬────┘└───┬────┘└────┬─────┘└───┬────┘└──────────┘
       │         │          │          │
       ▼         ▼          ▼          ▼
   ┌────────┐┌────────┐┌────────┐┌────────┐
   │toolkit ││  mcp   ││ pause  ││  ...   │
   │Toolkit ││Client  ││ State  ││        │
   └────────┘└────────┘└────────┘└────────┘
```

**No hay ciclos de dependencia.** El grafo es estrictamente acíclico.

### 4.3 Core Interfaces

#### LLMProvider

```go
// LLMProvider abstrae cualquier API de LLM bajo una interfaz unificada.
// Cada implementación (openai, anthropic, google, custom) encapsula
// su propio Translator para convertir mensajes universales a formato nativo.
type LLMProvider interface {
    Model() string
    Chat(ctx context.Context, req LLMRequest) (*LLMResponse, error)
    ChatStream(ctx context.Context, req LLMRequest) (<-chan StreamChunk, error)
    Translator() Translator
}
```

#### Translator

```go
// Translator convierte entre el formato universal de mensajes y el
// formato nativo de cada provider. El usuario NUNCA interactúa con estos.
type Translator interface {
    // ToNative convierte mensajes + system prompt al formato de la API.
    // El systemPrompt se inyecta aquí, NO desde el History.
    ToNative(systemPrompt string, messages []message.Message, tools []*tool.Tool) any

    // FromNative convierte la respuesta de la API al formato universal.
    FromNative(response any) (*LLMResponse, error)
}
```

#### Skill

```go
// Skill agrupa tools relacionadas con un fragmento de prompt temático.
// A diferencia de Toolkit, una Skill modifica el system prompt.
type Skill interface {
    Name() string
    RegisterTools(reg *tool.ToolRegistry)
    PromptFragment() string
}
```

#### Toolkit

```go
// Toolkit agrupa tools relacionadas con estado interno compartido
// (API keys, caches, rate limiters). No modifica el system prompt.
type Toolkit interface {
    RegisterTools(reg *tool.ToolRegistry)
}
```

#### MemoryManager

```go
// MemoryManager controla el tamaño del History para evitar token overflow.
// Estrategias: sliding window, summarization, token budget.
type MemoryManager interface {
    Manage(ctx context.Context, history *message.History) error
}
```

---

### 4.4 System Prompt — Fuera del History

**Decisión arquitectónica crítica:** el system prompt base del agente NUNCA se almacena en el History. Esto evita que se duplique al reanudar una conversación con `WithHistory(existingHistory)`.

```
┌─ Agent ──────────────────────────────────────┐
│                                               │
│  systemPrompt: func(ctx) string              │
│  + skill.PromptFragment() concatenados        │
│                                               │
│  ↓ En cada llamada al LLM:                    │
│                                               │
│  ┌─ Translator.ToNative() ────────────────┐  │
│  │                                         │  │
│  │  [system] systemPrompt(ctx)             │  │ ← Resuelto dinámicamente
│  │  [system] skill_1.PromptFragment()      │  │ ← Fuera del History
│  │  [system] skill_2.PromptFragment()      │  │ ← Fuera del History
│  │  [system] structured output instructions│  │ ← Inyectado si struct output
│  │  [user]   History[0]                    │  │ ← Del History
│  │  [assist] History[1]                    │  │ ← Del History
│  │  [tool]   History[2]                    │  │ ← Del History
│  │  [user]   History[3]                    │  │ ← Del History
│  │  ...                                    │  │
│  └─────────────────────────────────────────┘  │
│                                               │
│  History solo contiene mensajes de conversación│
│  + system messages inyectados por hooks        │
└───────────────────────────────────────────────┘
```

**Flujo sin duplicación:**

```go
// Run 1: crea history fresco
agent := NewAgent(openaiProvider, "You are a helpful assistant")
result1, _ := agent.Run(ctx, "What is 2+2?")
// History: [user: "What is 2+2?", assistant: "4"]
// System prompt "You are a helpful assistant" NO está en el History

// Persistir
data, _ := result1.History.MarshalJSON()
db.Save(ctx, "session-123", data)

// Run 2: reanuda sin duplicar el system prompt
data := db.Load(ctx, "session-123")
history, _ := message.UnmarshalHistory(data)
result2, _ := agent.Run(ctx, "What about 3+3?",
    WithHistory(history),
)
// History: [user: "What is 2+2?", assistant: "4", user: "What about 3+3?", assistant: "6"]
// El system prompt se inyecta fresco en cada llamada, no se duplica
```

### 4.5 Agentic Loop Flow

```
agent.Run(ctx, input, opts...)
│
├─ 1. Resolver systemPrompt(ctx) — string o func(ctx) string
├─ 2. Concatenar skill.PromptFragment() de todas las Skills
├─ 3. Injectar instrucciones de structured output (si output es struct)
├─ 4. Init History (NewHistory o restaurar de WithHistory)
├─ 5. Init OTel trace raíz (si WithTelemetry configurado)
├─ 6. AddUserMessage(input) al History
│
└─▶ FOR turn := 0; turn < maxTurns; turn++
    │
    ├─▶ [Hook: BeforeCall]
    │   El hook recibe *CallParams{History, Turn, MaxTurns, SystemPrompt, RunID}
    │   Puede modificar History, inyectar contexto, o abortar (return error)
    │
    ├─▶ [Memory: Manage]
    │   MemoryManager.Manage(ctx, history) — trunca, resume, o poda mensajes
    │   Se ejecuta antes de la llamada al LLM para controlar token budget
    │
    ├─▶ [LLM Call]
    │   ├─ Translator.ToNative(systemPrompt, history.Messages(), tools)
    │   │   Convierte mensajes universales → formato nativo del provider
    │   │   Inyecta system prompt como primer mensaje (NO modifica History)
    │   │
    │   ├─ Provider.Chat(req) o Provider.ChatStream(req)
    │   │   Si ChatStream: leer del channel, emitir StepStreamingChunk
    │   │
    │   ├─ Translator.FromNative(response)
    │   │   Convierte respuesta nativa → LLMResponse universal
    │   │
    │   └─ Registrar usage + cost en span OTel
    │       costTracker.RecordUsage(span, usage)
    │       costTracker.RecordCost(span, costModel.Calculate(provider, model, usage))
    │       Si hay cached tokens: costTracker.RecordCacheHit(span, cachedTokens, savings)
    │
    ├─▶ ¿El LLM devolvió tool_calls?
    │   │
    │   ├─ NO:
    │   │   ├─ ¿Tipo de respuesta es struct?
    │   │   │   SÍ → el LLM debería haber llamado a final_response tool
    │   │   │        Parsear args como struct → validar → [Hook: BeforeFinalResponse] → return RunResult
    │   │   │   NO → texto libre → [Hook: BeforeFinalResponse] → return RunResult
    │   │   │
    │   │   └─ AddAssistantMessage(content, toolCalls) al History
    │   │
    │   └─ SÍ:
    │       ├─ AddAssistantMessage(content, toolCalls) al History
    │       │
    │       ├─ Separar tool calls en dos grupos:
    │       │   ├─ Parallel=true  → ejecutar con errgroup (concurrente)
    │       │   └─ Parallel=false → ejecutar secuencialmente (en orden)
    │       │
    │       ├─ Para cada tool call (respetando orden secuencial):
    │       │   ├─ [Span: tool.validate]
    │       │   │   Validar args contra JSON schema de la tool
    │       │   │   Si falla → tool result con IsError=true (feedback, no excepción)
    │       │   │
    │       │   ├─ [Pause: check]
    │       │   │   Si la tool tiene un pause point configurado:
    │       │   │   ├─ PauseManager.Pause(ctx, PauseRequest{ToolName, Type, Message})
    │       │   │   ├─ Esperar respuesta externa (timeout → cancel o default action)
    │       │   │   └─ Si respuesta = "cancel" → skip tool
    │       │   │
    │       │   ├─ [Span: tool.call]
    │       │   │   Ejecutar tool.Execute(ctx, args)
    │       │   │   Con timeout si ToolConfig.Timeout > 0
    │       │   │   Con retries si ToolConfig.Retries > 0 (solo errores transitorios)
    │       │   │
    │       │   ├─ [Event: tool.output]
    │       │   │   Capturar resultado o error
    │       │   │
    │       │   └─ AddToolResult(callID, name, result, isError) al History
    │       │
    │       └─ Continuar al siguiente turno (el LLM procesa los tool results)
    │
    ├─▶ [Hook: AfterCall]
    │   Puede inspeccionar History, añadir mensajes de sistema, loggear, etc.
    │
    ├─▶ ¿maxTurns alcanzado?
    │   SÍ → error "max turns exceeded" → cancel
    │
    └─▶ ¿Final response?
        SÍ → [Hook: BeforeFinalResponse] → [Hook: AfterFinalResponse] → return RunResult
```

### 4.6 Tool Execution: Secuencial vs Paralelo

```
Tool calls del LLM en un turno:
  [tool_a (parallel=true), tool_b (parallel=false), tool_c (parallel=true)]

Ejecución:
  ┌─ Fase 1: tool_a y tool_c se lanzan en paralelo (errgroup) ──────────┐
  │  go tool_a.Execute()            go tool_c.Execute()                  │
  │  (validación previa)            (validación previa)                  │
  └─ Ambas terminan ─────────────────────────────────────────────────────┘
  ┌─ Fase 2: tool_b (parallel=false) se ejecuta secuencial ─────────────┐
  │  tool_b.Execute()  ← espera a que terminen tool_a y tool_c          │
  └──────────────────────────────────────────────────────────────────────┘
```

### 4.7 Error como Feedback

Cuando una tool falla, el framework NO lanza una excepción ni aborta el bucle. En su lugar:

```go
// La tool falla
result, err := tool.Execute(ctx, args)
if err != nil {
    // Se añade al History como tool result con IsError=true
    history.AddToolResult(callID, toolName, err.Error(), true)
    // El LLM en el siguiente turno ve este mensaje y puede autocorregirse
}
```

El LLM recibe algo como:
```
[assistant] Call tool: web_search({query: "golang agents"})
[tool] Error: API rate limit exceeded. Retry after 5 seconds.
```

Y puede decidir reintentar, usar otra tool, o informar al usuario.

### 4.8 Structured Output

```go
type AnalysisResult struct {
    Sentiment string   `json:"sentiment" jsonschema:"description=Positive/Negative/Neutral,enum=Positive|Negative|Neutral"`
    Score     float64  `json:"score" jsonschema:"description=Confidence score,minimum=0,maximum=1"`
    Keywords  []string `json:"keywords" jsonschema:"description=Key topics found"`
}

// El framework detecta que el tipo de retorno NO es string
// → inyecta automáticamente la tool final_response con el schema de AnalysisResult
// → añade instrucciones en inglés al system prompt:
//   "You MUST use the final_response tool to return your answer.
//    Do not respond with plain text. Always call final_response
//    with a valid AnalysisResult object."

result, err := agent.Run(ctx, "Analyze: 'I love this product, it's amazing!'",
    WithResponseType[AnalysisResult](),
)
// result.Output es string (el JSON del struct)
// result.StructuredOutput es *AnalysisResult (parseado y validado)
```

### 4.9 Streaming

```go
iter := agent.Iterate(ctx, "Search for latest Go news and summarize")

for step := range iter.Next() {
    switch step.Type {
    case loop.StepLLMCall:
        fmt.Printf("[LLM call] turn %d\n", step.Turn)

    case loop.StepStreamingChunk:
        // Texto intermedio (pensamiento del LLM)
        fmt.Print(step.Content)

    case loop.StepToolCall:
        fmt.Printf("\n[Tool call] %s(%s)\n", step.ToolName, step.ToolArgs)

    case loop.StepToolResult:
        fmt.Printf("[Tool result] %s\n", step.Content)

    case loop.StepFinalResponse:
        fmt.Printf("\n[Final] %s\n", step.Content)
        break
    }
}
```

---

## 5. OpenTelemetry & Cost Tracking

### 5.1 Configuración

OpenTelemetry es **opcional y configurable**. Si no se configura, el framework usa el no-op provider de OTel (zero overhead).

**Vía código:**
```go
agent := NewAgent(provider, systemPrompt, tools...,
    WithTelemetry(
        WithOTelEndpoint("localhost:4317"),     // OTLP gRPC endpoint
        WithOTelInsecure(),                      // para desarrollo local
    ),
    WithTelemetryVerbose(),                      // incluye prompts en spans (solo dev)
    WithCustomModelCost("local-llama", CostConfig{
        InputCostPer1MTokens:  0.0,
        OutputCostPer1MTokens: 0.0,
        CachedCostPer1MTokens: 0.0,
    }),
)
```

**Vía variables de entorno:**
```bash
LOOPER_OTEL_ENABLED=true
LOOPER_OTEL_ENDPOINT=localhost:4317
LOOPER_OTEL_INSECURE=true
LOOPER_OTEL_VERBOSE=true
```

El CLI `looper serve` arranca automáticamente un collector OTel local para desarrollo, accesible en `localhost:4317` (gRPC) y `localhost:4318` (HTTP). Esto permite que el entorno de desarrollo y el CLI se conecten sin configuración adicional.

### 5.2 OTel Server Local (CLI)

```bash
# Arranca la web UI + OTel collector local + endpoint gRPC
looper serve
# Backend: Jaeger (all-in-one) en :16686
# OTLP gRPC: :4317
# OTLP HTTP: :4318
# Web UI: :9090
```

El comando `looper serve` levanta:
1. Un collector OTel embebido (usando `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`)
2. Un servidor HTTP con la web UI (templ + htmx)
3. Un endpoint SSE para streaming en vivo

### 5.3 Debug Mode: `looper run`

```bash
# Ejecuta un proyecto Go que usa Looper Agent en modo debug
looper run ./main.go --args "--input 'analyze this data' --verbose"

# Con OTel endpoint custom
looper run ./main.go --otel-endpoint localhost:4317 --args "--model gpt-4o"

# Con variables de entorno
LOOPER_DEBUG=true go run ./main.go
```

Cuando `LOOPER_DEBUG=true` o `--debug`, el framework:
- Activa `WithTelemetryVerbose()` (incluye prompts completos en spans)
- Imprime cada step del bucle en stderr
- Expone métricas en `localhost:9091/metrics` (Prometheus)
- Guarda el trace completo en disco (`./looper-traces/run_<id>.json`)

### 5.4 Jerarquía de Spans con Costes Integrados

```
[Trace: agent.run] agent_id="my-agent" run_id="uuid"
│ looper.cost.total_usd=0.00847  looper.cost.input_usd=0.00210
│ looper.cost.output_usd=0.00637  looper.cost.cached_usd=0.00000
│ looper.tokens.total=427  looper.tokens.prompt=210
│ looper.tokens.completion=217  looper.tokens.cached=0
│ turns_used=2  status="completed"
│
├── [Span: agent.loop.turn] turn=1 max_turns=10
│   │ looper.cost.total_usd=0.00623  looper.cost.input_usd=0.00210
│   │ looper.cost.output_usd=0.00413
│   │ looper.tokens.prompt=210  looper.tokens.completion=165
│   │
│   ├── [Span: llm.call] provider="openai" model="gpt-4o" stream=true
│   │   │ looper.cost.total_usd=0.00210  looper.cost.input_usd=0.00210
│   │   │ looper.cost.output_usd=0.00000
│   │   │ looper.tokens.prompt=210  looper.tokens.completion=0
│   │   │ looper.tokens.cached=0  looper.llm.cache.hit=false
│   │   ├── [Event: llm.prompt]     looper.tokens.prompt=210
│   │   ├── [Event: llm.completion] looper.tokens.completion=165
│   │   │   looper.cost.output_usd=0.00413
│   │   └── [Event: llm.streaming_chunk] chunk_index=0..N
│   │
│   ├── [Span: tool.validate] tool="search" looper.tool.valid=true
│   │
│   ├── [Span: tool.call] tool="search" duration_ms=230 parallel=true
│   │   │ looper.cost.total_usd=0.00000
│   │   ├── [Event: tool.input]  args={"query": "golang agents"}
│   │   └── [Event: tool.output] result="{...}"
│   │
│   └── [Span: hook.execute] hook="AfterCall" phase="tool"
│
├── [Span: agent.loop.turn] turn=2
│   │ looper.cost.total_usd=0.00224  looper.cost.input_usd=0.00000
│   │ looper.cost.output_usd=0.00224
│   │ looper.tokens.prompt=375  looper.tokens.completion=52  looper.tokens.cached=375
│   │
│   ├── [Span: llm.call] provider="openai" model="gpt-4o" stream=false
│   │   │ looper.cost.total_usd=0.00224  looper.cost.input_usd=0.00000
│   │   │ looper.cost.output_usd=0.00224
│   │   │ looper.tokens.prompt=375  looper.tokens.completion=52
│   │   │ looper.tokens.cached=375  looper.llm.cache.hit=true
│   │   │ looper.llm.cache.cached_tokens=375  looper.llm.cache.savings_usd=0.00375
│   │   └── [Event: llm.completion] looper.tokens.completion=52
│   │
│   └── [Span: tool.call] tool="final_response"
│       │ looper.cost.total_usd=0.00000
│       └── [Event: tool.output] structured_output=true type="MyResponse"
│
└── [Span: agent.loop.end]
    looper.cost.total_usd=0.00847  looper.cost.savings_usd=0.00375
    looper.tokens.total=427  turns_used=2  status="completed"
```

### 5.5 Span Attributes Reference

| Nivel | Atributo | Tipo | Descripción |
|---|---|---|---|
| `agent.run` | `looper.agent.id` | string | Agent identifier |
| `agent.run` | `looper.agent.run_id` | string | UUID único de ejecución |
| `agent.run` | `looper.cost.total_usd` | float64 | Coste total acumulado |
| `agent.run` | `looper.cost.input_usd` | float64 | Coste de input tokens |
| `agent.run` | `looper.cost.output_usd` | float64 | Coste de output tokens |
| `agent.run` | `looper.cost.cached_usd` | float64 | Coste de cached tokens |
| `agent.run` | `looper.cost.savings_usd` | float64 | Ahorro por caching |
| `agent.run` | `looper.tokens.total` | int | Total de tokens |
| `agent.run` | `turns_used` | int | Turnos ejecutados |
| `agent.run` | `status` | string | completed / error / paused / cancelled |
| `llm.call` | `looper.llm.provider` | string | openai / anthropic / google |
| `llm.call` | `looper.llm.model` | string | gpt-4o / claude-sonnet-4-20250514 |
| `llm.call` | `looper.llm.stream` | bool | Si usó streaming |
| `llm.call` | `looper.llm.cache.hit` | bool | Si hubo cache hit |
| `llm.call` | `looper.llm.cache.cached_tokens` | int | Tokens cacheados |
| `llm.call` | `looper.llm.cache.savings_usd` | float64 | Ahorro en USD |
| `tool.call` | `looper.tool.name` | string | Nombre de la tool |
| `tool.call` | `looper.tool.parallel` | bool | Ejecución paralela |
| `tool.call` | `looper.tool.duration_ms` | int64 | Duración en ms |
| `tool.validate` | `looper.tool.valid` | bool | Resultado validación |
| `hook.execute` | `looper.hook.name` | string | Nombre del hook |
| `hook.execute` | `looper.hook.phase` | string | Fase (BeforeCall, etc) |
| `hook.execute` | `looper.hook.type` | string | pause / resume (para pause points) |

### 5.6 Metrics

| Nombre | Tipo | Labels | Descripción |
|---|---|---|---|
| `looper.agent.runs.total` | Counter | status | Runs completadas/errores/pausadas |
| `looper.llm.tokens.prompt` | Histogram | provider, model | Distribución de prompt tokens |
| `looper.llm.tokens.completion` | Histogram | provider, model | Distribución de completion tokens |
| `looper.llm.tokens.cached` | Histogram | provider, model | Efectividad del caching |
| `looper.tool.calls.duration` | Histogram | tool_name | Latencia de ejecución |
| `looper.tool.calls.errors` | Counter | tool_name, error_type | Errores por tool |
| `looper.cost.total` | Histogram | provider, model | Coste por run |
| `looper.cost.savings` | Histogram | provider | Ahorro por caching |
| `looper.cost.per_turn` | Histogram | provider, model | Distribución de coste por turno |

---

## 6. Pause & Resume (Man-in-the-Middle)

### 6.1 API Declarativa

```go
// Pausar antes de ejecutar una tool destructiva → confirmación
agent.OnTool("delete_file").RequireConfirmation()

// Pausar para pedir un dato que falta → input requerido
agent.OnTool("send_email").RequireInput("recipient")

// Timeout: si no hay respuesta en 30s, cancelar
agent.OnTool("deploy_to_prod").RequireConfirmation(
    WithPauseTimeout(30 * time.Second),
    WithPauseDefaultAction("cancel"),
)

// Pausar antes de la respuesta final (revisión humana)
agent.OnFinalResponse().RequireConfirmation()
```

### 6.2 Flujo de Pause/Resume

```
agente ejecutando...
│
├─▶ tool "delete_file" tiene pause point
│   │
│   ├─▶ PauseManager.Pause(ctx, PauseRequest{
│   │       Type:     PauseToolConfirm,
│   │       ToolName: "delete_file",
│   │       Message:  "About to delete /etc/config.yaml. Confirm?",
│   │       Timeout:  30s,
│   │   })
│   │
│   ├─▶ Serializa estado completo:
│   │       SerializedState{
│   │           ID:           "run-123",
│   │           History:      [...mensajes...],
│   │           CurrentTurn:  3,
│   │           MaxTurns:     10,
│   │           PendingTools: ["delete_file"],
│   │           Context:      {"user_id": "abc", ...},
│   │       }
│   │
│   ├─▶ Guarda en DB/caché → notifica al actor externo
│   │
│   ├─▶ Espera respuesta...
│   │   ├─ "ok"     → ejecuta la tool, continúa el bucle
│   │   ├─ "cancel" → salta la tool, añade tool result con error
│   │   └─ timeout  → acción por defecto (cancel o proceed)
│   │
│   └─▶ Reanuda el bucle donde se quedó
```

### 6.3 Resume desde otro proceso

```go
// Proceso A: el agente se pausa, estado serializado
state := agent.PauseManager().Serialize()
db.Save(ctx, state.ID, state)

// Proceso B (otro servidor, otra máquina): restaurar y reanudar
data := db.Load(ctx, "run-123")
state := pause.RestoreState(data)
agent.Restore(state)

// El actor externo responde
agent.Resume(ctx, PauseResponse{Action: "ok"})
// El bucle continúa exactamente donde se pausó
```

---

## 7. MCP Integration

### 7.1 MCP Client (Framework)

Consume tools MCP externas como si fueran tools nativas del framework:

```go
// Conectar a un MCP server (stdio o HTTP)
mcpProvider, err := mcp.NewMCPToolProvider("npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp")
defer mcpProvider.Close()

// Obtener tools del MCP server
tools, err := mcpProvider.Tools(ctx)

// Usarlas como tools nativas
agent := NewAgent(provider, systemPrompt,
    tools...,                    // tools MCP mezcladas con tools nativas
    &SearchToolkit{APIKey: "..."},
)
```

### 7.2 MCP Debug Server (CLI)

El comando `looper mcp` expone un MCP server vía stdio para que agentes de código (VS Code, Cursor, etc.) puedan inspeccionar y debuggear ejecuciones de Looper:

```
Resources:
  looper://runs              → listado de ejecuciones recientes
  looper://runs/{id}         → detalle completo de una ejecución
  looper://runs/{id}/traces  → trace OTel completo en JSON
  looper://costs             → resumen de costes acumulados

Tools:
  looper_run                 → lanza una ejecución de agente
  looper_analyze_trace       → analiza un trace (errores, cuellos de botella, costes)
  looper_replay              → re-ejecuta un run con modificaciones (ej: añadir retry a tool X)
  looper_list_history        → lista el historial de una conversación

Prompts:
  looper_debug               → prompt predefinido para analizar un trace fallido
```

---

## 8. CLI & Web UI

### 8.1 Comandos

```bash
# Web UI con OTel collector local
looper serve [--port 9090] [--otel-endpoint :4317]

# Debug: ejecuta un proyecto que usa Looper Agent
looper run ./main.go --args "--model gpt-4o --input 'analyze this'"

# MCP debug server (stdio)
looper mcp

# Info
looper version
```

### 8.2 Web UI (templ + htmx)

| Ruta | Método | Descripción | HTMX |
|---|---|---|---|
| `/` | GET | Dashboard con stats, costes acumulados, últimos runs | Full page |
| `/runs` | GET | Listado paginado de ejecuciones históricas | Infinite scroll |
| `/runs/:id` | GET | Detalle: traza completa, spans, costes, timings | Lazy load spans |
| `/live` | GET | Vista en vivo con SSE stream del bucle agéntico | SSE + swap |
| `/live/:run_id` | GET | Seguimiento de un run específico en vivo | SSE + swap |
| `/api/run` | POST | Lanza una ejecución → redirect a `/live/:run_id` | Form submit |
| `/api/runs` | GET | JSON con histórico de ejecuciones (API) | — |
| `/api/costs` | GET | JSON con costes agregados por modelo/provider | — |

La UI usa **templ** para server-side rendering y **htmx** para interactividad sin JavaScript custom. Los costes se muestran con código de colores (verde = ahorro por caching, rojo = sobre límite) y gráficos CSS-only.

### 8.3 SSE Stream Format

```
event: step
data: {"type":"llm_call","turn":1,"content":"Let me search for that..."}

event: step
data: {"type":"tool_call","turn":1,"tool_name":"web_search","tool_args":"{\"query\":\"...\"}"}

event: step
data: {"type":"tool_result","turn":1,"tool_name":"web_search","content":"Found 3 results..."}

event: step
data: {"type":"streaming_chunk","turn":2,"content":"Based on the search results, "}

event: step
data: {"type":"streaming_chunk","turn":2,"content":"the latest Go news includes..."}

event: step
data: {"type":"final_response","turn":2,"content":"The latest Go news..."}

event: done
data: {"cost_total_usd":0.0042,"turns":2,"tokens_total":350,"status":"completed"}
```

---

## 9. Provider-Specific Features

### 9.1 Prompt Caching

El framework soporta los mecanismos de caching de cada provider de forma transparente al usuario pero configurable por provider:

| Provider | Mecanismo | Configuración |
|---|---|---|
| OpenAI | Automatic cached input tokens | `CacheAuto` (default). Se reportan via `cached_tokens` en usage |
| Anthropic | `cache_control` breakpoints | `WithAnthropicCacheConfig(budget int)` inyecta breakpoints en system prompt y tools |
| Google | Context caching API | `WithGoogleCacheConfig(ttl time.Duration)` crea y reutiliza cached contexts |
| Custom | Interfaz `CacheStrategy` | Implementar `CacheStrategy` para lógica custom |

```go
// OpenAI: sin configuración, automático
provider := openai.NewProvider(apiKey,
    openai.WithCacheStrategy(CacheAuto),
    openai.WithCacheMinTokens(1024),
)

// Anthropic: configuración específica
provider := anthropic.NewProvider(apiKey,
    anthropic.WithCacheBreakpoints(
        anthropic.CacheSystemPrompt,
        anthropic.CacheTools,
    ),
)

// El agente es transparente al caching
agent := NewAgent(provider, systemPrompt, tools...)
// Los tokens cacheados se reflejan en result.Cost.CachedUSD y result.Cost.SavingsUSD
```

### 9.2 Provider Queue & Key Rotation

```go
// Cola de providers con failover
queue := provider.NewProviderQueue(
    openai.NewProvider(key1),   // primario
    openai.NewProvider(key2),   // fallback 1
    openai.NewProvider(key3),   // fallback 2
)

// Rotación de API keys por flujo (round-robin)
provider := openai.NewProvider(
    openai.WithAPIKeys(key1, key2, key3),
    openai.WithKeyRotation(provider.RotationRoundRobin),
)
```

---

## 10. Context Tipado

```go
// Definir claves tipadas para el context
type userIDKey struct{}
type sessionKey struct{}

// Inyectar valores en el context antes de ejecutar el agente
ctx = context.WithValue(ctx, userIDKey{}, "user-123")
ctx = context.WithValue(ctx, sessionKey{}, "sess-456")

// El system prompt extrae datos del context
systemPrompt := func(ctx context.Context) string {
    userID, _ := ctx.Value(userIDKey{}).(string)
    return fmt.Sprintf("You are assisting user %s. Be helpful and concise.", userID)
}

// Las tools también reciben el context
searchTool := NewTool(SearchInput{}, func(ctx context.Context, input SearchInput) (string, error) {
    userID, _ := ctx.Value(userIDKey{}).(string)
    // Usar userID para personalizar la búsqueda
    return searchWithUser(ctx, userID, input.Query)
}, ToolConfig{...})
```

---

## 11. Memory Management

### 11.1 Estrategias

| Estrategia | Descripción | Útil para |
|---|---|---|
| `SlidingWindow` | Mantiene los últimos N mensajes o tokens | Conversaciones lineales |
| `Summarization` | Resume mensajes antiguos via LLM call | Conversaciones muy largas |
| `TokenBudget` | Combinación: sliding window + resumen cuando se excede el budget | Balance coste/contexto |

```go
// Sliding window: mantener últimos 50 mensajes
agent := NewAgent(provider, systemPrompt, tools...,
    WithMemory(&memory.SlidingWindow{MaxMessages: 50}),
)

// Token budget: max 8000 tokens, resumir si se excede
agent := NewAgent(provider, systemPrompt, tools...,
    WithMemory(&memory.TokenBudget{
        Budget:     8000,
        Summarizer: memory.NewSummarizer(provider),
    }),
)
```

### 11.2 Agent Nesting & Memory

Cuando un agente lanza otro agente dentro de una tool:
- El agente hijo tiene su propio History y MemoryManager
- El trace del hijo se anida como child span del `tool.call` del padre
- El coste del hijo se propaga al padre
- El atributo `looper.agent.parent_run_id` permite rastrear la jerarquía

---

## 12. Restricciones de Implementación

| Restricción | Razón |
|---|---|
| **Go 1.22+** | Generics completos, `for range` sobre canales |
| **Sin reflection en hot path** | Solo en `NewTool` y `Register` (una vez por tool) |
| **Sin dependencias pesadas** | Solo SDKs oficiales + jsonschema + OTel |
| **Thread-safe** | `sync.RWMutex` en History, ToolRegistry, HookManager |
| **Zero allocations en el loop** | Pre-allocar slices, reutilizar buffers |
| **Context cancellation** | Respetar `ctx.Done()` en todas las operaciones bloqueantes |
| **Sin panics** | Todos los errores se manejan como valores |
| **Doc comments** | Todos los tipos exportados y funciones públicas |

---

## 13. Plan de Fases

| Fase | Descripción | Entregable | Estado |
|---|---|---|---|
| **Fase 1** | Diseño de arquitectura completa | PRD, diagramas, interfaces | ✅ Done |
| **Fase 2** | Esqueleto del proyecto | Todos los packages con interfaces y tipos definidos. `go build ./...` compila | 🔄 In progress |
| **Fase 3** | MVP funcional | Provider OpenAI, tool schema, bucle agéntico, structured output, streaming | ⬜ Pending |
| **Fase 4** | Features avanzadas | Hooks, context tipado, memory management, Anthropic + Google providers | ⬜ Pending |
| **Fase 5** | Observabilidad | OTel integration, cost tracking, pause/resume, MCP client | ⬜ Pending |
| **Fase 6** | CLI + Web UI | `looper serve`, `looper run`, `looper mcp`, web UI (templ + htmx) | ⬜ Pending |
| **Fase 7** | QA & Docs | Tests table-driven, ejemplos ejecutables, documentación completa | ⬜ Pending |

# Rol

Eres un arquitecto de software senior especializado en Go, diseño de frameworks, sistemas distribuidos e IA. Tu tarea es diseñar e implementar un **framework de agentes LLM en Go** llamado **Looper Agent**.

# Contexto

Los frameworks actuales en Go (langchain-go, genkit) imponen demasiados trade-offs y decisiones opacas. Necesitamos un harness minimalista, extensible y transparente que se construya sobre las librerías oficiales de cada provider:

| Provider | Librería base |
|---|---|
| OpenAI | `github.com/openai/openai-go` |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` |
| Google | `github.com/googleapis/go-genai` |
| Custom (OpenRouter, Ollama, LMStudio, llama-server) | Compatible con API OpenAI |

# Objetivo

Diseñar la arquitectura completa del framework e implementar un **MVP funcional** que demuestre todos los subsistemas clave. Prioriza: simplicidad de uso, extensibilidad vía interfaces, y código idiomático en Go.

# Filosofía de diseño

El framework sigue un **enfoque funcional por defecto**: tools, hooks, providers, prompts y cost trackers se definen como funciones y structs de configuración, sin requerir que el usuario implemente interfaces. Para casos avanzados donde se necesita control total (providers custom, middleware complejo, estrategias de memoria propias), el framework expone interfaces que el usuario puede implementar manualmente. El 90% de los casos de uso se resuelven con el API funcional; el 10% restante tiene interfaces bien definidas como escape hatch.

# Arquitectura de subsistemas

Diseña e implementa los siguientes subsistemas. Para cada uno, define las interfaces principales, structs clave, y un ejemplo de uso:

## 1. Tool Schema & Validación

### Definición de tools

Una tool se define como una función que recibe el schema de input, una lambda con la lógica, y un struct de configuración:

```go
// ToolConfig agrupa toda la configuración de una tool
type ToolConfig struct {
    Name        string
    Description string
    Retries     int           // reintentos automáticos si la tool falla
    Parallel    bool          // si true, esta tool puede ejecutarse en paralelo con otras
    Timeout     time.Duration // timeout por ejecución
}

// NewTool crea una tool a partir de un schema de input (struct con tags jsonschema),
// una función ejecutora y su configuración
searchTool := NewTool(SearchInput{}, func(ctx context.Context, input SearchInput) (string, error) {
    return searchWeb(input.Query)
}, ToolConfig{
    Name:        "web_search",
    Description: "Search the web for information",
    Parallel:    true,
    Retries:     2,
})
```

- El input schema se genera desde un struct Go con tags `jsonschema`, soportando tipos nested, enums, descripciones y validación
- Validar los JSON de entrada de cada tool call contra su schema antes de ejecutar la tool
- Si la validación falla, devolver un error tipado que el agente interprete como feedback (no como excepción) para autocorregirse

### Ejecución secuencial vs paralela

- Cada tool declara `Parallel: true/false` en su `ToolConfig`
- Cuando el LLM devuelve múltiples tool calls en un mismo turno:
  - Las tools con `Parallel: true` se ejecutan concurrentemente con `errgroup`
  - Las tools con `Parallel: false` se ejecutan secuencialmente en orden
  - El framework respeta dependencias: si una tool secuencial aparece entre tools paralelas, las paralelas esperan a que la secuencial termine
- El bucle agéntico gestiona la concurrencia de forma transparente

## 2. Provider Abstraction Layer

- Interfaz genérica `LLMProvider` que unifique streaming, non-streaming, structured output y tool calls
- **Cola de providers**: si un provider falla, el siguiente en la cola toma su lugar de forma transparente
- **Multi API key rotation**: configurar N API keys por provider y rotar dinámicamente por flujo (round-robin o random)
- Investigar y proponer cómo abstraer las diferencias entre las 3 APIs oficiales bajo una misma interfaz

### Prompt Caching

El framework soporta los distintos mecanismos de prompt caching de cada provider, de forma **transparente a nivel de framework** pero **configurable a nivel de LLMProvider**:

- **OpenAI**: Automatic cached input tokens (no configuration needed, se reportan via `cached_tokens` en usage). El framework marca los system prompts y tools como candidatos a cacheo reutilizando el mismo system message across turns
- **Anthropic**: Prompt caching con `cache_control` breakpoints. El provider inyecta automáticamente `cache_control: {"type": "ephemeral"}` en los system prompts y tool definitions para habilitar cacheo. El usuario puede configurar qué partes cachear via `AnthropicCacheConfig`
- **Google**: Context caching API. El provider puede crear y reutilizar cached contexts para prompts largos (system prompt + tools estáticos)
- **Custom providers**: interfaz `CacheStrategy` que permite implementar caching custom para providers no oficiales

```go
// Configuración a nivel de provider (opcional, por defecto smart caching)
provider := openai.NewProvider(
    openai.WithCacheStrategy(CacheAuto),     // CacheAuto | CacheAlways | CacheDisabled
    openai.WithCacheMinTokens(1024),         // mínimo de tokens para activar cacheo
)

// A nivel de agente, transparente: el framework reordena mensajes
// y marca breakpoints automáticamente según el provider activo
agent := NewAgent(provider, systemPrompt, tools...)
```

- Los tokens cacheados se reportan en el cost tracker con precio reducido según el provider
- Atributo OTel: `looper.llm.cache.hit=true|false`, `looper.llm.cache.cached_tokens=N`

## 3. Messages & History

El sistema de mensajes es el core de comunicación del framework. Los mensajes son **provider-agnostic**: se definen una sola vez y cada provider los traduce a su formato nativo. El history es serializable, editable y portátil.

### Formato de mensajes

```go
// MessageType identifica el tipo de mensaje
type MessageType string

const (
    MessageSystem    MessageType = "system"
    MessageUser      MessageType = "user"
    MessageAssistant MessageType = "assistant"
    MessageTool      MessageType = "tool"
)

// Message es la representación universal, independiente del provider
type Message struct {
    ID        string            `json:"id"`
    Type      MessageType       `json:"type"`
    Content   string            `json:"content,omitempty"`
    ToolCalls []ToolCall        `json:"tool_calls,omitempty"`
    ToolID    string            `json:"tool_id,omitempty"`
    Name      string            `json:"name,omitempty"`
    Metadata  map[string]any    `json:"metadata,omitempty"`
    CreatedAt time.Time         `json:"created_at"`
}

// ToolCall representa una llamada a tool del assistant
type ToolCall struct {
    ID       string          `json:"id"`
    Name     string          `json:"name"`
    Arguments json.RawMessage `json:"arguments"`
}

// ToolResult es el resultado de ejecutar una tool
type ToolResult struct {
    ToolCallID string `json:"tool_call_id"`
    Content    string `json:"content"`
    IsError    bool   `json:"is_error"`
}
```

### History

```go
// History gestiona la secuencia de mensajes de una conversación
type History struct {
    messages []Message
}

// Operaciones principales
history := NewHistory()

// Añadir mensajes
history.AddUserMessage("analyze this data")
history.AddAssistantMessage("I'll search for that", toolCalls)
history.AddToolResult(toolCallID, result)

// Acceso
messages := history.Messages()          // []Message - solo lectura
last := history.LastMessage()           // *Message
turns := history.TurnCount()            // int - pares user/assistant

// Edición (para hooks y middleware)
history.Update(index, func(m *Message) {
    m.Content = sanitized
})
history.Remove(index)
history.InsertBefore(index, systemMsg)
history.Truncate(maxMessages)
```

### Serialización y persistencia

El history está diseñado para serializarse directamente a JSON y almacenarse en cualquier medio (SQL, NoSQL, Redis, filesystem):

```go
// Serializar a JSON (para persistir en DB)
data, err := history.MarshalJSON()
db.Save(ctx, "conversation:"+sessionID, data)

// Restaurar desde JSON
data := db.Load(ctx, "conversation:"+sessionID)
history, err := UnmarshalHistory(data)

// Inyectar history existente en el agente (ej: reanudar conversación)
agent := NewAgent(provider, systemPrompt, tools...)
result, err := agent.Run(ctx, "continue from where we left off",
    WithHistory(history),
)

// Exportar/importar para debug o auditoría
export := history.Export()
```

### Provider-agnostic

Los mensajes se definen una sola vez en formato universal. Cada `LLMProvider` tiene un `Translator` interno que convierte `[]Message` al formato nativo de su API:

- `OpenAITranslator`: convierte a `openai.ChatCompletionMessageParamUnion`
- `AnthropicTranslator`: convierte a `anthropic.MessageParam` con gestión de content blocks
- `GoogleTranslator`: convierte a `genai.Content`
- El usuario **nunca** interactúa con los formatos nativos; siempre usa `Message` y `History`

### Edición desde middleware

Los hooks y middleware pueden inspeccionar y modificar el history antes/después de cada llamada:

```go
// Hook que filtra contenido sensible antes de enviar al LLM
agent.On("BeforeCall", func(ctx context.Context, params *CallParams) error {
    for i, msg := range params.History.Messages() {
        if msg.Type == MessageUser {
            params.History.Update(i, func(m *Message) {
                m.Content = redactPII(m.Content)
            })
        }
    }
    return nil
})

// Hook que enriquece el history con contexto del turno anterior
agent.On("AfterCall", func(ctx context.Context, params *CallParams) error {
    params.History.AddSystemMessage(fmt.Sprintf("Turn %d completed", params.Turn))
    return nil
})
```

## 4. Bucle Agéntico (Agentic Loop)

- Loop iterable: `agent.Run(ctx)` para ejecución completa, `agent.Iterate(ctx)` para iteración manual con `for`
- **Hooks tipo middleware**: funciones que se ejecutan `BeforeCall`, `AfterCall`, `OnCancel` recibiendo parámetros validados y la función a ejecutar. El usuario decide cuándo y cómo intervenir
- **Hooks a nivel de loop**: `BeforeFinalResponse`, `AfterFinalResponse` para validaciones, retries o modificación del output final
- **Control de turnos**: `maxTurns` y `maxConsecutiveToolRetries` configurables
- **Error como feedback**: si una tool falla, el error se convierte en un tipo de response que el agente recibe para no repetir el error

## 4. Bucle Agéntico (Agentic Loop)

- Loop iterable: `agent.Run(ctx)` para ejecución completa, `agent.Iterate(ctx)` para iteración manual con `for`
- **Hooks tipo middleware**: funciones que se ejecutan `BeforeCall`, `AfterCall`, `OnCancel` recibiendo parámetros validados y la función a ejecutar. El usuario decide cuándo y cómo intervenir
- **Hooks a nivel de loop**: `BeforeFinalResponse`, `AfterFinalResponse` para validaciones, retries o modificación del output final
- **Control de turnos**: `maxTurns` y `maxConsecutiveToolRetries` configurables
- **Error como feedback**: si una tool falla, el error se convierte en un tipo de response que el agente recibe para no repetir el error

### Invocación del agente

```go
// Ejecución completa con resultado final
result, err := agent.Run(ctx, "analyze this codebase",
    WithHistory(existingHistory),     // restaurar conversación previa
    WithMaxTurns(15),                 // override de config
    WithMetadata(map[string]any{      // metadata inyectada en el context
        "user_id": "abc123",
        "session_id": "sess-456",
    }),
)
// result contiene: output, cost, usage, history completo, metadata

// Iteración manual para control fino
iter := agent.Iterate(ctx, "analyze this codebase")
for step := range iter.Next() {
    switch step.Type {
    case StepLLMCall:
        fmt.Printf("LLM: %s\n", step.Content)
    case StepToolCall:
        fmt.Printf("Tool: %s(%s)\n", step.ToolName, step.ToolArgs)
    case StepToolResult:
        fmt.Printf("Result: %s\n", step.Content)
    case StepFinalResponse:
        fmt.Printf("Final: %s\n", step.Content)
        break
    }
}
```

## 5. Structured Output

- Si el tipo de respuesta es `string`, el agente se comporta de forma natural (texto libre)
- Si el tipo de respuesta es un `struct`, el framework inyecta automáticamente una tool `final_response` y agrega instrucciones explícitas en inglés al prompt para que el agente use esa tool con un objeto válido
- Los mensajes de texto finales del agente también se marcan via `final_response`

## 6. Streaming

- Streaming de texto intermedio (pensamiento) y streaming de texto final (capturando el patrón de `final_response` al vuelo)
- Streaming de tool calls en progreso

## 7. Context Tipado

- Las tools reciben un context tipado definido a nivel de tool con generics o a nivel de toolkit
- Usar `context.Context` nativo de Go con typed keys para compartir variables a nivel de flujo (state, config, funciones) sin acoplamiento
- El prompt es una función `func(ctx context.Context) string` que extrae del context lo que necesita

## 8. Definición Modular de Agentes

- Constructor tipo pydantic-ai: `NewAgent(provider, systemPromptFn, tools/toolkits...)`

### System Prompt

El system prompt es una función que recibe el context del agente y retorna un string. Permite construir prompts dinámicos que dependen del contexto de ejecución:

```go
// System prompt como función que extrae datos del context
systemPrompt := func(ctx context.Context) string {
    user := auth.UserFromContext(ctx)
    lang := config.LangFromContext(ctx)
    return fmt.Sprintf(
        "You are an assistant for %s. Respond in %s. Current time: %s",
        user.Name, lang, time.Now().Format(time.RFC3339),
    )
}

// También se puede pasar un string estático directamente
agent := NewAgent(provider, "You are a helpful assistant", tools...)
```

### Skills

Una skill es un struct que implementa la interfaz `Skill` y agrupa una o varias tools con fragmentos de prompt inyectables y funciones auxiliares. A diferencia de un toolkit, una skill puede modificar el system prompt y aporta un contexto temático completo:

```go
// Interfaz que debe implementar cualquier skill
type Skill interface {
    Name() string
    RegisterTools(reg *ToolRegistry)
    PromptFragment() string
}

// Ejemplo: skill de análisis de código
type CodeAnalysisSkill struct {
    Language string
}

func (s *CodeAnalysisSkill) Name() string {
    return "code_analysis"
}

func (s *CodeAnalysisSkill) PromptFragment() string {
    return fmt.Sprintf(
        "You have access to code analysis tools for %s. "+
        "Always analyze code for security issues, performance, and best practices.",
        s.Language,
    )
}

func (s *CodeAnalysisSkill) RegisterTools(reg *ToolRegistry) {
    reg.Register(LintInput{}, s.lint, ToolConfig{
        Name:        "lint_code",
        Description: "Lint code and return issues",
        Parallel:    true,
    })
    reg.Register(AnalyzeInput{}, s.analyze, ToolConfig{
        Name:        "analyze_code",
        Description: "Deep code analysis",
        Parallel:    false,
    })
}

// Uso: las skills se pasan al agente y sus prompt fragments se concatenan
// automáticamente al system prompt
agent := NewAgent(provider, systemPrompt,
    &CodeAnalysisSkill{Language: "Go"},
    &SearchToolkit{APIKey: "..."},
    someStandaloneTool,
)
```

- El framework concatena los `PromptFragment()` de todas las skills al system prompt base, separados por newlines
- Las skills se pasan al constructor del agente junto con toolkits y tools individuales
- Una skill puede compartir estado interno entre sus tools y su prompt fragment

### Toolkits

Un toolkit es un struct que implementa la interfaz `Toolkit` y agrupa tools relacionadas bajo un mismo namespace con context compartido:

```go
// Interfaz que debe implementar cualquier toolkit
type Toolkit interface {
    RegisterTools(reg *ToolRegistry)
}

// Ejemplo: toolkit de búsqueda web
type SearchToolkit struct {
    APIKey string
    RateLimit time.Duration
}

func (tk *SearchToolkit) RegisterTools(reg *ToolRegistry) {
    reg.Register(WebSearchInput{}, tk.webSearch, ToolConfig{
        Name:        "web_search",
        Description: "Search the web for information",
        Parallel:    true,
    })
    reg.Register(NewsSearchInput{}, tk.newsSearch, ToolConfig{
        Name:        "news_search",
        Description: "Search news articles",
        Parallel:    true,
    })
    reg.Register(ImageSearchInput{}, tk.imageSearch, ToolConfig{
        Name:        "image_search",
        Description: "Search for images",
        Parallel:    false,
    })
}

// Uso: el toolkit se pasa al agente como cualquier tool
agent := NewAgent(provider, systemPrompt, &SearchToolkit{APIKey: "..."})
```

- El `ToolRegistry` inyectado en `RegisterTools` permite registrar tools con la misma API funcional que `NewTool`
- El toolkit puede compartir estado interno (API keys, configs, caches) entre todas sus tools
- Los toolkits se pasan directamente al constructor del agente junto con tools individuales

## 9. Memory Management

- Sistema configurable de control de memoria para evitar token overflow
- Estrategias intercambiables: sliding window, summarization, token budget, etc.

## 10. Agent Nesting

- Si un agente lanza otro agente dentro de una tool, la jerarquía debe ser rastreable
- Integrar con OpenTelemetry para que la UI muestre el árbol de llamadas completo

## 11. Observabilidad y Cost Tracking (OpenTelemetry)

La observabilidad es un subsistema central, no un add-on. Cada ejecución de agente genera traces completos que permiten reconstruir exactamente qué pasó, cuándo, por qué y cuánto costó. El **cost tracking** no es un módulo aparte sino que está **integrado nativamente en cada span** del loop agéntico.

### Cost Model Registry

- **Base costs**: el framework incluye un registry con los precios oficiales de los modelos base (OpenAI, Anthropic, Google) por modelo y variante (input/output/cached tokens). Actualizable con `UpdateModelCost(provider, model, inputCostPer1M, outputCostPer1M)`
- **Cached token costs**: cada provider tiene precio diferenciado para cached tokens (ej: OpenAI 50% descuento, Anthropic 90% descuento). El registry los incluye como `cachedCostPer1M`
- **Custom costs**: para modelos no oficiales (Ollama, LMStudio, OpenRouter, llama-server):
  ```go
  agent.WithCustomModelCost("my-local-model", CostConfig{
      InputCostPer1MTokens:   0.0,
      OutputCostPerMTokens:   0.0,
      CachedCostPer1MTokens:  0.0,
  })
  ```

### Jerarquía de spans con costes integrados

Cada invocación de `agent.Run()` o `agent.Iterate()` genera un **trace** raíz. Los costes se acumulan y exponen en **cada nivel** de la jerarquía:

```
[Trace: agent.run] agent_id="my-agent" run_id="uuid"
│ cost.total_usd=0.00847  cost.input_usd=0.00210  cost.output_usd=0.00637  cost.cached_usd=0.00000
│ tokens.total=427  tokens.prompt=210  tokens.completion=217  tokens.cached=0
│ turns_used=2  status="completed"
│
├── [Span: agent.loop.turn] turn=1 max_turns=10
│   │ cost.total_usd=0.00623  cost.input_usd=0.00210  cost.output_usd=0.00413
│   │ tokens.prompt=210  tokens.completion=165
│   │
│   ├── [Span: llm.call] provider="openai" model="gpt-4o" stream=true
│   │   │ cost.total_usd=0.00210  cost.input_usd=0.00210  cost.output_usd=0.00000
│   │   │ tokens.prompt=210  tokens.completion=0  tokens.cached=0
│   │   │ cache.hit=false
│   │   ├── [Event: llm.prompt]        tokens.prompt=210
│   │   ├── [Event: llm.completion]    tokens.completion=165  cost.output_usd=0.00413
│   │   └── [Event: llm.streaming_chunk] chunk_index=0..N
│   │
│   ├── [Span: tool.validate] tool="search" valid=true
│   │
│   ├── [Span: tool.call] tool="search" duration_ms=230  parallel=true
│   │   │ cost.total_usd=0.00000  (tool sin coste LLM directo)
│   │   ├── [Event: tool.input]   args={"query": "golang agents"}
│   │   └── [Event: tool.output]  result="{...}"
│   │
│   └── [Span: hook.execute] hook="AfterCall" phase="tool"
│
├── [Span: agent.loop.turn] turn=2
│   │ cost.total_usd=0.00224  cost.input_usd=0.00000  cost.output_usd=0.00224
│   │ tokens.prompt=375  tokens.completion=52  tokens.cached=375
│   │
│   ├── [Span: llm.call] provider="openai" model="gpt-4o" stream=false
│   │   │ cost.total_usd=0.00224  cost.input_usd=0.00000  cost.output_usd=0.00224
│   │   │ tokens.prompt=375  tokens.completion=52  tokens.cached=375
│   │   │ cache.hit=true  cache.cached_tokens=375  cache.savings_usd=0.00375
│   │   └── [Event: llm.completion] tokens.completion=52
│   │
│   └── [Span: tool.call] tool="final_response"
│       │ cost.total_usd=0.00000
│       └── [Event: tool.output] structured_output=true type="MyResponse"
│
└── [Span: agent.loop.end]
    cost.total_usd=0.00847  cost.input_usd=0.00210  cost.output_usd=0.00637  cost.cached_usd=0.00000
    cost.savings_usd=0.00375  (ahorro por caching)
    tokens.total=427  turns_used=2  status="completed"
```

### Acumulación de costes por nivel

Los costes se calculan y acumulan en **cada nivel de la jerarquía de spans**:

| Nivel | Atributos de coste | Descripción |
|---|---|---|
| `agent.run` (trace raíz) | `looper.cost.total_usd`, `looper.cost.input_usd`, `looper.cost.output_usd`, `looper.cost.cached_usd`, `looper.cost.savings_usd` | Suma total de toda la ejecución. Disponible en `result.Cost` |
| `agent.loop.turn` | mismos atributos de coste | Coste acumulado en ese turno (LLM call + tools indirectas) |
| `llm.call` | `looper.cost.total_usd`, `looper.cost.input_usd`, `looper.cost.output_usd`, `looper.cost.cached_usd`, `looper.llm.cache.hit`, `looper.llm.cache.cached_tokens`, `looper.llm.cache.savings_usd` | Coste exacto de esa llamada LLM con desglose |
| `tool.call` | `looper.cost.total_usd` | Coste directo de la tool (0 si no hace llamadas LLM; si anida otro agente, hereda el coste del sub-trace) |
| `agent.loop.end` | mismos atributos que trace raíz | Span final con el resumen consolidado |

### Formato de traces

- **Protocolo**: OpenTelemetry SDK estándar (`go.opentelemetry.io/otel`), exportable a cualquier backend (Jaeger, Tempo, Datadog, Honeycomb, etc.)
- **Span attributes**: cada span incluye atributos semánticos estandarizados:
  - `looper.agent.id`, `looper.agent.run_id`, `looper.agent.turn`
  - `looper.llm.provider`, `looper.llm.model`, `looper.llm.stream`
  - `looper.tool.name`, `looper.tool.valid`, `looper.tool.duration_ms`, `looper.tool.parallel`
  - `looper.hook.name`, `looper.hook.phase`
  - `looper.hook.type` = `"pause"` | `"resume"` para pause points
  - `looper.cost.*` en todos los niveles (ver tabla arriba)
- **Events (logs estructurados)**: cada span puede emitir eventos con timestamps para prompt tokens, completion tokens, tool input/output, errores, etc.
- **Metrics**: contadores y histogramas por defecto:
  - `looper.agent.runs.total` (counter) con status: completed | error | paused | cancelled
  - `looper.llm.tokens.prompt` y `looper.llm.tokens.completion` (histogram)
  - `looper.llm.tokens.cached` (histogram) para monitorizar efectividad del caching
  - `looper.tool.calls.duration` (histogram) por tool name
  - `looper.tool.calls.errors` (counter) por tool name y error type
  - `looper.cost.total` (histogram) por provider y model — coste por run
  - `looper.cost.savings` (histogram) por provider — ahorro por caching
  - `looper.cost.per_turn` (histogram) — distribución de coste por turno

### Nesting de agentes y costes

- Cuando un agente lanza otro agente dentro de una tool, el trace del agente hijo se anida como child span del `tool.call` del agente padre
- El coste del agente hijo se propaga al `tool.call` del padre, que a su vez propaga al turno y al run del padre
- Atributo `looper.agent.parent_run_id` para rastrear la jerarquía completa
- El coste total de un run padre incluye los costes de todos los agentes hijo anidados

### Configuración

- `WithTelemetry(provider trace.TracerProvider, meter metric.MeterProvider)` como opción del agente
- Si no se configura, usa el provider global de OTel (no-op por defecto, zero overhead)
- `WithTelemetryVerbose()` para incluir en los spans el contenido completo de prompts y completions (útil para debugging, off en producción)
- `WithCustomModelCost(model, CostConfig{...})` para registrar precios de modelos no oficiales

### API de resultado

```go
result, err := agent.Run(ctx, "analyze this data")
fmt.Printf("Cost: $%.6f (input: $%.6f, output: $%.6f, cached: $%.6f)\n",
    result.Cost.TotalUSD, result.Cost.InputUSD, result.Cost.OutputUSD, result.Cost.CachedUSD)
fmt.Printf("Tokens: %d prompt, %d completion, %d cached (saved $%.6f)\n",
    result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CachedTokens, result.Cost.SavingsUSD)
```

## 12. Pause & Resume (Man-in-the-Middle)

El framework implementa un sistema de interrupción y reanudación de flujos que permite pausar la ejecución del bucle agéntico en cualquier punto sin perder estado, interactuar con un actor externo (usuario, sistema, otro servicio), y reanudar exactamente donde se quedó:

- **Pause points configurables**: el agente puede pausarse automáticamente antes de ejecutar una tool crítica, antes de la final_response, o en cualquier hook definido por el usuario
- **Tipos de interacción con el actor externo**:
  - **Confirmación simple**: el actor responde OK/Cancel para permitir o bloquear una acción (ej: antes de ejecutar una tool destructiva)
  - **Solicitud de datos**: el actor proporciona un valor que el agente necesita para continuar (ej: pedir un email faltante, un parámetro no determinado)
  - **Parada manual**: el actor puede detener el flujo en cualquier momento (ej: botón de "stop" en una UI)
- **Serialización completa del estado**: cuando el flujo se pausa, todo el estado (historial de mensajes, tools pendientes, context, variables de flujo) se serializa a un formato persistible (JSON/struct) para poder almacenarse en DB, caché o filesystem
- **Resume desde estado serializado**: el flujo puede reanudarse desde el estado serializado en cualquier momento, incluso en otro proceso o máquina, continuando el bucle agéntico como si nunca se hubiera interrumpido
- **API limpia**: el usuario define pause points declarativamente (ej: `agent.OnTool("delete_file").RequireConfirmation()`, `agent.OnTool("send_email").RequireInput("recipient")`) y el framework gestiona la pausa, la serialización y la reanudación de forma transparente
- **Integración con hooks**: los pause points son un tipo especial de hook que, en lugar de ejecutarse y continuar, delegan al actor externo y esperan su respuesta antes de proseguir
- **Timeout configurable**: cada pause point puede tener un timeout tras el cual el flujo se cancela o toma una acción por defecto

## 13. MCP Integration

- Las tools MCP se integran como tools nativas del framework
- Interface `MCPToolProvider` que convierte MCP tools en tools del framework

## 13. Cost Tracking

El framework incluye un sistema de tracking de costes que calcula el coste real de cada ejecución de agente por modelo y provider:

### Modelo de costes

- **Base costs**: el framework incluye un registry con los precios oficiales de los modelos base (OpenAI, Anthropic, Google) por cada modelo y variante (input/output/cached tokens). Este registry se actualiza con una función `UpdateModelCost(provider, model, inputCostPer1M, outputCostPer1M)`
- **Custom costs**: para modelos no oficiales que usan la API de OpenAI (Ollama, LMStudio, OpenRouter, llama-server, etc.), el usuario puede registrar precios custom:
  ```go
  agent.WithCustomModelCost("my-local-model", CostConfig{
      InputCostPer1MTokens:  0.0,
      OutputCostPer1MTokens: 0.0,
  })
  ```

### Integración con el agente

- Cada `llm.call` reporta al cost tracker los tokens consumidos (prompt, completion, cached si disponibles)
- El coste se acumula a nivel de **run** (ejecución completa), a nivel de **turno**, y a nivel de **tool** (tokens consumidos por las llamadas que esa tool generó indirectamente)
- El coste total está disponible en el resultado del run y en los spans de OpenTelemetry:
  - Atributos: `looper.cost.total_usd`, `looper.cost.input_usd`, `looper.cost.output_usd`
  - Metric: `looper.cost.total` (histogram) por provider y model

### API

```go
result, err := agent.Run(ctx, "analyze this data")
fmt.Printf("Cost: $%.6f (input: %d tokens, output: %d tokens)\n",
    result.Cost.TotalUSD, result.Usage.InputTokens, result.Usage.OutputTokens)
```

# Restricciones

- **Go idiomático**: interfaces pequeñas, composición sobre herencia, errores como valores
- **Sin dependencias pesadas**: solo las librerías de provider, jsonschema, y OpenTelemetry
- **Thread-safe**: el framework debe ser seguro para uso concurrente
- **Zero opinions por defecto**: cada decisión opinada debe poder sobreescribirse
- **Documentación**: cada paquete debe tener doc comments completos

# Entregables esperados

1. **Diseño de arquitectura** — Diagrama de paquetes, interfaces principales, y flujo de datos del bucle agéntico
2. **Esqueleto del proyecto** — Estructura de directorios `pkg/` con todas las interfaces definidas
3. **MVP funcional** — Implementación completa de: provider abstraction (OpenAI), tool schema con jsonschema, bucle agéntico con hooks, structured output, streaming, y context tipado
4. **Ejemplos** — Mínimo 3 ejemplos testados: agente básico, agente con structured output, agente con tools y streaming
5. **Tests** — Table-driven tests para los subsistemas clave

# Plan de ataque

Ejecuta en este orden:

1. **Fase 1**: Diseña la arquitectura completa (interfaces, structs, flujo del loop). Presenta el diseño antes de implementar
2. **Fase 2**: Crea el esqueleto del proyecto con todas las interfaces vacías
3. **Fase 3**: Implementa el MVP: provider OpenAI, tool schema, bucle agéntico, structured output, streaming
4. **Fase 4**: Añade hooks, context tipado, memory management básico
5. **Fase 5**: Ejemplos, tests y documentación `docs/` en markdown

**Comienza por la Fase 1.** Presenta el diseño de arquitectura completo y espera validación antes de pasar a la implementación.

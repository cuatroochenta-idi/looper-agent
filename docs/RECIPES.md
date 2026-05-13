# Recipes

Copy-pasteable patterns for the most common production scenarios. Every
recipe assumes you've imported:

```go
import (
    "context"
    "encoding/json"
    "time"
    "github.com/cuatroochenta-idi/looper-agent/looper"
    "github.com/cuatroochenta-idi/looper-agent/loop"
    "github.com/cuatroochenta-idi/looper-agent/memory"
    "github.com/cuatroochenta-idi/looper-agent/message"
    "github.com/cuatroochenta-idi/looper-agent/pause"
    "github.com/cuatroochenta-idi/looper-agent/provider"
    "github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
    "github.com/cuatroochenta-idi/looper-agent/provider/openai"
    "github.com/cuatroochenta-idi/looper-agent/tool"
)
```

## Table of contents

- [1. Reliable structured output for an API endpoint](#1-reliable-structured-output-for-an-api-endpoint)
- [2. Phase-machine agent (research → publish)](#2-phase-machine-agent-research--publish)
- [3. Loop detector — stop the model spamming the same tool](#3-loop-detector--stop-the-model-spamming-the-same-tool)
- [4. Human-in-the-loop approval for destructive tools](#4-human-in-the-loop-approval-for-destructive-tools)
- [5. Multi-modal: image → typed structured output](#5-multi-modal-image--typed-structured-output)
- [6. Cheap fallback when the primary provider is down](#6-cheap-fallback-when-the-primary-provider-is-down)
- [7. Anthropic prompt caching for a 30 KB system prompt](#7-anthropic-prompt-caching-for-a-30-kb-system-prompt)
- [8. Multi-tenant chat with typed deps and cost caps](#8-multi-tenant-chat-with-typed-deps-and-cost-caps)
- [9. Long conversations: keep history bounded](#9-long-conversations-keep-history-bounded)
- [10. Wrap MCP servers as native tools](#10-wrap-mcp-servers-as-native-tools)

---

## 1. Reliable structured output for an API endpoint

You expose `POST /classify` and need the LLM's verdict in a typed Go
struct — no string parsing, no "occasionally returns markdown", no
silent invalid scores.

```go
type Classification struct {
    Label      string   `json:"label" jsonschema:"enum=spam|ham|unknown,required"`
    Confidence float64  `json:"confidence" jsonschema:"minimum=0,maximum=1,required"`
    Reasons    []string `json:"reasons" jsonschema:"description=Short bullet reasons"`
}

func newClassifier() *looper.Agent {
    return looper.MustNewAgent(
        openai.NewProvider(os.Getenv("OPENAI_API_KEY"),
            openai.WithModel("gpt-4o-mini")),
        "You are a spam classifier. Be conservative.",
        looper.WithStructuredOutput[Classification](),
        looper.WithOutputRetries(2),
        looper.WithOutputValidator(func(c Classification) error {
            if c.Label == "unknown" && c.Confidence > 0.5 {
                return looper.ErrOutputInvalid(
                    "If label is 'unknown', confidence must be ≤ 0.5")
            }
            return nil
        }),
        looper.WithUsageLimits(loop.UsageLimits{MaxRequests: 5, MaxUSD: 0.01}),
    )
}

func Classify(ctx context.Context, text string) (Classification, error) {
    res, err := newClassifier().Run(ctx, "Classify: "+text)
    if err != nil {
        return Classification{}, err
    }
    if res.Status != "completed" {
        return Classification{}, fmt.Errorf("classifier failed: %s", res.Status)
    }
    var out Classification
    return out, looper.Decode(res, &out)
}
```

**What you get for free**: schema-driven validation on every candidate,
auto-retry with the validator's error as the LLM hint, cost cap so a
runaway tool loop can't bankrupt the endpoint, status field for clean
error handling on the caller side.

---

## 2. Phase-machine agent (research → publish)

Hide tools that don't make sense yet. Pair with `ToolChoice` to force
the right move on each phase.

```go
search    := buildSearch()
summarise := buildSummarise()
publish   := buildPublish()
done      := buildComplete()

phaseFn := func(_ context.Context, h *message.History) []*tool.Tool {
    summarised := false
    published  := false
    for _, m := range h.Messages() {
        for _, tc := range m.ToolCalls {
            switch tc.Name {
            case "summarise":
                summarised = true
            case "publish":
                published = true
            }
        }
    }
    switch {
    case !summarised:
        return []*tool.Tool{search, summarise}
    case !published:
        return []*tool.Tool{publish}
    default:
        return []*tool.Tool{done}
    }
}

agent := looper.MustNewAgent(p,
    `You are a publication agent. Follow the steps the available tools
    suggest. Don't claim you've published until you actually called publish.`,
    looper.WithDynamicTools(phaseFn),
    looper.WithToolChoice(provider.ToolChoiceRequired()), // never pure-text reply
    looper.WithMaxTurns(20),
)
```

---

## 3. Loop detector — stop the model spamming the same tool

The LLM sometimes gets stuck calling the same tool with the same args.
Cancel the third repeat with a corrective hint:

```go
type seenKey struct{ name, args string }

func newLoopDetector() loop.ToolCallHook {
    counts := make(map[seenKey]int)
    var mu sync.Mutex
    return func(_ context.Context, p *loop.ToolExecutionParams) error {
        mu.Lock()
        defer mu.Unlock()
        for _, c := range p.Calls {
            k := seenKey{c.Name, string(c.Arguments)}
            counts[k]++
            if counts[k] >= 3 {
                p.Cancel(c.ID, fmt.Sprintf(
                    "You've called %s with the same arguments %d times. "+
                        "That input isn't yielding new info — change strategy.",
                    c.Name, counts[k]))
            }
        }
        return nil
    }
}

agent.OnBeforeToolExecution(newLoopDetector())
```

Per-run state stays in the closure's `counts` map; if you run this
hook on a shared `Agent` across goroutines, scope the map per
`RequestID` (see Recipe 8).

---

## 4. Human-in-the-loop approval for destructive tools

`send_email` and `publish_pages` need a human to click "ok" before they
fire.

```go
pm := pause.NewPauseManager()
pm.SetPausePoint("send_email",   pause.PauseToolConfirm, 10*time.Minute)
pm.SetPausePoint("publish_pages", pause.PauseToolConfirm, 10*time.Minute)

agent := looper.MustNewAgent(p, sysPrompt,
    sendEmailTool, publishPagesTool, harmlessTool,
    looper.WithAgentPause(pm),
)

// In the HTTP/UI layer, when the human clicks approve:
//   pm.Resume(&pause.PauseResponse{RequestID: pendingCallID, Action: "ok"})
// or reject:
//   pm.Resume(&pause.PauseResponse{RequestID: pendingCallID, Action: "cancel"})

go agent.Run(ctx, userInput)
```

`RequestID` is the tool call ID — the framework auto-routes the Resume
to the right waiter, so concurrent agent runs can pause / resume
independently on the same `PauseManager`.

---

## 5. Multi-modal: image → typed structured output

Send an image, get back a typed annotation.

```go
type Annotation struct {
    Objects    []string `json:"objects" jsonschema:"description=Objects visible in the image,required"`
    DominantHue string  `json:"dominant_hue" jsonschema:"enum=red|green|blue|yellow|other"`
    Confidence float64  `json:"confidence" jsonschema:"minimum=0,maximum=1"`
}

agent := looper.MustNewAgent(
    openai.NewProvider(os.Getenv("OPENAI_API_KEY"),
        openai.WithModel("gpt-4o-mini")),
    "You are a vision annotator. Be specific.",
    looper.WithStructuredOutput[Annotation](),
    looper.WithOutputRetries(2),
)

hist := message.NewHistory()
hist.AddUserMessageParts(
    message.TextPart("Annotate this image."),
    message.ImageURLPart("https://example.com/cat.png"),
)
res, _ := agent.Run(ctx, "", looper.WithHistory(hist))

var out Annotation
_ = looper.Decode(res, &out)
```

For inline (base64) image bytes use `message.ImagePart(mimeType, data)`.
For PDFs / documents: `message.FilePart(name, mime, data)`.

---

## 6. Cheap fallback when the primary provider is down

Two-tier strategy: retry-with-circuit-breaker on the primary, fall over
to a secondary provider when the breaker is open.

```go
primary := provider.NewRetryProvider(
    anthropic.NewProvider(os.Getenv("ANTHROPIC_API_KEY")),
    provider.RetryConfig{
        MaxAttempts:             3,
        CircuitFailureThreshold: 5,
        CircuitCooldown:         30 * time.Second,
    },
)
secondary := provider.NewRetryProvider(
    openai.NewProvider(os.Getenv("OPENAI_API_KEY"),
        openai.WithModel("gpt-4o-mini")),
    provider.RetryConfig{MaxAttempts: 2},
)

queue := provider.NewProviderQueue(primary, secondary)
agent := looper.MustNewAgent(queue, sysPrompt)
```

`ProviderQueue.Execute` walks the queue in order until one succeeds.
Wire it into your own dispatcher when you need finer control (sticky
sessions, per-tenant routing, etc.).

---

## 7. Anthropic prompt caching for a 30 KB system prompt

The system prompt is 30 KB and stable across every session. Two-line
config saves you most of the input cost:

```go
ant := anthropic.NewProvider(os.Getenv("ANTHROPIC_API_KEY"),
    anthropic.WithModel("claude-sonnet-4-..."),
    anthropic.WithCacheBreakpoints(
        anthropic.CacheSystemPrompt,   // cache the system block
        anthropic.CacheTools,          // and the tool list
    ),
)

agent := looper.MustNewAgent(ant, biggie30KBPrompt, lotsOfTools...)
```

On the second + every subsequent call to the same `agent`, Anthropic
returns `cache_read_input_tokens` and bills them at ~10% of fresh
tokens. Monitor `res.Cost.CachedTokens` and `res.Cost.SavingsUSD` to
see the hit rate.

---

## 8. Multi-tenant chat with typed deps and cost caps

One `*Agent` instance, N concurrent end-user sessions, each with its own
DB scope and budget.

```go
type SessionDeps struct {
    DB        *sql.DB
    UserID    string
    Tier      string  // "free" | "pro"
}

agent := looper.MustNewAgent(p, sysPrompt, lookUpRecord, summariseEmails)

func ServeSession(w http.ResponseWriter, r *http.Request) {
    user := authenticate(r)
    deps := SessionDeps{DB: db, UserID: user.ID, Tier: user.Tier}

    ctx := looper.WithRunDeps(r.Context(), deps)

    res, err := agent.Run(ctx, r.FormValue("question"),
        looper.WithMaxTurns(20),
        looper.WithMetadata(map[string]any{"user_id": user.ID}),
    )
    if err != nil { http.Error(w, err.Error(), 500); return }

    // Tier-specific cost guard via the agent option works too — this
    // illustrates the per-run pattern with a one-off check.
    if res.Cost.TotalUSD > user.RemainingBudget {
        // ... bill / freeze / etc
    }

    fmt.Fprint(w, res.Output)
}

// Inside lookUpRecord's body:
func lookUpRecordBody(ctx context.Context, in LookUpIn) (string, error) {
    deps, ok := looper.Deps[SessionDeps](ctx)
    if !ok {
        return "", errors.New("missing deps")
    }
    return queryDB(deps.DB, deps.UserID, in.RecordID)
}
```

The 50-concurrent stress test (`concurrency_test.go`) verifies this
pattern under `-race`. Per-run state never leaks across goroutines.

---

## 9. Long conversations: keep history bounded

For an end-user chat that may run hundreds of turns, attach a memory
strategy. A cheap summariser uses `gpt-4o-mini` to compact older
messages:

```go
summariserProv := openai.NewProvider(os.Getenv("OPENAI_API_KEY"),
    openai.WithModel("gpt-4o-mini"),
    openai.WithMaxTokens(400),
)

summariseFn := func(ctx context.Context, msgs []message.Message) (string, error) {
    // Build a single user message listing the older conversation, ask
    // gpt-4o-mini for a 200-word summary, return its content.
    transcript := renderTranscript(msgs)
    h := message.NewHistory()
    h.AddUserMessage("Summarise this conversation in ≤200 words:\n\n" + transcript)
    resp, err := summariserProv.Chat(ctx, provider.LLMRequest{
        Messages: h.Messages(),
    })
    if err != nil {
        return "", err
    }
    return resp.Content, nil
}

agent := looper.MustNewAgent(mainProvider, sysPrompt,
    looper.WithAgentMemory(memory.NewSummarizer(summariseFn,
        memory.WithKeepLast(8),
        memory.WithSummaryPrompt("[Earlier conversation summary]"),
    )),
)
```

If you don't want summarisation, `memory.SlidingWindow{MaxMessages: 30}`
is the cheapest option. For raw structural truncation in your own code,
use `history.TruncateByTurns(n)` — it never splits tool_use ↔
tool_result pairs.

---

## 10. Wrap MCP servers as native tools

You ship binaries that speak MCP (filesystem-server, github-server,
…). Register them on an agent in one call:

```go
import "github.com/cuatroochenta-idi/looper-agent/mcp"

fsTools, err := mcp.NewStdioToolProvider(ctx,
    "mcp-filesystem", nil,
    "--root", "/Users/me/projects")
if err != nil { return err }
defer fsTools.Close()

githubTools, err := mcp.NewStdioToolProvider(ctx,
    "mcp-github", []string{"GITHUB_TOKEN=" + os.Getenv("GITHUB_TOKEN")})
if err != nil { return err }
defer githubTools.Close()

components := make([]any, 0)
for _, t := range fsTools.Tools()    { components = append(components, t) }
for _, t := range githubTools.Tools(){ components = append(components, t) }

agent := looper.MustNewAgent(p, sysPrompt, components...)
```

Every MCP tool becomes a native `*tool.Tool` — schema preserved,
arguments validated, error results surface as Go errors. They're
indistinguishable from hand-written tools to the rest of the framework.

---

## When NOT to use a feature

A few quick "skip this" guidelines so you don't over-engineer:

- **Don't set `WithOutputRetries` unless you have a typed schema** — without `WithStructuredOutput[T]` it's a no-op.
- **Don't use `TurnValidator` for output-shape validation** — that's what `WithOutputValidator[T]` is for. Use `TurnValidator` for process invariants ("always call publish before complete_prd").
- **Don't share `PauseManager` across unrelated agents** — it's cheap to create one per logical workflow.
- **Don't reach for `MCP` for in-process tools** — write them directly with `tool.NewTool`.
- **Don't enable `WithIncludeReasoning(true)` in production** — reasoning chunks are noisy and not billed at the same rate. Turn it on only for debugging.
- **Don't put credentials in deps via `WithRunDeps`** — deps survive in `context.Context` through every tool call. Use it for IDs and handles; keep secrets in env vars / a secrets manager and read them at provider construction.

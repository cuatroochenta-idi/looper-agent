# Looper Agent — examples

Self-contained Go programs that exercise the framework from the simplest call
all the way to multi-provider, hook-driven, persisted-history runs. Each one
is a single `main.go` with no extra dependencies beyond the parent module.

## Setup

```bash
# 1. From the repo root, copy or edit `.env.local` (already created for you,
#    gitignored). Fill in real keys.
$EDITOR .env.local

# 2. Load it into the current shell so the examples see the keys.
set -a && source .env.local && set +a

# 3. Verify the framework builds.
go build ./...
```

`.env.local` ships with sensible defaults for OTel (`LOOPER_OTEL_ENABLED=true`,
endpoint `localhost:4317`, `LOOPER_OTEL_VERBOSE=true`) so the debug UI works
out of the box once a collector is reachable.

## The examples

| # | Path | What it shows |
|---|------|---------------|
| 01 | `01_basic` | Smallest possible agent — provider + system prompt + one user input |
| 02 | `02_structured` | Typed Go struct as the response — the framework injects a `final_response` tool and validates |
| 03 | `03_tools_streaming` | Multiple tools (parallel + sequential) consumed via the `Iterate` step iterator |
| 04 | `04_multi_provider` | Same agent, swappable provider — pick via `LOOPER_PROVIDER=openai\|anthropic\|google` |
| 05 | `05_hooks_lifecycle` | Registers every hook type (`BeforeCall`, `AfterCall`, `BeforeFinalResponse`, `AfterFinalResponse`, `OnCancel`) |
| 06 | `06_skill_and_toolkit` | `CalculatorToolkit` (tools only) + `TranslatorSkill` (tools **and** a prompt fragment) |
| 07 | `07_history_resume` | Persists `*message.History` to disk, reloads it via `looper.WithHistory`, continues the conversation |
| 08 | `08_presentation_builder` | Long, multi-tool agent that drafts a slide deck — exercises history growth and parallel tool calls |
| 09 | `09_pause_resume` | Pause-point gating: snapshot a run, resume from disk, finish the conversation |
| 10 | `10_nested_agents` | A parent agent that delegates to child agents via a `delegate` tool — composition pattern |
| 11 | `11_dev_cli` | Driving the framework from `cmd/looper` (debug CLI, web UI, MCP) end-to-end |
| 12 | `12_multimodal` | Multi-modal input — text + image parts via `NewUserMessageWithParts` and `WithHistory` |
| 13 | `13_turn_validator` | `WithTurnValidatorFunc` rejecting short replies with a corrective Hint and a retry budget |
| 14 | `14_dynamic_tools` | `WithDynamicTools` phase machine — hide / reveal tools per turn based on history |
| 15 | `15_before_tool_hook` | `OnBeforeToolExecution` loop-detector — `params.Cancel(callID, reason)` on the 4th identical call |
| 16 | `16_history_truncate` | `message.History.TruncateByTurns(n)` — tool-pair-aware pruning, no LLM call needed |

Run any of them with:

```bash
go run ./examples/04_multi_provider
go run ./examples/05_hooks_lifecycle
go run ./examples/06_skill_and_toolkit
go run ./examples/07_history_resume
```

(or `go run examples/04_multi_provider/main.go` — both work.)

---

## Debug CLI flows

The framework ships a `looper` CLI in `cmd/looper` for live introspection.
Build it once:

```bash
go build -o ./bin/looper ./cmd/looper
./bin/looper version
```

It exposes three entry points:

```
looper serve [--port 9090] [--otel-endpoint :4317]   # web UI + dashboard
looper run   <main.go> [--args '…']                  # run a Go program in debug mode
looper mcp                                           # MCP debug server over stdio
```

### Flow A — watch a run live in the web UI

`looper serve` boots an htmx + SSE dashboard at <http://localhost:9090>:

```bash
# terminal 1: start the UI
./bin/looper serve --port 9090

# terminal 2: hit it from the form, or post the input via the API
curl -s -X POST localhost:9090/api/run --data-urlencode 'input=hello'
```

Routes worth knowing:

| Route | What you get |
|-------|--------------|
| `GET /`                 | Dashboard (run history, totals) |
| `GET /runs`             | List of runs |
| `GET /runs/{id}`        | Per-run detail |
| `GET /live`             | Live console |
| `GET /live/{id}`        | Live console scoped to one run |
| `POST /api/run`         | Kick off a run (form field `input=…`) |
| `GET /api/runs`         | All runs as JSON |
| `GET /api/costs`        | Aggregate cost / token totals |
| `GET /api/stream/{id}`  | SSE stream of step events for one run |

To pipe a long-running session straight into the SSE stream:

```bash
curl -N http://localhost:9090/api/stream/<runID>
```

### Flow B — run an example with debug instrumentation

```bash
# load the env
set -a && source .env.local && set +a

# any example, with LOOPER_DEBUG / LOOPER_OTEL_VERBOSE already on
go run ./examples/05_hooks_lifecycle
```

`05_hooks_lifecycle` prints one line per loop turn for every hook, so the CLI
output is already a step-by-step trace:

```
[BeforeCall] turn=0  history.len=1  prompt_len=84
[AfterCall]  turn=0  history.len=4
[BeforeCall] turn=1  history.len=4  prompt_len=84
...
[BeforeFinal] turn=1 — about to return final output
[AfterFinal]  turn=1 — final output delivered
```

If you want the same view through `cmd/looper run` (instead of `go run`), use:

```bash
./bin/looper run ./examples/05_hooks_lifecycle/main.go
```

> The `run` subcommand is a thin wrapper that sets `LOOPER_DEBUG=true` and
> `LOOPER_OTEL_VERBOSE=true` before invoking `go run`. The implementation is in
> [`cmd/looper/run.go`](../cmd/looper/run.go).

### Flow C — feed an OTel collector

The framework's `telemetry` package emits spans for every loop turn, every LLM
call and every tool execution. To see them, point an OTLP/gRPC collector at the
endpoint already configured in `.env.local`:

```bash
# Local one-liner: Jaeger all-in-one with OTLP enabled
docker run --rm -p 4317:4317 -p 16686:16686 \
  -e COLLECTOR_OTLP_ENABLED=true \
  jaegertracing/all-in-one:latest

# Then run any example…
go run ./examples/05_hooks_lifecycle

# …and open the UI
open http://localhost:16686
```

Relevant env vars (all already in `.env.local`):

| Var | Default | Effect |
|-----|---------|--------|
| `LOOPER_OTEL_ENABLED`  | `false` | Master switch (off → no-op tracer, zero overhead) |
| `LOOPER_OTEL_ENDPOINT` | `localhost:4317` | OTLP gRPC endpoint |
| `LOOPER_OTEL_INSECURE` | `true`  | Disable TLS for local collectors |
| `LOOPER_OTEL_VERBOSE`  | `false` | Include full prompt + completion text in spans (dev-only) |
| `LOOPER_DEBUG`         | `false` | Free-form debug toggle picked up by the CLI |

### Flow D — drive the framework over MCP

`looper mcp` serves the four debug tools (`looper_run`,
`looper_analyze_trace`, `looper_replay`, `looper_list_history`) over JSON-RPC
on stdio, ready to be wired into any MCP-aware client (Claude Code, Cursor,
Zed, …). Quick smoke test:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | ./bin/looper mcp
```

You should see the four tools and the `looper://runs` / `looper://costs`
resource pointers in the response.

---

## Cheat sheet

```bash
# Load env + verify build
set -a && source .env.local && set +a && go build ./...

# Smallest possible roundtrip
go run ./examples/01_basic

# Same agent, swap the vendor
LOOPER_PROVIDER=anthropic go run ./examples/04_multi_provider
LOOPER_PROVIDER=google    go run ./examples/04_multi_provider

# Web UI + live SSE
./bin/looper serve --port 9090
# → open http://localhost:9090

# Local OTel collector (Jaeger all-in-one)
docker run --rm -p 4317:4317 -p 16686:16686 \
  -e COLLECTOR_OTLP_ENABLED=true jaegertracing/all-in-one:latest

# MCP debug server (stdio)
./bin/looper mcp
```

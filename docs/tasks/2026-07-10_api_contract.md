# Panel API + SSE contract (v1) — 2026-07-10

Contract between the Go server (`internal/web`) and the SolidJS UI (`ui/`).
Both sides implement against THIS doc. Field names are snake_case JSON.

## Conventions

- `kind`: `"run"` when `parent_run_id == ""`, else `"subagent"`. Server derives
  it; clients never guess.
- Costs: `self_usd` = the run's own LLM spend. `subtree_usd` = self + all
  descendants (memoized rollup). Aggregate endpoints (`/api/state/summary`,
  `/api/state/costs`) sum **self costs only** — never both, no double counting.
  - Server interpretation (resolved ambiguity, 2026-07-10): the monetary/token
    totals (`total_usd`, `total_tokens`, `cost_estimated`, plus `costs.by_model`)
    are the sum of **every run's self figure** (each run counted exactly once —
    identical to summing `subtree_usd` over top-level runs, but robust to
    orphans). The **counts** (`total_runs`, `running`, `completed`, `errored`,
    `unknown`, `avg_turns`) cover **top-level runs only**, where "top-level" =
    `parent_run_id == ""` OR the parent is not in the store (orphan fallback).
- `cost_estimated`: true when any contributing call was priced from tables
  instead of API-reported cost. UI renders `~$0.1234`.
- Timestamps RFC3339Nano. Token fields: `input_tokens`, `output_tokens`,
  `cached_tokens` (cache reads), `cache_write_tokens`.

## REST (all under auth when enabled, except /api/login; /ingest uses bearer token)

| Method+Path | Response |
|---|---|
| `GET /api/state/summary?since=` | `{total_runs, running, completed, errored, unknown, total_usd, cost_estimated, total_tokens, avg_turns}` (top-level runs only) |
| `GET /api/state/runs?since=&status=&q=` | `{runs: [RunListItem]}` — FLAT list incl. subagents; client builds trees via `parent_run_id`/`parent_tool_call_id` |
| `GET /api/state/runs/{id}` | `RunDetail` |
| `GET /api/state/chats?since=` | `{chats: [ChatSummary]}` |
| `GET /api/state/chats/{key}?since=` | `{chat: ChatSummary, messages: [ChatMessage]}` |
| `GET /api/state/costs?since=` | `{total_usd, cost_estimated, by_model: [ModelCost]}` |
| `POST /api/run` `{input}` | `{id}` |
| `POST /api/login` `{username?, password}` | 204 + `looper_session` cookie (HMAC) |
| `POST /api/logout` | 204 |
| `GET /api/me` | `{auth_enabled, authenticated, username?}` |
| `POST /ingest` | unchanged TraceEvent wire; `Authorization: Bearer <ingest_token>` when auth on |

### Shapes

RunListItem: `{id, session_id, parent_run_id?, parent_tool_call_id?, kind,
project?, input_preview, output_preview?, status, turns, started_at,
ended_at?, last_seen_at, self_usd, subtree_usd, cost_estimated, tokens,
input_tokens, output_tokens, cached_tokens, cache_write_tokens,
subagent_count, subagents_running, models: ["anthropic/claude-…"], fallback_calls}`

RunDetail: RunListItem + `{system_prompt?, input, output, error?, providers:
[ProviderStat], turns_detail: [Turn], child_ids: [id]}` where
Turn: `{turn, provider, model, fallback, api_key_suffix?, assistant_text?,
reasoning?, usage?{...}, tool_calls: [{id, name, args_json, result?{content,
is_error, at}, spawned_run_ids: [id]}], final?, error?, started_at, ended_at}`
(Subagent content is NOT inlined — UI fetches `/api/state/runs/{child_id}`
lazily per expansion; keeps payloads small.)

ChatSummary: `{key, title, project?, started_at, last_seen_at, message_count,
total_usd, cost_estimated, running}` — key = root ancestor session_id||run_id.
ChatMessage: `{run_id, role: "user"|"agent", content, status, streaming,
subagent_count, subagents_running, usd, cost_estimated, at}` — subagent runs
NEVER produce their own messages when their parent is known.

ModelCost: `{provider, model, calls, input_tokens, output_tokens,
cached_tokens, cache_write_tokens, usd, estimated}`

## SSE — `GET /api/events?topics=a,b,c`

Topics: `runs` (list-level changes), `chats`, `run:{id}` (per-run detail),
`summary`. One connection, multiplexed; heartbeat comment every 25s.

Events (SSE `event:` + JSON `data:`):
- `runs_changed {}` — coarse; client refetches its runs list (debounced).
- `run_updated {id, parent_run_id?, kind, status, last_seen_at, self_usd,
  subtree_usd, turns, tokens}` — small delta for list rows + summary tiles.
- `step_appended {run_id, step: {kind, turn, content?, tool_name?,
  tool_call_id?, err?, at, usage?, provider?, model?}}` — persisted steps only.
- `chunk {run_id, turn, kind: "text"|"reasoning", delta}` — LIVE ONLY,
  transient, never persisted; emitted only to `run:{id}` subscribers when an
  in-process runner streams. UI appends to the live buffer.
- `chats_changed {}` — coarse chat list/thread refetch (debounced).

Rules: no HTML over the wire; no full-state re-push per event; server never
persists chunk events (memory or disk); a client that missed events just
refetches snapshots (every event is safe to drop).

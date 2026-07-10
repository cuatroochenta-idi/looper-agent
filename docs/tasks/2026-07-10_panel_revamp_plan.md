# Panel revamp — SolidJS UI, precise costs, Postgres, auth (2026-07-10)

Working plan for the full revamp requested 2026-07-10. Phases are ordered so the
backend contract stabilizes before the new frontend consumes it.

## Phase A — Cost precision (telemetry/, provider/*, loop/)

Cascade (in order):
1. **API-reported cost/usage** — authoritative when present, success OR failure.
2. **Estimate from pricing** — pricing source = custom dict (config) > built-in matrix.
3. **$0 + one-time warn** only when neither exists.

Fixes:
- `provider/anthropic`: streaming currently reports ZERO usage. Capture
  `message_start.usage` (input + cache_creation + cache_read) and
  `message_delta.usage` (cumulative output); attach to final chunk AND to
  error chunks (partial usage on mid-stream failure).
- Add `CacheWriteTokens` to `provider.Usage` / `telemetry.Usage` and
  `CacheWriteCostPer1MTokens` to `CostConfig` (Anthropic bills 1.25×).
- `provider/google`: don't discard accumulated usage on stream error; capture
  `ThoughtsTokenCount` into output/reasoning tokens.
- `provider/openai`: attach partial usage on error when available.
- `loop`: record `chunk.Usage` whenever non-nil (not only `IsFinal`); record
  usage carried by error chunks before emitting `StepError`.
- `telemetry.CostModel`: custom overrides become a real dict —
  `WithCustomCosts(map[string]CostConfig)` + family-prefix matching for custom
  entries too; custom takes precedence over matrix during estimation; when API
  cost present, split components using whatever pricing is available (custom >
  matrix), keep TotalUSD authoritative.
- Guard `nonCachedInput < 0`; keep float64 but round exposed USD to 1e-8.
- Config file (Phase C) feeds the dict: `model_costs` map.

## Phase B — Subagents as first-class children

- Already have `ParentRunID`/`ParentToolCallID` on TraceEvent/RunRecord.
- Add explicit `kind: "subagent" | "run"` derived server-side (ParentRunID != "")
  in all JSON API payloads so the UI never guesses.
- `/api/costs` + dashboard: count self-costs once; expose `self_usd` vs
  `subtree_usd` explicitly (no double counting in tracker).
- Chats/runs lists: subagents NEVER appear top-level when parent known; orphan
  fallback stays.
- Nested example: parent's RunResult should expose aggregated child usage via
  the web rollup (framework: child cost still on child run; attribution by
  rollup, not mutation).

## Phase C — Storage interface + Postgres (Atlas) + config + auth

- Extract persistence seam: keep in-memory `Store` as the hot cache; move disk
  I/O behind `Persistence` interface: `SaveRun(*RunRecord)`, `LoadRuns() ([]*RunRecord, error)`, `Close()`.
  - `folder` impl = current `.looper/` JSON files.
  - `postgres` impl = pgx; runs table (jsonb steps) + versioned schema.
- Atlas: `internal/store/postgres/migrations/` (atlas-generated, committed,
  `atlas.sum`), embedded via go:embed, auto-applied at startup (versioned
  migrator honoring atlas file naming). Makefile target `db-diff` uses atlas
  CLI to generate new migrations from `schema.sql`.
- CLI: `looper serve --db postgres://…` (or `LOOPER_DB`); default remains
  `--store .looper`.
- Config file `looper.json` (project root, optional; `--config` flag):
  `{ "port", "db", "store_dir", "auth": {"username","password"}, "model_costs": {"provider/model": {input,output,cached,cache_write}} }`.
- Auth: when `auth` set (or env `LOOPER_AUTH_PASSWORD`), login page + HMAC-signed
  session cookie; `/ingest` protected by bearer token (`auth.ingest_token`,
  auto-generated if empty and printed at boot). Off by default for local dev.

## Phase D — SolidJS frontend (ui/) + embed

- `ui/`: Bun + Vite + SolidJS + TS + Tailwind v4; shadcn-style hand-rolled
  components (Button/Card/Badge/Tabs/Dialog/Tooltip/Table) on CSS vars.
- Keep visual essence: dark default `#0a0d12` bg, indigo accent `#7c8df8`,
  Inter + JetBrains Mono, status color rails, calm live pulses; shadcn polish
  (radius, borders, muted palette, consistent spacing).
- Pages: Dashboard (stats + recent), Chats (conv list / thread / trace panel),
  Traces (sidebar tree + detail), Login.
- Data: new JSON API (`/api/state/*`) + **typed JSON SSE** (`/api/events?topics=…`)
  replacing datastar HTML patches. Events: `runs_changed`, `run:{id}` (step
  appended / status), `chats_changed` — payloads are small deltas + revision;
  client refetches snapshots on gap. No HTML over the wire.
- Server: templ/datastar removed; `internal/web` keeps store/ingest/rollup/
  timeline as JSON view-model builders; SPA served from `go:embed ui/dist`
  with index fallback; `LOOPER_UI_DEV` proxies to Vite dev server.
- Makefile: `ui-install`, `ui-dev`, `ui-build` (bun), `build` depends on
  `ui-build`; CI-friendly (skip if dist present).

## Phase E — Server-panel example + verify

- `examples/20_server_panel/`: embeds `web.NewServer` in user code, auth
  enabled via config, agents in-process reporting to the panel; README +
  Dockerfile sketch for prod distribution.
- Verify: `make check`, `bun run build`, drive panel end-to-end (runs, chats,
  subagent nesting, costs, SSE), Postgres path with dockerized PG + atlas
  migration apply.

## Key facts from exploration (do not re-derive)

- UI today: templ v0.3.1020 + datastar SSE (HTML fragment patches), ~5.1k LOC
  hand-written in `internal/web`, no npm/no embed, CDN datastar. htmx.min.js is
  dead code.
- Store: concrete `internal/web.Store` (in-mem slice) + one JSON file per run
  in `.looper/` written on run_end (`writeRunFile`), chunks stripped
  (`stripChunkSteps`). No interface seam. No SQL deps in go.mod.
- CLI: stdlib flag; `looper serve --port --store [-- child cmd]`; config =
  env vars only; zero auth today.
- Wire: `looper/tracer.go` POSTs TraceEvent{run_start,step,run_end} to
  `LOOPER_TRACE_ENDPOINT`; streaming_chunk never leaves the process.
- Cost bugs: anthropic streaming = $0 (no usage extracted); cache_creation
  tokens dropped; mid-stream failure loses partial usage everywhere; matrix
  miss + API cost → zeroed component split; custom costs code-only exact-match.
- Subagent linkage: ctx keys → TraceEvent.ParentRunID/ParentToolCallID;
  rollups memoized in `internal/web/rollup.go`; conv grouping by root ancestor
  (`convKeyFor`).
- Module `github.com/cuatroochenta-idi/looper-agent`, go 1.26. Bun 1.3.14
  available; **use Bun for all JS tooling** (user directive).

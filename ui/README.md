# LooperAgent panel UI

The SolidJS observability panel for looper-agent. Talks to the Go server's
`/api/state/*` REST endpoints and the typed JSON SSE stream at `/api/events`
(contract: `docs/tasks/2026-07-10_api_contract.md`).

## Stack

- **Bun** — package manager + task runner (commit `bun.lock`)
- **Vite 6** + **SolidJS** + **TypeScript** (strict)
- **@solidjs/router** — routing
- **Tailwind CSS v4** — CSS-first theming via `@theme` in `src/app.css`
- Self-hosted **Inter** + **JetBrains Mono** (`@fontsource-variable/*`)

No component library — primitives are hand-rolled in `src/components/ui`.

## Commands

```bash
bun install            # install deps
bun run dev            # Vite dev server on :5173, proxies /api /ingest /sse → :9090
bun run build          # tsc --noEmit + vite build → dist/
bun run preview        # preview the production build

VITE_MOCK=1 bun run dev    # run entirely against in-memory fixtures (no server)
```

Point the dev proxy at a non-default backend with `LOOPER_PROXY=http://host:port`.

## Layout

```
src/
  lib/
    api/        types.ts · client.ts (fetch, 401→/login) · sse.ts (reconnecting EventSource)
                index.ts (real vs mock via VITE_MOCK) · mock/ (fixtures + mock client)
    state/      per-domain Solid stores · timeRange (global `since`) · theme · sseHub · runTree
    format.ts   usd / tokens / duration / relative-time / model-split
  components/
    ui/         Button Card Badge Tabs Input Dialog Tooltip Spinner StatTile EmptyState CopyButton
    domain/     RunCard RunTree RunStatusDot CostChip ModelChip SubagentChip TokenStats
                TraceTree TraceNode ToolCallNode JsonViewer TimeRangePicker ThemeToggle
  routes/       Dashboard · Runs (Traces) · Chats · Login
  AppShell.tsx  topbar: brand · nav pills · time-range · theme toggle · live dot
```

## Model notes

- **Subagents** are never listed at top level. They appear only nested inside
  their parent (sidebar tree, inline trace expansion). An orphaned subagent
  (parent missing) falls back to top level with an `orphan` badge.
- **Costs**: `self_usd` is a run's own spend; `subtree_usd` = self + all
  descendants. Aggregates sum self costs only. A `~` prefix marks a figure
  estimated from pricing tables.
- **SSE**: one coarse connection (`runs`,`chats`,`summary`) drives list/tile
  invalidation; detail views open ephemeral `run:{id}` subscriptions for live
  `chunk` streaming. Every event is safe to drop — a gap just refetches.

## Serving from Go

`bun run build` emits `dist/`, embedded by the server (`go:embed ui/dist`)
with SPA index fallback. `LOOPER_UI_DEV` makes the server proxy to the Vite
dev server instead.

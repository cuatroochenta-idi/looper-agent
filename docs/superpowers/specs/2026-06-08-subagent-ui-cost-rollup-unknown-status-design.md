# Debug UI — subagent visibility, model-per-step, cost rollup, `unknown` status

Date: 2026-06-08 · Target release: **v0.4.1**

## Problem

The debug web UI (`internal/web/`, templ + datastar/SSE) has four gaps:

1. **Subagents are nearly invisible in the chat view.** They only appear if you
   click a bubble → open the trace pane → expand a tool node, and the spawned
   card then *navigates away* (replaces the pane, losing the parent). Running
   subagents have no bubble-level signal.
2. **The model per step isn't visible at a glance.** `TurnNode.Model` is
   recorded but only shown inside the expanded `llm_call` node — not on the turn
   summary line, subagent cards, or chat bubbles.
3. **Subagent token/€ cost is not aggregated into the parent run.** Each run
   tracks its own `runStats`; lineage exists (`ParentRunID`) but the parent's
   `TotalUSD` excludes children, so the headline number under-reports.
4. **Stale runs linger.** A sweeper marks runs idle > 3 min as `error`. The ask
   is **10 min → a new `unknown` status** (idle ≠ failure).

## Decisions (confirmed with user)

- **Subagents** → inline expansion (drawer), no navigate-away; same interaction
  in Traces and Chat.
- **Cost** → recursive display-layer rollup with breakdown `self · +sub`.
- **Stale** → 10 min → new `unknown` status (neutral grey), replaces the
  3 min→error behavior; keep the 90 s `IsStuck` soft "stuck?" hint.
- **Version** → v0.4.1.
- Costs stay in USD (`$`) — no FX source; "euros" was colloquial.

## Design

### 1 · Inline subagent expansion (`detail_pane.templ`, handlers, ingest)

- New recursive view-model `SpawnedRun{Run, Timeline, Live, Rollup, Model,
  Children map[string][]*SpawnedRun}`. `DetailData.SpawnedByToolCall` becomes
  `map[string][]*SpawnedRun`.
- `detailData()` builds the whole descendant subtree once (children-by-parent
  index + per-run rollup map), cycle-guarded.
- `spawnedRuns()` renders each child as a collapsible `<details class="spawn-node">`:
  summary = status dot, short id, **model**, rolled-up cost, turns; body =
  nested `@TraceTree(c.Timeline, c.Live, c.Children)` (recursion) + an
  `open full ↗` link that still allows full-pane navigation.
- **Live nesting:** the detail-pane SSE stream only listens on
  `TopicRun(ownID)`. On ingest, walk the `ParentRunID` chain and also publish
  each ancestor's run topic so a parent pane refreshes when a descendant emits
  an event. Cycle-guarded with a visited set.
- **Chat bubble chip:** `chatMsgBubble` shows `⤷ N sub-agentes (M live)` when the
  run spawned children. New `ChatMessage` fields `SubAgentCount`,
  `SubAgentRunning`, plus `Rollup` and `Model`, populated in `chatSidebarData`.

### 2 · Model per step

- `turnNode` summary gains a compact `provider/model` chip (reuses
  `turn.Provider`/`turn.Model`).
- Helper `RunModelLabel(r)` → dominant model from `r.Providers` (`gpt-5` or
  `gpt-5 +1` when mixed); falls back to the last usage-bearing step's `Model`
  for live runs with no provider breakdown yet. Shown on spawn cards, sidebar
  cards, and chat bubble foot.

### 3 · Cost rollup with breakdown

- `CostRollup{SelfUSD, SubUSD, SelfTokens, SubTokens, SubCount}` with
  `TotalUSD()`, `TotalTokens()`, `HasSubs()`.
- `buildRollups(all, childIndex)` — memoized, cycle-guarded recursion over the
  full run set.
- Surfaces: run header (`cost` metric → total + a one-line `incl. N sub-agents`
  note), sidebar card, chat bubble, spawn card. **Self vs sub stays visible.**
- **Correctness guard:** the Dashboard "total cost" stat already sums every
  run's own `TotalUSD` once (children are separate rows) → it is already the
  true global total. Rollup is a per-run *display* concern only; the dashboard
  aggregate is **not** changed (would double-count).

### 4 · `unknown` status + 10-min sweeper

- `RunUnknown RunStatus = "unknown"` (`types.go`).
- `stuckRunMaxIdle`: 3 m → 10 m (`server.go`). Sweeper sets `RunUnknown` (not
  `RunError`); synthetic note reworded ("no events for 10m — marked unknown").
- `Store.Counts()` gains an `unknown` return; sidebar + chat status pills add an
  `unknown` filter (`CountUnknown` on `SidebarData`/`ChatSidebarData`). The
  `string(r.Status) == filter` match handles filtering automatically.
- Neutral-grey styling: `.badge-unknown`, `.cx-status.s-unknown`,
  `.spawn-status.s-unknown`, `.msg-dot.s-unknown`, bars — using muted tokens,
  no pulse, visually distinct from red `error`.
- Keep the 90 s `IsStuck`/`stuck?` soft hint as the early warning.

### 5 · Cross-cutting

- Every `.templ` edit → `make generate` (templ) then `go build ./...`.
- **Tests** (`internal/web/*_test.go`): sweeper → `unknown` (update existing
  test); `Counts` includes unknown; `buildRollups` recursion + cycle guard;
  `RunModelLabel`; ancestor-topic fan-out on ingest.
- Bump to **v0.4.1**, tag at the end.

## Out of scope

- EUR display (no FX source); Dashboard recent-row model chip (model is already
  prominent in trace/sidebar/chat) — can follow later.

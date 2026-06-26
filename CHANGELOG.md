# Changelog

All notable changes to Looper Agent are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org).

## [v1.2.2] — 2026-06-26

Temperature is now opt-in. Previously every request carried `temperature: 0.7`
(the openai Translator baked the provider default unconditionally), which made
reasoning models — gpt-5.x, o-series — fail with a 400 (`temperature does not
support 0.7 … only the default (1) is supported`). Now temperature is omitted
unless explicitly configured, so the provider's own default applies and
reasoning models work out of the box.

### Changed

- `provider/openai` only sets `temperature` on a request when one is
  configured (non-zero). The Translator no longer bakes the provider default
  into every request.
- Default temperature dropped from `0.7` to unset (`0`) in the openai provider,
  the loop, and the agent. Set a value explicitly via `WithTemperature` /
  `WithLoopTemperature` (or per-request `LLMRequest.Temperature`) to send it.
  anthropic and google already gated temperature behind a non-zero check, so
  they only change by no longer receiving the loop's `0.7` default.

### Note

A temperature of exactly `0` is treated as "unset" (omitted). Callers that
relied on the implicit `0.7` should set it explicitly.

## [v1.2.1] — 2026-06-26

API-reported cost. When an upstream gateway returns the actual price of a call
(OpenRouter's `usage.cost`), the telemetry now uses it as the authoritative
total instead of estimating from the hardcoded price matrix; the matrix becomes
the fallback for providers that don't report a cost. Additive — the only
surface change is a new `Cost` field on `provider.Usage` and `telemetry.Usage`.

### Added

- `provider.Usage.Cost` / `telemetry.Usage.Cost` (USD) — the cost reported by
  the upstream API for a call, zero when none is reported. Multi-provider
  chains propagate the inner's value; the run accumulator sums it per
  (provider, model).
- `provider/openai` now reads a top-level `cost` field from the usage RawJSON
  on both the streaming and non-streaming paths (OpenRouter and compatible
  gateways), populating `Usage.Cost`.

### Changed

- `telemetry.CostModel.Calculate`: when `Usage.Cost > 0` it is authoritative
  for `TotalUSD`; the input/output/cached split is re-scaled from the matrix
  ratio so the breakdown stays consistent, degrading to zero on a matrix miss.
  When `Usage.Cost == 0` the behaviour is unchanged (matrix only). The
  cost-miss warning is now suppressed when an API cost is present.

## [v1.2.0] — 2026-06-25

Lazy skills: a skill can now be loaded on demand by the model instead of always
sitting in the system prompt, keeping the base context small. Minor and
additive in practice — the only contract change is two new methods on the
`skill.Skill` interface, and every in-repo implementer was updated.

### Added

- Unified skill content API: `skill.Skill` now also requires `Title() string`
  and `Summary() string` alongside the existing `Name()`, `RegisterTools()`,
  and `PromptFragment()`. Every skill — eager or lazy — exposes the same API;
  `Title`/`Summary` feed the compact skills index, `PromptFragment` carries the
  full instructions.
- `skill.LazySkill` — a `Skill` plus an unembeddable marker. Embed the new
  `skill.Lazy` struct into a skill to make it load-on-demand. Until the model
  loads it, only its `Title` + `Summary` appear in a `## Skills (load on demand
  …)` index in the system prompt; its tools stay hidden and its full
  `PromptFragment` is withheld.
- Auto-injected `load_skill` tool. When an agent is built with one or more lazy
  skills, `NewAgent` registers a native `load_skill` tool. Calling it with a
  skill name validates against the lazy set (erroring with the list of valid
  names on a miss) and returns that skill's full `PromptFragment` plus the list
  of unlocked tools, delivered as a tool result into history (never the base
  prompt).
- Activation gating read from history. A `DynamicToolsFunc` exposes the base
  tools (eager + standalone + `load_skill`) on every turn and turns a lazy
  skill's tools on once a `load_skill` call for it appears in the conversation.
  Detection is structural (assistant tool-calls, not text markers), order is
  preserved for prompt-cache stability, and a user-supplied `WithDynamicTools`
  takes precedence. Lazy-skill tools are also registered in the main registry,
  so a stray call before loading degrades gracefully rather than erroring.

### Changed

- Existing skill implementers were updated to the unified API (e.g. example
  `06_skill_and_toolkit`'s `TranslatorSkill` gained `Title`/`Summary`). New
  example `19_lazy_skills` demonstrates an eager skill alongside a lazy one.

## [v1.1.1] — 2026-06-25

Streaming robustness for OpenAI-compatible servers that append non-standard SSE
telemetry. No API changes.

### Fixed

- `provider/openai` `ChatStream` no longer fails a turn when the upstream sends
  SSE *comment* frames (e.g. `: energy …`, `: cost …`) after the usage chunk —
  as NeuralWatt / vLLM do. openai-go decodes each comment as an empty-data event
  and `json.Unmarshal("")` returns a `*json.SyntaxError` ("unexpected end of
  JSON input"); previously that surfaced as a spurious `network_error` and
  killed the turn despite a fully-streamed reply. The drop is narrow and typed:
  only a `*json.SyntaxError` that arrives *after* a `finish_reason` (reply
  already complete) is ignored. Server `error` events, connection drops, and
  any malformed chunk before `finish_reason` (a real truncation) still
  propagate.

## [v1.1.0] — 2026-06-22

Debug panel: sub-agents are now presented consistently as nested work, and the
chat trace no longer fights the operator's scroll. No API changes.

### Added

- Sub-agent indicator badge (`⤷ N sub-agents · M live`) on the parent run's
  card across all three list surfaces — dashboard "Recent runs", the chats
  conversation list, and the traces sidebar — so delegated work is visible at a
  glance without opening the run.

### Changed

- Sub-agent runs are no longer listed as standalone entries in the chat thread,
  the chats conversation list, or the dashboard "Recent runs" feed. They nest
  under their parent (whose trace still expands them inline) and are summarized
  by the new badge. Conversation grouping now keys a run by its **root
  ancestor**, so a session-less parent and the sub-agents it spawns stay a
  single conversation instead of splitting into an empty "ghost".
- Removed the read-only "Trigger a run" explainer panel from the dashboard and
  trimmed the "trigger via `POST /api/run`" hints from the empty states.

### Fixed

- **Chat trace no longer jumps to the top on live updates.** The scroll
  container now carries a stable per-run id, so datastar morphs it in place
  across SSE patches instead of replacing the node (which reset `scrollTop` to
  0 under rapid event bursts). Switching to a different run still resets to the
  top, as expected.
- **A stale trace stream can no longer hijack the panel.** Selecting one chat
  bubble then another used to leave the first run's SSE stream alive; on its
  next tick it patched the shared panel back to the old run (and, with the
  per-run id, reset scroll). A single `$selected`-keyed subscription now owns
  the trace panel, so the previous run's stream is cancelled on switch.
- Conversation-card cost and sub-agent counts now derive from the same
  full-store rollup as the in-thread bubbles, so the card and the bubbles always
  agree — previously they could diverge under an active time/status filter.

## [v1.0.0] — 2026-06-09

First stable release. The public API surface (`looper`, `loop`, `provider`,
`tool`, `toolkit`, `skill`, `mcp`, `memory`, `message`, `pause`, `telemetry`)
is now covered by the semver compatibility promise: no breaking changes
without a major version bump.

### Fixed

- **Debug panel no longer hangs.** Three compounding defects eliminated:
  - SSE patches now set a 15 s per-write deadline via
    `http.ResponseController`. Previously a half-dead client (laptop sleep,
    dropped network without RST) blocked the stream write forever once the
    kernel buffer filled — leaking the goroutine and pinning one of the
    browser's six per-host connections until the tab starved.
  - `Store.Find`/`Store.All` now return snapshot clones instead of live
    shared pointers, removing a data race between SSE renders reading
    `run.Steps` and the ingest path appending to it.
  - The HTTP server now sets `ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout`
    (no `WriteTimeout` — SSE writes are bounded individually), and the
    stuck-run sweeper publishes UI notifications before disk persistence.
- SSE patch failures are now logged unless the client already disconnected.
- Package doc example for `looper` updated to the real constructor API.

### Highlights accumulated since the last published release (v0.2.0)

- `FailoverProvider` + `KeyRotationProvider` for native provider chaining,
  with geometric backoff and typed errors (`ErrAllProvidersFailed`,
  `ErrAllKeysFailed`, `ErrCircuitOpen`). (v0.4.x)
- Per-provider cost tracking, per-chunk provenance (provider/model/key
  suffix on every step), and fallback-call accounting. (v0.4.x)
- OpenAI streaming probes the first chunk so pre-content errors surface for
  failover instead of dying mid-stream. (v0.4.0)
- Debug panel: chats view, inline sub-agent traces, model-per-step, cost
  rollup, run-tree-scoped stuck-run sweeper, "unknown" terminal status,
  live per-model token breakdown. (v0.3.x–v0.4.x)
- `SetFinalResponse` for canonical halt-turn wrap-up text.
- Parallel tool execution: tools with `ToolConfig.Parallel = true` run
  concurrently within a turn; sequential tools run afterwards in order.

[v1.1.0]: https://github.com/cuatroochenta-idi/looper-agent/releases/tag/v1.1.0
[v1.0.0]: https://github.com/cuatroochenta-idi/looper-agent/releases/tag/v1.0.0

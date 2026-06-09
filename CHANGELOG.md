# Changelog

All notable changes to Looper Agent are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org).

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

[v1.0.0]: https://github.com/cuatroochenta-idi/looper-agent/releases/tag/v1.0.0

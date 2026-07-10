import type { RunDetail, RunListItem } from "../types";

// Fixed clock so relative/duration output is stable-ish per load.
const NOW = Date.now();
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString();
const S = 1000;
const M = 60 * S;

/** Project the list-facing subset out of a full RunDetail. */
export function toListItem(r: RunDetail): RunListItem {
  const {
    system_prompt: _sp,
    input: _in,
    output: _out,
    error: _err,
    providers: _p,
    turns_detail: _t,
    child_ids: _c,
    ...rest
  } = r;
  return rest;
}

// ---------------------------------------------------------------------------
// Level 2 subagent — grep sweep spawned by the search subagent.
// ---------------------------------------------------------------------------
const subGrep: RunDetail = {
  id: "run_5f1c-grep",
  session_id: "sess_alpha",
  parent_run_id: "run_9a2b-search",
  parent_tool_call_id: "call_grep_web",
  kind: "subagent",
  project: "looper-agent",
  input_preview: "Grep every call site of web.Store across internal/",
  output_preview: "Found 14 references across 6 files; hottest is rollup.go.",
  status: "completed",
  turns: 2,
  started_at: iso(9 * M),
  ended_at: iso(8 * M + 40 * S),
  last_seen_at: iso(8 * M + 40 * S),
  self_usd: 0.0041,
  subtree_usd: 0.0041,
  cost_estimated: true,
  tokens: 3120,
  input_tokens: 2400,
  output_tokens: 720,
  cached_tokens: 1800,
  cache_write_tokens: 0,
  subagent_count: 0,
  subagents_running: 0,
  models: ["anthropic/claude-haiku-4-5"],
  fallback_calls: 0,
  system_prompt: "You are a focused code-search subagent. Return concise findings.",
  input: "Grep every call site of web.Store across internal/ and summarize the hot paths.",
  output: "Found 14 references across 6 files; hottest is rollup.go (memoized subtree rollups).",
  providers: [
    {
      provider: "anthropic",
      model: "claude-haiku-4-5",
      calls: 2,
      input_tokens: 2400,
      output_tokens: 720,
      cached_tokens: 1800,
      cache_write_tokens: 0,
      usd: 0.0041,
      estimated: true,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-haiku-4-5",
      fallback: false,
      assistant_text: "Running a repository-wide grep for the Store symbol.",
      usage: { input_tokens: 1400, output_tokens: 320, cached_tokens: 1800 },
      started_at: iso(9 * M),
      ended_at: iso(9 * M - 12 * S),
      tool_calls: [
        {
          id: "call_grep_1",
          name: "grep",
          args_json: '{"pattern":"web\\\\.Store","path":"internal/","flags":"-rn"}',
          result: {
            content: "internal/web/rollup.go:41\ninternal/web/store.go:12\n… 12 more matches",
            is_error: false,
            at: iso(9 * M - 13 * S),
          },
          spawned_run_ids: [],
        },
      ],
    },
    {
      turn: 2,
      provider: "anthropic",
      model: "claude-haiku-4-5",
      fallback: false,
      final: "Found 14 references across 6 files; hottest is rollup.go.",
      usage: { input_tokens: 1000, output_tokens: 400 },
      started_at: iso(8 * M + 50 * S),
      ended_at: iso(8 * M + 40 * S),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Level 1 subagent A — search agent that spawns the grep sweep.
// ---------------------------------------------------------------------------
const subSearch: RunDetail = {
  id: "run_9a2b-search",
  session_id: "sess_alpha",
  parent_run_id: "run_1000-main",
  parent_tool_call_id: "call_spawn_search",
  kind: "subagent",
  project: "looper-agent",
  input_preview: "Map the auth middleware surface and its Store usages",
  output_preview: "Auth flows through web.Store in 3 handlers; rollup is memoized.",
  status: "completed",
  turns: 3,
  started_at: iso(11 * M),
  ended_at: iso(8 * M + 30 * S),
  last_seen_at: iso(8 * M + 30 * S),
  self_usd: 0.0187,
  subtree_usd: 0.0228,
  cost_estimated: true,
  tokens: 9400,
  input_tokens: 7200,
  output_tokens: 2200,
  cached_tokens: 4800,
  cache_write_tokens: 900,
  subagent_count: 1,
  subagents_running: 0,
  models: ["anthropic/claude-sonnet-4-5"],
  fallback_calls: 0,
  system_prompt: "You are a codebase cartographer. Trace how a subsystem is wired.",
  input: "Map the auth middleware surface across internal/web and how it touches the Store.",
  output:
    "Auth flows through web.Store in 3 handlers (login, me, logout). Rollups are memoized in rollup.go and safe to read concurrently.",
  providers: [
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      calls: 3,
      input_tokens: 7200,
      output_tokens: 2200,
      cached_tokens: 4800,
      cache_write_tokens: 900,
      usd: 0.0187,
      estimated: true,
    },
  ],
  child_ids: [subGrep.id],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: false,
      reasoning:
        "The middleware likely wraps handlers. I should first find where Store is read, then delegate a wide grep to a cheaper model.",
      assistant_text: "I'll delegate the wide grep to a search subagent and read the middleware myself.",
      usage: { input_tokens: 2600, output_tokens: 640, cached_tokens: 1600, cache_write_tokens: 900 },
      started_at: iso(11 * M),
      ended_at: iso(10 * M + 30 * S),
      tool_calls: [
        {
          id: "call_spawn_grep",
          name: "spawn_subagent",
          args_json: '{"task":"Grep every call site of web.Store across internal/"}',
          result: {
            content: "subagent completed: 14 references across 6 files",
            is_error: false,
            at: iso(8 * M + 40 * S),
          },
          spawned_run_ids: [subGrep.id],
        },
      ],
    },
    {
      turn: 2,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: false,
      assistant_text: "Reading the middleware entrypoint to confirm the Store handoff.",
      usage: { input_tokens: 2600, output_tokens: 760, cached_tokens: 1600 },
      started_at: iso(10 * M),
      ended_at: iso(9 * M + 20 * S),
      tool_calls: [
        {
          id: "call_read_mw",
          name: "read_file",
          args_json: '{"path":"internal/web/middleware.go","limit":80}',
          result: {
            content: "func Auth(store *Store) Middleware { … }\n// 78 lines total",
            is_error: false,
            at: iso(9 * M + 55 * S),
          },
          spawned_run_ids: [],
        },
      ],
    },
    {
      turn: 3,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: false,
      final:
        "Auth flows through web.Store in 3 handlers (login, me, logout). Rollups memoized; concurrent reads are safe.",
      usage: { input_tokens: 2000, output_tokens: 800 },
      started_at: iso(9 * M),
      ended_at: iso(8 * M + 30 * S),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Level 1 subagent B — migration writer, still running.
// ---------------------------------------------------------------------------
const subMigrate: RunDetail = {
  id: "run_7c3d-migrate",
  session_id: "sess_alpha",
  parent_run_id: "run_1000-main",
  parent_tool_call_id: "call_spawn_migrate",
  kind: "subagent",
  project: "looper-agent",
  input_preview: "Write the Atlas migration for the sessions table",
  status: "running",
  turns: 2,
  started_at: iso(3 * M),
  last_seen_at: iso(4 * S),
  self_usd: 0.0312,
  subtree_usd: 0.0312,
  cost_estimated: false,
  tokens: 12800,
  input_tokens: 9800,
  output_tokens: 3000,
  cached_tokens: 6400,
  cache_write_tokens: 1200,
  subagent_count: 0,
  subagents_running: 0,
  models: ["anthropic/claude-sonnet-4-5"],
  fallback_calls: 1,
  system_prompt: "You write reversible SQL migrations. Follow Atlas naming.",
  input: "Write the Atlas migration for the sessions table with an HMAC token column.",
  output: "",
  providers: [
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      calls: 2,
      input_tokens: 9800,
      output_tokens: 3000,
      cached_tokens: 6400,
      cache_write_tokens: 1200,
      usd: 0.0312,
      estimated: false,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: true,
      api_key_suffix: "…a91f",
      reasoning: "Primary key rotated mid-call; retried on the fallback key. Schema needs a jsonb steps column.",
      assistant_text: "Drafting the CREATE TABLE and the down migration.",
      usage: { input_tokens: 5200, output_tokens: 1600, cached_tokens: 3200, cache_write_tokens: 1200 },
      started_at: iso(3 * M),
      ended_at: iso(2 * M + 10 * S),
      tool_calls: [
        {
          id: "call_write_mig",
          name: "write_file",
          args_json:
            '{"path":"internal/store/postgres/migrations/0002_sessions.sql","content":"CREATE TABLE sessions (…)"}',
          result: { content: "wrote 1.4 KB", is_error: false, at: iso(2 * M + 5 * S) },
          spawned_run_ids: [],
        },
      ],
    },
    {
      turn: 2,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: false,
      assistant_text: "Running atlas to validate the migration against the dev database…",
      usage: { input_tokens: 4600, output_tokens: 1400, cached_tokens: 3200 },
      started_at: iso(90 * S),
      tool_calls: [
        {
          id: "call_atlas",
          name: "shell",
          args_json: '{"cmd":"atlas migrate validate --dir file://internal/store/postgres/migrations"}',
          spawned_run_ids: [],
        },
      ],
    },
  ],
};

// ---------------------------------------------------------------------------
// Top-level run — the orchestrator that spawns both subagents.
// ---------------------------------------------------------------------------
const runMain: RunDetail = {
  id: "run_1000-main",
  session_id: "sess_alpha",
  kind: "run",
  project: "looper-agent",
  input_preview: "Refactor the auth middleware to use the new Store seam and add a sessions migration",
  output_preview: "Mapped the surface, wrote the migration; validation in progress.",
  status: "running",
  turns: 4,
  started_at: iso(12 * M),
  last_seen_at: iso(4 * S),
  self_usd: 0.0416,
  subtree_usd: 0.0956,
  cost_estimated: true,
  tokens: 18600,
  input_tokens: 14200,
  output_tokens: 4400,
  cached_tokens: 9600,
  cache_write_tokens: 2100,
  subagent_count: 3,
  subagents_running: 1,
  models: ["anthropic/claude-opus-4-8", "anthropic/claude-sonnet-4-5"],
  fallback_calls: 1,
  system_prompt:
    "You are LooperAgent, an autonomous engineer. Delegate wide work to subagents and keep the plan tight.",
  input:
    "Refactor the auth middleware to use the new Store seam and add a sessions migration. Delegate the codebase survey and the migration to subagents.",
  output: "Mapped the auth surface via a search subagent; a migration subagent is validating its SQL now.",
  providers: [
    {
      provider: "anthropic",
      model: "claude-opus-4-8",
      calls: 3,
      input_tokens: 11000,
      output_tokens: 3400,
      cached_tokens: 7200,
      cache_write_tokens: 2100,
      usd: 0.0361,
      estimated: true,
    },
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      calls: 1,
      input_tokens: 3200,
      output_tokens: 1000,
      cached_tokens: 2400,
      cache_write_tokens: 0,
      usd: 0.0055,
      estimated: false,
    },
  ],
  child_ids: [subSearch.id, subMigrate.id],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-opus-4-8",
      fallback: false,
      reasoning:
        "Two independent workstreams: understand the current wiring, and produce the migration. I'll fan both out to subagents and synthesize.",
      assistant_text: "I'll spawn a search subagent to map the auth surface and a migration subagent in parallel.",
      usage: { input_tokens: 4000, output_tokens: 1200, cached_tokens: 2400, cache_write_tokens: 2100 },
      started_at: iso(12 * M),
      ended_at: iso(11 * M + 20 * S),
      tool_calls: [
        {
          id: "call_spawn_search",
          name: "spawn_subagent",
          args_json: '{"task":"Map the auth middleware surface and its Store usages"}',
          result: { content: "subagent completed", is_error: false, at: iso(8 * M + 30 * S) },
          spawned_run_ids: [subSearch.id],
        },
        {
          id: "call_spawn_migrate",
          name: "spawn_subagent",
          args_json: '{"task":"Write the Atlas migration for the sessions table"}',
          result: { content: "subagent running", is_error: false, at: iso(3 * M) },
          spawned_run_ids: [subMigrate.id],
        },
      ],
    },
    {
      turn: 2,
      provider: "anthropic",
      model: "claude-opus-4-8",
      fallback: false,
      assistant_text: "Search subagent returned. Reading rollup.go to confirm concurrency safety before I touch it.",
      usage: { input_tokens: 4200, output_tokens: 1300, cached_tokens: 2400 },
      started_at: iso(8 * M),
      ended_at: iso(7 * M + 30 * S),
      tool_calls: [
        {
          id: "call_read_rollup",
          name: "read_file",
          args_json: '{"path":"internal/web/rollup.go"}',
          result: { content: "// memoized subtree rollups, sync.Map guarded", is_error: false, at: iso(7 * M + 40 * S) },
          spawned_run_ids: [],
        },
      ],
    },
    {
      turn: 3,
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      fallback: false,
      assistant_text: "Sketching the middleware change against the Store seam.",
      usage: { input_tokens: 3200, output_tokens: 1000, cached_tokens: 2400 },
      started_at: iso(6 * M),
      ended_at: iso(5 * M + 20 * S),
      tool_calls: [],
    },
    {
      turn: 4,
      provider: "anthropic",
      model: "claude-opus-4-8",
      fallback: false,
      assistant_text: "Waiting on the migration subagent's atlas validation before wiring the handler.",
      usage: { input_tokens: 2800, output_tokens: 900 },
      started_at: iso(2 * M),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Top-level run — live streaming single-turn chat (drives chunk events).
// ---------------------------------------------------------------------------
const runStream: RunDetail = {
  id: "run_2200-stream",
  session_id: "sess_beta",
  kind: "run",
  project: "looper-agent",
  input_preview: "Explain how the rollup memoization avoids double-counting subagent costs",
  status: "running",
  turns: 1,
  started_at: iso(20 * S),
  last_seen_at: iso(1 * S),
  self_usd: 0.0089,
  subtree_usd: 0.0089,
  cost_estimated: false,
  tokens: 4200,
  input_tokens: 3400,
  output_tokens: 800,
  cached_tokens: 2200,
  cache_write_tokens: 0,
  subagent_count: 0,
  subagents_running: 0,
  models: ["anthropic/claude-opus-4-8"],
  fallback_calls: 0,
  system_prompt: "You explain systems crisply for engineers.",
  input: "Explain how the rollup memoization avoids double-counting subagent costs.",
  output: "",
  providers: [
    {
      provider: "anthropic",
      model: "claude-opus-4-8",
      calls: 1,
      input_tokens: 3400,
      output_tokens: 800,
      cached_tokens: 2200,
      cache_write_tokens: 0,
      usd: 0.0089,
      estimated: false,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-opus-4-8",
      fallback: false,
      reasoning: "Key idea: self_usd stays on each run; subtree_usd is a read-time rollup, never a mutation.",
      assistant_text: "",
      started_at: iso(20 * S),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Top-level run — errored deploy.
// ---------------------------------------------------------------------------
const runErr: RunDetail = {
  id: "run_3300-deploy",
  session_id: "sess_beta",
  kind: "run",
  project: "infra",
  input_preview: "Deploy the panel build to the staging cluster",
  output_preview: "Failed: image push rejected (unauthorized to registry).",
  status: "errored",
  turns: 2,
  started_at: iso(48 * M),
  ended_at: iso(47 * M),
  last_seen_at: iso(47 * M),
  self_usd: 0.0123,
  subtree_usd: 0.0123,
  cost_estimated: false,
  tokens: 5600,
  input_tokens: 4400,
  output_tokens: 1200,
  cached_tokens: 0,
  cache_write_tokens: 0,
  subagent_count: 0,
  subagents_running: 0,
  models: ["openai/gpt-5.1"],
  fallback_calls: 0,
  system_prompt: "You are a release engineer.",
  input: "Deploy the panel build to the staging cluster and report the rollout status.",
  output: "",
  error: "image push rejected: unauthorized to registry ghcr.io/looper (401)",
  providers: [
    {
      provider: "openai",
      model: "gpt-5.1",
      calls: 2,
      input_tokens: 4400,
      output_tokens: 1200,
      cached_tokens: 0,
      cache_write_tokens: 0,
      usd: 0.0123,
      estimated: false,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "openai",
      model: "gpt-5.1",
      fallback: false,
      assistant_text: "Building the image and pushing to the registry.",
      usage: { input_tokens: 2400, output_tokens: 700 },
      started_at: iso(48 * M),
      ended_at: iso(47 * M + 40 * S),
      tool_calls: [
        {
          id: "call_push",
          name: "shell",
          args_json: '{"cmd":"docker push ghcr.io/looper/panel:staging"}',
          result: {
            content: "denied: unauthorized to access repository (401)",
            is_error: true,
            at: iso(47 * M + 20 * S),
          },
          spawned_run_ids: [],
        },
      ],
    },
    {
      turn: 2,
      provider: "openai",
      model: "gpt-5.1",
      fallback: false,
      error: "image push rejected: unauthorized to registry ghcr.io/looper (401)",
      usage: { input_tokens: 2000, output_tokens: 500 },
      started_at: iso(47 * M + 15 * S),
      ended_at: iso(47 * M),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Top-level run — simple completed Q&A (its own chat).
// ---------------------------------------------------------------------------
const runSolo: RunDetail = {
  id: "run_4400-solo",
  session_id: "sess_gamma",
  kind: "run",
  project: "looper-agent",
  input_preview: "What is the difference between self_usd and subtree_usd?",
  output_preview: "self_usd is a run's own spend; subtree_usd rolls in all descendants.",
  status: "completed",
  turns: 1,
  started_at: iso(2 * 60 * M),
  ended_at: iso(2 * 60 * M - 8 * S),
  last_seen_at: iso(2 * 60 * M - 8 * S),
  self_usd: 0.0027,
  subtree_usd: 0.0027,
  cost_estimated: true,
  tokens: 1400,
  input_tokens: 1100,
  output_tokens: 300,
  cached_tokens: 0,
  cache_write_tokens: 0,
  subagent_count: 0,
  subagents_running: 0,
  models: ["google/gemini-2.5-pro"],
  fallback_calls: 0,
  system_prompt: "You answer concisely.",
  input: "What is the difference between self_usd and subtree_usd?",
  output:
    "self_usd is a run's own LLM spend. subtree_usd is self plus every descendant subagent — a memoized rollup used for attribution without double counting.",
  providers: [
    {
      provider: "google",
      model: "gemini-2.5-pro",
      calls: 1,
      input_tokens: 1100,
      output_tokens: 300,
      cached_tokens: 0,
      cache_write_tokens: 0,
      usd: 0.0027,
      estimated: true,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "google",
      model: "gemini-2.5-pro",
      fallback: false,
      final:
        "self_usd is a run's own LLM spend. subtree_usd is self plus every descendant subagent — a memoized rollup used for attribution without double counting.",
      usage: { input_tokens: 1100, output_tokens: 300 },
      started_at: iso(2 * 60 * M),
      ended_at: iso(2 * 60 * M - 8 * S),
      tool_calls: [],
    },
  ],
};

// ---------------------------------------------------------------------------
// Orphaned subagent — parent id present but parent not in the list.
// Falls back to top-level with an "orphan" badge.
// ---------------------------------------------------------------------------
const runOrphan: RunDetail = {
  id: "run_5500-orphan",
  session_id: "sess_delta",
  parent_run_id: "run_missing-9999",
  parent_tool_call_id: "call_gone",
  kind: "subagent",
  project: "looper-agent",
  input_preview: "Summarize the failing test output for the parent",
  output_preview: "3 tests fail in telemetry/cost_test.go; all assert cache-write pricing.",
  status: "completed",
  turns: 1,
  started_at: iso(30 * M),
  ended_at: iso(29 * M + 50 * S),
  last_seen_at: iso(29 * M + 50 * S),
  self_usd: 0.0019,
  subtree_usd: 0.0019,
  cost_estimated: true,
  tokens: 900,
  input_tokens: 700,
  output_tokens: 200,
  cached_tokens: 0,
  cache_write_tokens: 0,
  subagent_count: 0,
  subagents_running: 0,
  models: ["anthropic/claude-haiku-4-5"],
  fallback_calls: 0,
  system_prompt: "You summarize test failures.",
  input: "Summarize the failing test output for the parent run.",
  output: "3 tests fail in telemetry/cost_test.go; all assert cache-write pricing at 1.25×.",
  providers: [
    {
      provider: "anthropic",
      model: "claude-haiku-4-5",
      calls: 1,
      input_tokens: 700,
      output_tokens: 200,
      cached_tokens: 0,
      cache_write_tokens: 0,
      usd: 0.0019,
      estimated: true,
    },
  ],
  child_ids: [],
  turns_detail: [
    {
      turn: 1,
      provider: "anthropic",
      model: "claude-haiku-4-5",
      fallback: false,
      final: "3 tests fail in telemetry/cost_test.go; all assert cache-write pricing at 1.25×.",
      usage: { input_tokens: 700, output_tokens: 200 },
      started_at: iso(30 * M),
      ended_at: iso(29 * M + 50 * S),
      tool_calls: [],
    },
  ],
};

export const ALL_DETAILS: RunDetail[] = [
  runMain,
  subSearch,
  subGrep,
  subMigrate,
  runStream,
  runErr,
  runSolo,
  runOrphan,
];

/** The token the streaming run will emit, chunk by chunk. */
export const STREAM_RUN_ID = runStream.id;
export const STREAM_TEXT =
  "Each run carries its own self_usd — the exact dollars it spent on LLM calls. " +
  "Subagent costs stay on the subagent's own record; the parent never mutates a child's cost. " +
  "subtree_usd is computed at read time by walking the parent→child edges and summing self_usd across the subtree, " +
  "memoized so repeated reads are cheap. Because aggregates (the dashboard, /api/state/costs) sum self_usd only, " +
  "a dollar is counted exactly once no matter how deep the tree goes.";

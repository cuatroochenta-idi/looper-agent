-- Declarative desired schema — the single source of truth for `make db-diff`.
-- Atlas diffs this file against the applied migrations to author the next
-- migration. The runtime migrator (postgres.go) never reads this file; it only
-- applies the committed files under migrations/. Keep this in sync with them.

CREATE TABLE "looper_runs" (
  "id"                  text PRIMARY KEY,
  "session_id"          text NOT NULL DEFAULT '',
  "parent_run_id"       text NOT NULL DEFAULT '',
  "parent_tool_call_id" text NOT NULL DEFAULT '',
  "project"             text NOT NULL DEFAULT '',
  "status"              text NOT NULL DEFAULT '',
  "started_at"          timestamptz NOT NULL,
  "ended_at"            timestamptz NULL,
  "last_seen_at"        timestamptz NOT NULL,
  "total_usd"           double precision NOT NULL DEFAULT 0,
  "cost_estimated"      boolean NOT NULL DEFAULT false,
  "tokens"              bigint NOT NULL DEFAULT 0,
  "input_tokens"        bigint NOT NULL DEFAULT 0,
  "output_tokens"       bigint NOT NULL DEFAULT 0,
  "cached_tokens"       bigint NOT NULL DEFAULT 0,
  "cache_write_tokens"  bigint NOT NULL DEFAULT 0,
  "record"              jsonb NOT NULL
);
CREATE INDEX "looper_runs_started_at_idx" ON "looper_runs" ("started_at" DESC);
CREATE INDEX "looper_runs_session_id_idx" ON "looper_runs" ("session_id");
CREATE INDEX "looper_runs_parent_run_id_idx" ON "looper_runs" ("parent_run_id");
CREATE INDEX "looper_runs_status_idx" ON "looper_runs" ("status");

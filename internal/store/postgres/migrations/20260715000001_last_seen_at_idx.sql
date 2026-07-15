-- Incremental hydration reads filter on last_seen_at (LoadRunsSince), which
-- the cross-replica hydrator polls every few seconds — index it.
CREATE INDEX "looper_runs_last_seen_at_idx" ON "looper_runs" ("last_seen_at");

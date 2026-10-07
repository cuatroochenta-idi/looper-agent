-- Read projections: the panel lists, aggregates and searches runs from these
-- columns and loads `record` only for a run's detail.
ALTER TABLE "looper_runs"
  ADD COLUMN "input"          text    NOT NULL DEFAULT '',
  ADD COLUMN "output_preview" text    NOT NULL DEFAULT '',
  ADD COLUMN "turns"          integer NOT NULL DEFAULT 0,
  ADD COLUMN "fallback_calls" integer NOT NULL DEFAULT 0,
  ADD COLUMN "providers"      jsonb   NOT NULL DEFAULT '[]';
UPDATE "looper_runs" SET
  "input"          = btrim(coalesce("record"->>'input', ''), E' \t\n\r'),
  "output_preview" = left(btrim(coalesce("record"->>'output', ''), E' \t\n\r'), 201),
  "turns"          = coalesce(("record"->>'turns')::integer, 0),
  "fallback_calls" = coalesce(("record"->>'fallback_calls')::integer, 0),
  "providers"      = coalesce(nullif("record"->'providers', 'null'::jsonb), '[]');
ANALYZE "looper_runs";

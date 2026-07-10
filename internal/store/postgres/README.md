# internal/store/postgres

PostgreSQL-backed `web.Persistence` for the debug panel. Run snapshots are
stored so the panel survives restarts.

## Model

Single table `looper_runs`:

- `record jsonb NOT NULL` — the **source of truth**: the full `web.RunRecord`
  JSON, with live-only `streaming_chunk` / `reasoning_chunk` steps stripped
  (via `web.PersistableSnapshot`, matching the folder backend byte-for-byte).
- Scalar columns (`id`, `session_id`, `parent_run_id`, `parent_tool_call_id`,
  `project`, `status`, `started_at`, `ended_at`, `last_seen_at`, `total_usd`,
  `cost_estimated`, `tokens`, `input_tokens`, `output_tokens`, `cached_tokens`,
  `cache_write_tokens`) are **projections** maintained on write, purely for
  querying/indexing. Reads reconstruct the record from jsonb only.
- Indexes: `started_at DESC`, `session_id`, `parent_run_id`, `status`.

`SaveRun` is an idempotent upsert (`INSERT ... ON CONFLICT (id) DO UPDATE`).
`LoadRuns` returns all runs ordered by `started_at ASC`.

## Migrations — runtime vs authoring

**Runtime is self-contained. No Atlas binary is needed to run the panel.**

`NewPostgres` embeds `migrations/*.sql` via `go:embed` and applies any pending
files in filename order, each in its own transaction, tracking applied versions
in `looper_schema_migrations(version, applied_at)`. Applying an up-to-date
database is a no-op, so startup is always safe.

**Authoring** new migrations uses the [Atlas](https://atlasgo.io) CLI, which
diffs the declarative desired state in `schema.sql` against the existing
migrations and writes the delta:

```sh
# 1. edit schema.sql to the new desired state
# 2. generate the next migration (needs atlas + docker for the dev database)
make db-diff NAME=add_something
#   → atlas migrate diff add_something \
#       --dir file://internal/store/postgres/migrations \
#       --to  file://internal/store/postgres/schema.sql \
#       --dev-url docker://postgres/17/dev
# 3. commit the new migrations/<ts>_add_something.sql AND the updated atlas.sum
```

`atlas.sum` is an authoring-time integrity checksum; the runtime applier ignores
it (only `*.sql` is embedded). If you hand-edit a migration, run
`make db-hash` to regenerate it.

## Files

- `postgres.go`   — `Postgres` type, `NewPostgres`, `SaveRun` / `LoadRuns` / `Close`.
- `migrate.go`    — embedded, self-contained versioned migrator.
- `migrations/`   — Atlas-authored `*.sql` + `atlas.sum` (committed).
- `schema.sql`    — declarative desired state; input to `make db-diff`.

## Running the panel with Postgres

```sh
looper serve --db 'postgres://looper:looper@localhost:5432/looper?sslmode=disable'
# or:
LOOPER_DB='postgres://looper:looper@localhost:5432/looper?sslmode=disable' looper serve
```

`--db` (or `LOOPER_DB`) overrides `--store`; when both are set, `--db` wins with
a warning. Without either, the panel uses the folder backend at `.looper`.

## Tests

`postgres_test.go` skips unless `LOOPER_TEST_PG_DSN` points at a reachable
Postgres — default `go test` runs require neither a database nor Docker:

```sh
LOOPER_TEST_PG_DSN='postgres://looper:looper@localhost:5432/looper?sslmode=disable' \
  go test ./internal/store/postgres/
```

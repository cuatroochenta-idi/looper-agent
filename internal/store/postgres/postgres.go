// Package postgres is a PostgreSQL-backed web.Persistence: it durably stores
// agent run snapshots so the panel survives restarts. The full RunRecord lives
// in a jsonb column (source of truth); scalar columns are query projections.
//
// Import direction: this package imports internal/web for RunRecord and
// implements web.Persistence. web never imports here, so there is no cycle.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cuatroochenta-idi/looper-agent/internal/web"
)

// Postgres is a pgxpool-backed durable store for finalized runs.
type Postgres struct {
	pool *pgxpool.Pool
}

// Postgres implements the persistence seam.
var _ web.Persistence = (*Postgres)(nil)

// NewPostgres connects to dsn, verifies the connection, and applies any pending
// embedded migrations before returning. The returned store owns the pool; call
// Close to release it. Migrations are self-contained — no Atlas binary needed.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// upsertSQL upserts a run by id, refreshing both the jsonb record and every
// scalar projection column from the incoming snapshot.
const upsertSQL = `
INSERT INTO looper_runs (
	id, session_id, parent_run_id, parent_tool_call_id, project, status,
	started_at, ended_at, last_seen_at, total_usd, cost_estimated,
	tokens, input_tokens, output_tokens, cached_tokens, cache_write_tokens, record
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (id) DO UPDATE SET
	session_id          = EXCLUDED.session_id,
	parent_run_id       = EXCLUDED.parent_run_id,
	parent_tool_call_id = EXCLUDED.parent_tool_call_id,
	project             = EXCLUDED.project,
	status              = EXCLUDED.status,
	started_at          = EXCLUDED.started_at,
	ended_at            = EXCLUDED.ended_at,
	last_seen_at        = EXCLUDED.last_seen_at,
	total_usd           = EXCLUDED.total_usd,
	cost_estimated      = EXCLUDED.cost_estimated,
	tokens              = EXCLUDED.tokens,
	input_tokens        = EXCLUDED.input_tokens,
	output_tokens       = EXCLUDED.output_tokens,
	cached_tokens       = EXCLUDED.cached_tokens,
	cache_write_tokens  = EXCLUDED.cache_write_tokens,
	record              = EXCLUDED.record`

// SaveRun upserts a finalized run. The persisted record is denoised
// (streaming/reasoning chunks stripped) via web.PersistableSnapshot so it
// matches the folder backend byte-for-byte on reload.
func (p *Postgres) SaveRun(r *web.RunRecord) error {
	snap := web.PersistableSnapshot(r)
	record, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("postgres: marshal run %s: %w", r.ID, err)
	}
	// timestamptz NULL for a run that has not ended, instead of the zero time.
	var endedAt *time.Time
	if !snap.EndedAt.IsZero() {
		t := snap.EndedAt
		endedAt = &t
	}
	if _, err := p.pool.Exec(context.Background(), upsertSQL,
		snap.ID, snap.SessionID, snap.ParentRunID, snap.ParentToolCallID, snap.Project, string(snap.Status),
		snap.StartedAt, endedAt, snap.LastSeenAt, snap.TotalUSD, snap.CostEstimated,
		snap.Tokens, snap.InputTokens, snap.OutputTokens, snap.CachedTokens, snap.CacheWriteTokens, record,
	); err != nil {
		return fmt.Errorf("postgres: save run %s: %w", r.ID, err)
	}
	return nil
}

// LoadRuns returns every stored run in chronological order, reconstructed from
// the jsonb record column (the scalar columns are query aids, not read back).
func (p *Postgres) LoadRuns() ([]*web.RunRecord, error) {
	return p.queryRuns(`SELECT record FROM looper_runs ORDER BY started_at ASC`)
}

// LoadRunsSince returns the runs whose last_seen_at is at or after since, in
// chronological order — the incremental read behind the cross-replica
// hydrator (see looper_runs_last_seen_at_idx).
func (p *Postgres) LoadRunsSince(since time.Time) ([]*web.RunRecord, error) {
	return p.queryRuns(
		`SELECT record FROM looper_runs WHERE last_seen_at >= $1 ORDER BY started_at ASC`,
		since)
}

func (p *Postgres) queryRuns(sql string, args ...any) ([]*web.RunRecord, error) {
	rows, err := p.pool.Query(context.Background(), sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: load runs: %w", err)
	}
	defer rows.Close()

	var out []*web.RunRecord
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("postgres: scan run: %w", err)
		}
		var r web.RunRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal run: %w", err)
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate runs: %w", err)
	}
	return out, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

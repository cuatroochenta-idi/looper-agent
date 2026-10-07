package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cuatroochenta-idi/looper-agent/internal/web"
)

// headerInputChars keeps enough of the input for web.Preview and for the
// user-message check, without loading long prompts into list reads.
const headerInputChars = 400

// headerCols selects a run header from the projection columns; it never
// touches `record`. It is followed by one extra boolean column in every query.
var headerCols = fmt.Sprintf(`r.id, r.session_id, r.parent_run_id, r.parent_tool_call_id, r.project,
	r.status, r.started_at, r.ended_at, r.last_seen_at, r.total_usd, r.cost_estimated,
	r.tokens, r.input_tokens, r.output_tokens, r.cached_tokens, r.cache_write_tokens,
	left(r.input, %d), r.output_preview, r.turns, r.fallback_calls, r.providers`, headerInputChars)

// relatedSQL wraps a seed query (selecting `id`) into the seeds plus every
// ancestor and descendant, as headers flagged with whether each is a seed.
// UNION dedups, so a malformed parent loop terminates.
const relatedSQL = `
WITH RECURSIVE seed AS (%s),
up AS (
	SELECT r.id, r.parent_run_id FROM looper_runs r JOIN seed USING (id)
	UNION
	SELECT p.id, p.parent_run_id FROM up JOIN looper_runs p ON p.id = up.parent_run_id
),
down AS (
	SELECT id FROM seed
	UNION
	SELECT c.id FROM down JOIN looper_runs c ON c.parent_run_id = down.id
)
SELECT %s, r.id IN (SELECT id FROM seed)
FROM (SELECT id FROM up UNION SELECT id FROM down) ids
JOIN looper_runs r USING (id)
ORDER BY r.started_at, r.id`

const subtreeSQL = `
WITH RECURSIVE down AS (
	SELECT id FROM looper_runs WHERE id = $1
	UNION
	SELECT c.id FROM down JOIN looper_runs c ON c.parent_run_id = down.id
)
SELECT %s, true FROM looper_runs r JOIN down USING (id) ORDER BY r.started_at, r.id`

// summarySQL mirrors the in-memory summary: costs over every run in the
// window, counts over its top-level runs (no parent inside the window).
const summarySQL = `
WITH w AS (
	SELECT r.status, r.turns, r.total_usd, r.tokens, r.cost_estimated,
		(r.parent_run_id = '' OR NOT EXISTS (
			SELECT 1 FROM looper_runs p WHERE p.id = r.parent_run_id AND p.started_at >= $1)) AS top
	FROM looper_runs r WHERE r.started_at >= $1
)
SELECT coalesce(sum(total_usd), 0), coalesce(bool_or(cost_estimated), false), coalesce(sum(tokens), 0),
	count(*) FILTER (WHERE top),
	count(*) FILTER (WHERE top AND status = 'running'),
	count(*) FILTER (WHERE top AND status = 'completed'),
	count(*) FILTER (WHERE top AND status = 'error'),
	count(*) FILTER (WHERE top AND status = 'unknown'),
	coalesce(sum(turns) FILTER (WHERE top), 0)
FROM w`

const costTotalsSQL = `
SELECT coalesce(sum(total_usd), 0), coalesce(bool_or(cost_estimated), false)
FROM looper_runs WHERE started_at >= $1`

const costsByModelSQL = `
SELECT coalesce(p->>'provider', ''), coalesce(p->>'model', ''),
	coalesce(sum((p->>'calls')::bigint), 0),
	coalesce(sum((p->>'input_tokens')::bigint), 0),
	coalesce(sum((p->>'output_tokens')::bigint), 0),
	coalesce(sum((p->>'cached_tokens')::bigint), 0),
	coalesce(sum((p->>'cache_write_tokens')::bigint), 0),
	coalesce(sum((p->>'total_usd')::double precision), 0),
	coalesce(bool_or((p->>'estimated')::boolean), false)
FROM looper_runs r CROSS JOIN LATERAL jsonb_array_elements(r.providers) p
WHERE r.started_at >= $1
GROUP BY 1, 2`

// runsSeedSQL picks the newest runs matching a RunFilter; LIMIT NULL is no cap.
const runsSeedSQL = `
SELECT id FROM looper_runs
WHERE started_at >= $1
	AND ($2 = '' OR status = $2)
	AND ($3 = '' OR strpos(lower(input), $3) > 0 OR strpos(lower(id), $3) > 0)
ORDER BY started_at DESC
LIMIT $4`

const conversationSeedSQL = `SELECT id FROM looper_runs WHERE session_id = $1 OR id = $1`

// messagesSQL loads what messagesForRun reads: full input/output and, only
// when the output is empty, the steps it falls back to.
const messagesSQL = `
SELECT id, status, started_at, ended_at, last_seen_at, cost_estimated,
	coalesce(record->>'input', ''), coalesce(record->>'output', ''),
	CASE WHEN coalesce(record->>'input', '') = '' OR coalesce(record->>'output', '') = ''
		THEN coalesce(jsonb_path_query_array(record->'steps',
			'$[*] ? (@.Kind == "user_input" || @.Kind == "final_response" || @.Kind == "streaming_chunk")'), '[]')
		ELSE '[]' END
FROM looper_runs WHERE id = ANY($1)`

// sweepSQL finalizes running runs idle since $1 that have no running
// descendant, appending the error step $4 to their record.
const sweepSQL = `
WITH RECURSIVE cand AS (
	SELECT id FROM looper_runs
	WHERE status = 'running' AND greatest(last_seen_at, started_at) < $1
),
down AS (
	SELECT id AS root, id FROM cand
	UNION
	SELECT down.root, c.id FROM down JOIN looper_runs c ON c.parent_run_id = down.id
),
busy AS (
	SELECT DISTINCT down.root FROM down JOIN looper_runs r ON r.id = down.id
	WHERE down.id <> down.root AND r.status = 'running'
)
UPDATE looper_runs r SET
	status = 'unknown',
	ended_at = $2,
	record = jsonb_set(jsonb_set(r.record, '{status}', '"unknown"'), '{ended_at}', $3::jsonb)
		|| jsonb_build_object('steps', coalesce(r.record->'steps', '[]'::jsonb) || $4::jsonb)
FROM cand
WHERE r.id = cand.id AND cand.id NOT IN (SELECT root FROM busy)
RETURNING r.id`

func (p *Postgres) Summary(ctx context.Context, since time.Time) (web.SummaryResponse, error) {
	var resp web.SummaryResponse
	var turnSum int
	if err := p.pool.QueryRow(ctx, summarySQL, since).Scan(
		&resp.TotalUSD, &resp.CostEstimated, &resp.TotalTokens,
		&resp.TotalRuns, &resp.Running, &resp.Completed, &resp.Errored, &resp.Unknown, &turnSum,
	); err != nil {
		return resp, fmt.Errorf("postgres: summary: %w", err)
	}
	if resp.TotalRuns > 0 {
		resp.AvgTurns = float64(turnSum) / float64(resp.TotalRuns)
	}
	resp.TotalUSD = web.Round8(resp.TotalUSD)
	return resp, nil
}

func (p *Postgres) Costs(ctx context.Context, since time.Time) (web.CostsResponse, error) {
	var resp web.CostsResponse
	if err := p.pool.QueryRow(ctx, costTotalsSQL, since).Scan(&resp.TotalUSD, &resp.CostEstimated); err != nil {
		return resp, fmt.Errorf("postgres: cost totals: %w", err)
	}
	rows, err := p.pool.Query(ctx, costsByModelSQL, since)
	if err != nil {
		return resp, fmt.Errorf("postgres: costs by model: %w", err)
	}
	defer rows.Close()
	var byModel []web.ModelCost
	for rows.Next() {
		var c web.ModelCost
		if err := rows.Scan(&c.Provider, &c.Model, &c.Calls, &c.InputTokens, &c.OutputTokens,
			&c.CachedTokens, &c.CacheWriteTokens, &c.USD, &c.Estimated); err != nil {
			return resp, fmt.Errorf("postgres: scan cost row: %w", err)
		}
		byModel = append(byModel, c)
	}
	if err := rows.Err(); err != nil {
		return resp, fmt.Errorf("postgres: costs by model: %w", err)
	}
	resp.TotalUSD = web.Round8(resp.TotalUSD)
	resp.ByModel = web.SortModelCosts(byModel)
	return resp, nil
}

func (p *Postgres) Runs(ctx context.Context, f web.RunFilter) (web.RunSet, error) {
	var limit *int
	if f.Limit > 0 {
		limit = &f.Limit
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	return p.related(ctx, runsSeedSQL, f.Since, f.Status, q, limit)
}

func (p *Postgres) Run(ctx context.Context, id string) (*web.RunRecord, error) {
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT record FROM looper_runs WHERE id = $1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: load run %s: %w", id, err)
	}
	var r web.RunRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal run %s: %w", id, err)
	}
	return &r, nil
}

func (p *Postgres) Subtree(ctx context.Context, id string) ([]*web.RunRecord, error) {
	all, _, err := p.headers(ctx, fmt.Sprintf(subtreeSQL, headerCols), id)
	return all, err
}

func (p *Postgres) Conversation(ctx context.Context, key string) ([]*web.RunRecord, error) {
	set, err := p.related(ctx, conversationSeedSQL, key)
	return set.All, err
}

func (p *Postgres) Messages(ctx context.Context, ids []string) ([]*web.RunRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, messagesSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: load messages: %w", err)
	}
	defer rows.Close()
	var out []*web.RunRecord
	for rows.Next() {
		var r web.RunRecord
		var status string
		var endedAt *time.Time
		var steps []byte
		if err := rows.Scan(&r.ID, &status, &r.StartedAt, &endedAt, &r.LastSeenAt, &r.CostEstimated,
			&r.Input, &r.Output, &steps); err != nil {
			return nil, fmt.Errorf("postgres: scan messages: %w", err)
		}
		r.Status = web.RunStatus(status)
		r.StartedAt, r.LastSeenAt = r.StartedAt.UTC(), r.LastSeenAt.UTC()
		if endedAt != nil {
			r.EndedAt = endedAt.UTC()
		}
		if err := json.Unmarshal(steps, &r.Steps); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal message steps %s: %w", r.ID, err)
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: load messages: %w", err)
	}
	return out, nil
}

func (p *Postgres) SweepStuckRuns(ctx context.Context, maxIdle time.Duration, now time.Time) ([]string, error) {
	endedAt, err := json.Marshal(now)
	if err != nil {
		return nil, err
	}
	step, err := json.Marshal([]web.TimelineStep{{
		Kind: web.StepKindError,
		Err:  "no events received for " + maxIdle.String() + " — marked unknown (process likely died or run_end lost)",
		At:   now,
	}})
	if err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, sweepSQL, now.Add(-maxIdle), now, string(endedAt), string(step))
	if err != nil {
		return nil, fmt.Errorf("postgres: sweep stuck runs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("postgres: sweep stuck runs: %w", err)
	}
	return ids, nil
}

// related loads a seed query's runs with their ancestors and descendants.
func (p *Postgres) related(ctx context.Context, seedSQL string, args ...any) (web.RunSet, error) {
	all, matched, err := p.headers(ctx, fmt.Sprintf(relatedSQL, seedSQL, headerCols), args...)
	return web.RunSet{Matched: matched, All: all}, err
}

// headers scans header rows (headerCols plus a seed flag) in query order.
func (p *Postgres) headers(ctx context.Context, sql string, args ...any) (all, matched []*web.RunRecord, err error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: load run headers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r web.RunRecord
		var status string
		var endedAt *time.Time
		var providers []byte
		var seed bool
		if err := rows.Scan(&r.ID, &r.SessionID, &r.ParentRunID, &r.ParentToolCallID, &r.Project,
			&status, &r.StartedAt, &endedAt, &r.LastSeenAt, &r.TotalUSD, &r.CostEstimated,
			&r.Tokens, &r.InputTokens, &r.OutputTokens, &r.CachedTokens, &r.CacheWriteTokens,
			&r.Input, &r.Output, &r.Turns, &r.FallbackCalls, &providers, &seed); err != nil {
			return nil, nil, fmt.Errorf("postgres: scan run header: %w", err)
		}
		r.Status = web.RunStatus(status)
		r.StartedAt, r.LastSeenAt = r.StartedAt.UTC(), r.LastSeenAt.UTC()
		if endedAt != nil {
			r.EndedAt = endedAt.UTC()
		}
		if err := json.Unmarshal(providers, &r.Providers); err != nil {
			return nil, nil, fmt.Errorf("postgres: unmarshal providers %s: %w", r.ID, err)
		}
		all = append(all, &r)
		if seed {
			matched = append(matched, &r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres: load run headers: %w", err)
	}
	return all, matched, nil
}

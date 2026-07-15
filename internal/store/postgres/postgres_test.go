package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/internal/web"
)

// requirePG connects to the DSN in LOOPER_TEST_PG_DSN or skips. Default `go
// test` runs must not require a database or Docker, so the whole suite is gated
// on that env var being set to a reachable Postgres.
func requirePG(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("LOOPER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LOOPER_TEST_PG_DSN to a Postgres DSN to run postgres store tests")
	}
	pg, err := NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	// Isolate from any prior run rows so assertions on LoadRuns are stable.
	if _, err := pg.pool.Exec(context.Background(), `TRUNCATE looper_runs`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pg
}

// TestMigrateIdempotent asserts a second NewPostgres against the same database
// applies nothing new and still succeeds.
func TestMigrateIdempotent(t *testing.T) {
	_ = requirePG(t) // first apply
	dsn := os.Getenv("LOOPER_TEST_PG_DSN")
	pg2, err := NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("second NewPostgres (idempotent migrate): %v", err)
	}
	defer pg2.Close()

	var n int
	if err := pg2.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+migrationsTable).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	files, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles: %v", err)
	}
	if n != len(files) {
		t.Fatalf("applied migrations = %d, want %d (one per embedded file)", n, len(files))
	}
}

// TestSaveLoadRoundTrip covers upsert, jsonb step fidelity, and started_at
// ordering through a real Save + Load cycle.
func TestSaveLoadRoundTrip(t *testing.T) {
	pg := requirePG(t)

	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	older := &web.RunRecord{
		ID:        "run-older",
		SessionID: "sess-1",
		Status:    web.RunCompleted,
		StartedAt: base,
		EndedAt:   base.Add(time.Minute),
		TotalUSD:  0.0123,
		Steps: []web.TimelineStep{
			{Kind: web.StepKindUserInput, Content: "hello", At: base},
			{Kind: web.StepKindToolCall, ToolName: "add", ToolArgs: `{"a":1,"b":2}`, At: base.Add(time.Second)},
			// Chunk steps must be stripped by PersistableSnapshot before storage.
			{Kind: web.StepKindStreamingChunk, Content: "partial", At: base.Add(2 * time.Second)},
			{Kind: web.StepKindFinal, Content: "3", At: base.Add(3 * time.Second)},
		},
	}
	newer := &web.RunRecord{
		ID:          "run-newer",
		ParentRunID: "run-older",
		Status:      web.RunRunning,
		StartedAt:   base.Add(time.Hour),
		LastSeenAt:  base.Add(time.Hour),
	}

	for _, r := range []*web.RunRecord{newer, older} { // insert out of order
		if err := pg.SaveRun(r); err != nil {
			t.Fatalf("SaveRun %s: %v", r.ID, err)
		}
	}

	// Upsert path: re-save older with a mutated field.
	older.TotalUSD = 0.0456
	if err := pg.SaveRun(older); err != nil {
		t.Fatalf("SaveRun upsert: %v", err)
	}

	runs, err := pg.LoadRuns()
	if err != nil {
		t.Fatalf("LoadRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("LoadRuns len = %d, want 2 (upsert must not duplicate)", len(runs))
	}
	// started_at ASC ordering.
	if runs[0].ID != "run-older" || runs[1].ID != "run-newer" {
		t.Fatalf("ordering = [%s, %s], want [run-older, run-newer]", runs[0].ID, runs[1].ID)
	}
	got := runs[0]
	if got.TotalUSD != 0.0456 {
		t.Fatalf("upsert TotalUSD = %v, want 0.0456", got.TotalUSD)
	}
	// jsonb step fidelity, chunks stripped (4 in → 3 stored).
	if len(got.Steps) != 3 {
		t.Fatalf("stored steps = %d, want 3 (streaming_chunk stripped)", len(got.Steps))
	}
	for _, s := range got.Steps {
		if s.Kind == web.StepKindStreamingChunk {
			t.Fatalf("streaming_chunk survived persistence")
		}
	}
	if got.Steps[1].ToolName != "add" || got.Steps[1].ToolArgs != `{"a":1,"b":2}` {
		t.Fatalf("tool step fidelity lost: %+v", got.Steps[1])
	}
}

// TestLoadRunsSince asserts the incremental hydrator read: only rows with
// last_seen_at at or after the cursor, chronological order preserved.
func TestLoadRunsSince(t *testing.T) {
	pg := requirePG(t)
	base := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	runs := []*web.RunRecord{
		{ID: "since-old", Status: web.RunCompleted, StartedAt: base, LastSeenAt: base},
		{ID: "since-mid", Status: web.RunCompleted, StartedAt: base.Add(time.Hour), LastSeenAt: base.Add(time.Hour)},
		{ID: "since-new", Status: web.RunRunning, StartedAt: base.Add(2 * time.Hour), LastSeenAt: base.Add(2 * time.Hour)},
	}
	for _, r := range runs {
		if err := pg.SaveRun(r); err != nil {
			t.Fatalf("SaveRun %s: %v", r.ID, err)
		}
	}
	got, err := pg.LoadRunsSince(base.Add(30 * time.Minute))
	if err != nil {
		t.Fatalf("LoadRunsSince: %v", err)
	}
	if len(got) != 2 || got[0].ID != "since-mid" || got[1].ID != "since-new" {
		t.Fatalf("LoadRunsSince returned %d rows (want since-mid, since-new in order)", len(got))
	}
	all, err := pg.LoadRunsSince(time.Time{})
	if err != nil {
		t.Fatalf("LoadRunsSince(zero): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("LoadRunsSince(zero) len = %d, want 3", len(all))
	}
}

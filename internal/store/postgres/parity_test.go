package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/internal/web"
)

// parityEvents builds a history covering every read: a two-turn session whose
// first run spawns a sub-agent, a run without session still running, an
// errored run, and an old run outside the 1h window.
func parityEvents(now time.Time) []web.TraceEvent {
	at := func(min int) time.Time { return now.Add(time.Duration(min) * time.Minute) }
	ev := func(typ, id string, ts time.Time, data any, opts ...func(*web.TraceEvent)) web.TraceEvent {
		raw, _ := json.Marshal(data)
		e := web.TraceEvent{Type: typ, RunID: id, Ts: ts, Data: raw}
		for _, o := range opts {
			o(&e)
		}
		return e
	}
	sess := func(s string) func(*web.TraceEvent) {
		return func(e *web.TraceEvent) { e.SessionID = s; e.Project = "demo" }
	}
	child := func(e *web.TraceEvent) { e.ParentRunID = "turn-1"; e.ParentToolCallID = "tc1" }
	provider := func(model string, usd float64) []map[string]any {
		return []map[string]any{{"provider": "anthropic", "model": model, "calls": 2, "input_tokens": 100, "output_tokens": 20, "total_usd": usd}}
	}
	return []web.TraceEvent{
		ev("run_start", "old", at(-180), map[string]any{"input": "an old question", "started_at": at(-180).Format(time.RFC3339Nano)}, sess("s-old")),
		ev("run_end", "old", at(-179), map[string]any{"output": "old answer", "status": "completed", "turns": 1, "total_usd": 0.5, "providers": provider("haiku", 0.5)}),

		ev("run_start", "turn-1", at(-40), map[string]any{"input": "  build me an invoice app  ", "system_prompt": "be helpful", "started_at": at(-40).Format(time.RFC3339Nano)}, sess("s1")),
		ev("step", "turn-1", at(-39), map[string]any{"kind": "tool_call", "turn": 0, "tool_name": "spawn", "tool_call_id": "tc1", "tool_args": `{"task":"x"}`}),
		ev("run_start", "sub-1", at(-38), map[string]any{"input": "research invoices", "started_at": at(-38).Format(time.RFC3339Nano)}, sess("s1"), child),
		ev("step", "sub-1", at(-37), map[string]any{"kind": "final_response", "turn": 0, "content": "found it"}),
		ev("run_end", "sub-1", at(-36), map[string]any{"output": "found it", "status": "completed", "turns": 1, "total_usd": 0.25, "cost_estimated": true, "providers": provider("haiku", 0.25)}),
		ev("step", "turn-1", at(-35), map[string]any{"kind": "tool_result", "turn": 0, "tool_call_id": "tc1", "content": "found it"}),
		ev("step", "turn-1", at(-34), map[string]any{"kind": "final_response", "turn": 1, "content": "here is your app"}),
		ev("run_end", "turn-1", at(-33), map[string]any{"status": "completed", "turns": 2, "total_usd": 1.5, "input_tokens": 300, "output_tokens": 50, "providers": provider("sonnet", 1.5), "fallback_calls": 1}),

		ev("run_start", "turn-2", at(-20), map[string]any{"input": "add a due date", "started_at": at(-20).Format(time.RFC3339Nano)}, sess("s1")),
		ev("step", "turn-2", at(-19), map[string]any{"kind": "error", "turn": 0, "err": "boom"}),
		ev("run_end", "turn-2", at(-18), map[string]any{"status": "error", "err": "boom", "turns": 1, "total_usd": 0.1, "providers": provider("sonnet", 0.1)}),

		ev("run_start", "live", at(-1), map[string]any{"input": "still thinking", "started_at": at(-1).Format(time.RFC3339Nano)}),
		ev("step", "live", at(0), map[string]any{"kind": "llm_call", "turn": 0}),
	}
}

// TestPostgresReadsMatchMemory serves the same history from the in-memory
// store and from Postgres and requires every endpoint to answer the same.
func TestPostgresReadsMatchMemory(t *testing.T) {
	pg := requirePG(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	mem, err := web.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	db, err := web.NewServer(web.WithPersistence(pg))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range parityEvents(now) {
		if err := mem.IngestEvent(ev); err != nil {
			t.Fatalf("memory ingest %s/%s: %v", ev.Type, ev.RunID, err)
		}
		if err := db.IngestEvent(ev); err != nil {
			t.Fatalf("postgres ingest %s/%s: %v", ev.Type, ev.RunID, err)
		}
	}

	paths := []string{
		"/api/state/summary", "/api/state/summary?since=1h",
		"/api/state/costs", "/api/state/costs?since=1h",
		"/api/state/runs", "/api/state/runs?since=1h", "/api/state/runs?status=error",
		"/api/state/runs?q=INVOICE", "/api/state/runs?limit=2",
		"/api/state/runs/turn-1", "/api/state/runs/sub-1", "/api/state/runs/live",
		"/api/state/chats", "/api/state/chats?since=1h",
		"/api/state/chats/s1", "/api/state/chats/s1?since=30m", "/api/state/chats/live", "/api/state/chats/s-old",
	}
	for _, p := range paths {
		want, got := fetch(t, mem.Handler(), p), fetch(t, db.Handler(), p)
		if !reflect.DeepEqual(want, got) {
			wj, _ := json.MarshalIndent(want, "", " ")
			gj, _ := json.MarshalIndent(got, "", " ")
			t.Errorf("GET %s differs\n%s", p, lineDiff(string(wj), string(gj)))
		}
	}

	if live := db.Store().All(); len(live) != 1 || live[0].ID != "live" {
		ids := make([]string, len(live))
		for i, r := range live {
			ids[i] = r.ID
		}
		t.Fatalf("in-memory runs with postgres = %v, want only the running one", ids)
	}
}

// TestPostgresIngestResumesUnknownRun covers a run started by another replica:
// its next event loads it from Postgres instead of being dropped.
func TestPostgresIngestResumesUnknownRun(t *testing.T) {
	pg := requirePG(t)
	now := time.Now().UTC()
	first, _ := web.NewServer(web.WithPersistence(pg))
	start, _ := json.Marshal(map[string]any{"input": "hi", "started_at": now.Format(time.RFC3339Nano)})
	if err := first.IngestEvent(web.TraceEvent{Type: "run_start", RunID: "handoff", Ts: now, Data: start}); err != nil {
		t.Fatal(err)
	}

	second, _ := web.NewServer(web.WithPersistence(pg))
	step, _ := json.Marshal(map[string]any{"kind": "final_response", "content": "done"})
	end, _ := json.Marshal(map[string]any{"status": "completed", "output": "done", "turns": 1})
	for _, ev := range []web.TraceEvent{
		{Type: "step", RunID: "handoff", Ts: now.Add(time.Second), Data: step},
		{Type: "run_end", RunID: "handoff", Ts: now.Add(2 * time.Second), Data: end},
	} {
		if err := second.IngestEvent(ev); err != nil {
			t.Fatal(err)
		}
	}

	run, err := pg.Run(context.Background(), "handoff")
	if err != nil || run == nil {
		t.Fatalf("Run: %v, %v", run, err)
	}
	if run.Status != web.RunCompleted || run.Output != "done" || len(run.Steps) != 2 {
		t.Fatalf("resumed run = status %s output %q steps %d, want completed/done/2", run.Status, run.Output, len(run.Steps))
	}
	if n := len(second.Store().All()); n != 0 {
		t.Fatalf("finished run still in memory (%d runs)", n)
	}
}

// TestPostgresSweepStuckRuns finalizes idle running runs but keeps a parent
// whose sub-agent is still running.
func TestPostgresSweepStuckRuns(t *testing.T) {
	pg := requirePG(t)
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	for _, r := range []*web.RunRecord{
		{ID: "stuck", Status: web.RunRunning, StartedAt: old, LastSeenAt: old},
		{ID: "parent", Status: web.RunRunning, StartedAt: old, LastSeenAt: old},
		{ID: "child", ParentRunID: "parent", Status: web.RunRunning, StartedAt: now, LastSeenAt: now},
		{ID: "done", Status: web.RunCompleted, StartedAt: old, LastSeenAt: old},
	} {
		if err := pg.SaveRun(r); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := pg.SweepStuckRuns(context.Background(), 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "stuck" {
		t.Fatalf("swept %v, want [stuck]", ids)
	}
	run, _ := pg.Run(context.Background(), "stuck")
	last := run.Steps[len(run.Steps)-1]
	if run.Status != web.RunUnknown || !run.EndedAt.Equal(now) || last.Kind != web.StepKindError {
		t.Fatalf("swept record = status %s ended %v last step %s", run.Status, run.EndedAt, last.Kind)
	}
}

func fetch(t *testing.T, h http.Handler, path string) any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var out any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return map[string]any{"status": rec.Code, "body": out}
}

// lineDiff lists the lines of want and got that differ, position by position.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d\n  memory:   %s\n  postgres: %s\n", i, wl, gl)
		}
	}
	return b.String()
}

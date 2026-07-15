package web

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakePersist is an in-memory Persistence that records writes and serves a
// scripted result for loads, so write-through and hydration are testable
// without a database.
type fakePersist struct {
	mu        sync.Mutex
	saves     map[string]int
	latest    map[string]*RunRecord
	toLoad    []*RunRecord
	lastSince time.Time
}

func newFakePersist() *fakePersist {
	return &fakePersist{saves: map[string]int{}, latest: map[string]*RunRecord{}}
}

func (f *fakePersist) SaveRun(r *RunRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves[r.ID]++
	f.latest[r.ID] = PersistableSnapshot(r)
	return nil
}

func (f *fakePersist) LoadRuns() ([]*RunRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.toLoad, nil
}

func (f *fakePersist) LoadRunsSince(since time.Time) ([]*RunRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSince = since
	var out []*RunRecord
	for _, r := range f.toLoad {
		if !r.LastSeenAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakePersist) Close() error { return nil }

func (f *fakePersist) saveCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves[id]
}

func (f *fakePersist) setToLoad(runs ...*RunRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toLoad = runs
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Write-through: every meaningful ingest event snapshots the run to the
// persistence layer so other replicas can hydrate it while it is still
// running. Chunk-only events are skipped (the persistable snapshot strips
// them, so writing would be redundant I/O).
func TestIngest_WriteThroughOnEveryMeaningfulEvent(t *testing.T) {
	fp := newFakePersist()
	s, err := NewServer(WithPersistence(fp))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	if err := s.IngestEvent(TraceEvent{
		RunID: "r1", Type: "run_start", Ts: now,
		Data: mustJSON(t, map[string]string{"input": "hola", "started_at": now.Format(time.RFC3339Nano)}),
	}); err != nil {
		t.Fatal(err)
	}
	if got := fp.saveCount("r1"); got != 1 {
		t.Fatalf("after run_start: saves = %d, want 1", got)
	}

	if err := s.IngestEvent(TraceEvent{
		RunID: "r1", Type: "step", Ts: now.Add(time.Second),
		Data: mustJSON(t, map[string]any{"kind": string(StepKindToolCall), "tool_name": "create_page"}),
	}); err != nil {
		t.Fatal(err)
	}
	if got := fp.saveCount("r1"); got != 2 {
		t.Fatalf("after tool step: saves = %d, want 2", got)
	}
	if st := fp.latest["r1"].Status; st != RunRunning {
		t.Fatalf("persisted mid-run status = %s, want running", st)
	}

	// Streaming chunks must not hit the persistence layer.
	if err := s.IngestEvent(TraceEvent{
		RunID: "r1", Type: "step", Ts: now.Add(2 * time.Second),
		Data: mustJSON(t, map[string]any{"kind": string(StepKindStreamingChunk), "content": "par"}),
	}); err != nil {
		t.Fatal(err)
	}
	if got := fp.saveCount("r1"); got != 2 {
		t.Fatalf("after chunk step: saves = %d, want still 2", got)
	}

	if err := s.IngestEvent(TraceEvent{
		RunID: "r1", Type: "run_end", Ts: now.Add(3 * time.Second),
		Data: mustJSON(t, map[string]string{"status": string(RunCompleted), "ended_at": now.Add(3 * time.Second).Format(time.RFC3339Nano)}),
	}); err != nil {
		t.Fatal(err)
	}
	if got := fp.saveCount("r1"); got != 3 {
		t.Fatalf("after run_end: saves = %d, want 3", got)
	}
	if st := fp.latest["r1"].Status; st != RunCompleted {
		t.Fatalf("persisted final status = %s, want completed", st)
	}
}

// A child's events refresh the parent's liveness; the parent snapshot must be
// re-persisted too, or other replicas would sweep the parent as stuck while
// its sub-agent works.
func TestIngest_WriteThroughRefreshesAncestors(t *testing.T) {
	fp := newFakePersist()
	s, err := NewServer(WithPersistence(fp))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start := func(id, parent string, ts time.Time) {
		t.Helper()
		if err := s.IngestEvent(TraceEvent{
			RunID: id, ParentRunID: parent, Type: "run_start", Ts: ts,
			Data: mustJSON(t, map[string]string{"input": "x", "started_at": ts.Format(time.RFC3339Nano)}),
		}); err != nil {
			t.Fatal(err)
		}
	}
	start("parent", "", now)
	start("child", "parent", now.Add(time.Second))
	parentSaves := fp.saveCount("parent")

	if err := s.IngestEvent(TraceEvent{
		RunID: "child", Type: "step", Ts: now.Add(time.Minute),
		Data: mustJSON(t, map[string]any{"kind": string(StepKindToolCall), "tool_name": "write_page"}),
	}); err != nil {
		t.Fatal(err)
	}
	if got := fp.saveCount("parent"); got <= parentSaves {
		t.Fatalf("parent saves after child step = %d, want > %d (liveness write-through)", got, parentSaves)
	}
	if last := fp.latest["parent"].LastSeenAt; !last.Equal(now.Add(time.Minute)) {
		t.Fatalf("persisted parent LastSeenAt = %v, want child step ts", last)
	}
}

// hydrateOnce pulls fresher snapshots from persistence into the in-memory
// store: unknown runs appear, fresher snapshots replace staler ones, and a
// fresher local record is never clobbered.
func TestHydrateOnce_MergesFresherRemoteRuns(t *testing.T) {
	fp := newFakePersist()
	s, err := NewServer(WithPersistence(fp))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()

	remote := &RunRecord{ID: "remote", Status: RunRunning, StartedAt: base, LastSeenAt: base}
	fp.setToLoad(remote)
	s.hydrateOnce(base.Add(-time.Minute))
	got := s.store.Find("remote")
	if got == nil || got.Status != RunRunning {
		t.Fatalf("remote run not hydrated: %+v", got)
	}

	// Fresher finalized snapshot wins.
	fp.setToLoad(&RunRecord{ID: "remote", Status: RunCompleted, StartedAt: base,
		EndedAt: base.Add(time.Minute), LastSeenAt: base.Add(time.Minute)})
	s.hydrateOnce(base.Add(-time.Minute))
	if got := s.store.Find("remote"); got.Status != RunCompleted {
		t.Fatalf("fresher snapshot did not replace: status = %s", got.Status)
	}

	// Staler snapshot must NOT regress the record.
	fp.setToLoad(&RunRecord{ID: "remote", Status: RunRunning, StartedAt: base, LastSeenAt: base})
	s.hydrateOnce(base.Add(-time.Hour))
	if got := s.store.Find("remote"); got.Status != RunCompleted {
		t.Fatalf("staler snapshot regressed the record: status = %s", got.Status)
	}
}

// A live local run (same LastSeenAt as its own persisted snapshot) keeps its
// in-memory record — hydration must not strip live chunk steps mid-stream.
func TestHydrateOnce_DoesNotClobberLiveLocalRun(t *testing.T) {
	fp := newFakePersist()
	s, err := NewServer(WithPersistence(fp))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.IngestEvent(TraceEvent{
		RunID: "local", Type: "run_start", Ts: now,
		Data: mustJSON(t, map[string]string{"input": "x", "started_at": now.Format(time.RFC3339Nano)}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.IngestEvent(TraceEvent{
		RunID: "local", Type: "step", Ts: now.Add(time.Second),
		Data: mustJSON(t, map[string]any{"kind": string(StepKindStreamingChunk), "content": "live"}),
	}); err != nil {
		t.Fatal(err)
	}
	stepsBefore := len(s.store.Find("local").Steps)

	// What another replica would read back: our own write-through snapshot.
	fp.setToLoad(fp.latest["local"])
	s.hydrateOnce(now.Add(-time.Minute))
	after := s.store.Find("local")
	if len(after.Steps) != stepsBefore {
		t.Fatalf("hydration clobbered live local run: steps %d → %d", stepsBefore, len(after.Steps))
	}
}

// Boot must NOT finalize hydrated runs that are fresh — they may be live on
// another replica. Genuinely stale ones still get purged.
func TestNewServer_BootKeepsFreshRunningRuns(t *testing.T) {
	fp := newFakePersist()
	now := time.Now().UTC()
	fp.setToLoad(
		&RunRecord{ID: "fresh", Status: RunRunning, StartedAt: now.Add(-time.Minute), LastSeenAt: now},
		&RunRecord{ID: "stale", Status: RunRunning, StartedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-time.Hour)},
	)
	s, err := NewServer(WithPersistence(fp))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.store.Find("fresh"); got == nil || got.Status != RunRunning {
		t.Fatalf("fresh remote run was finalized at boot: %+v", got)
	}
	if got := s.store.Find("stale"); got == nil || got.Status == RunRunning {
		t.Fatalf("stale orphan run survived boot: %+v", got)
	}
}

package web

import (
	"path/filepath"
	"testing"
	"time"
)

// TestFolderPersistenceRoundTrip covers Save + Load through a folder backend in
// a tmp dir, including chunk-step stripping and started_at ordering.
func TestFolderPersistenceRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".looper")
	p, err := NewFolderPersistence(dir)
	if err != nil {
		t.Fatalf("NewFolderPersistence: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	base := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	older := &RunRecord{
		ID:        "aaaaaaaa-older",
		Status:    RunCompleted,
		StartedAt: base,
		EndedAt:   base.Add(time.Minute),
		Steps: []TimelineStep{
			{Kind: StepKindUserInput, Content: "hi", At: base},
			{Kind: StepKindStreamingChunk, Content: "partial", At: base.Add(time.Second)},
			{Kind: StepKindReasoning, Content: "thinking", At: base.Add(2 * time.Second)},
			{Kind: StepKindFinal, Content: "done", At: base.Add(3 * time.Second)},
		},
	}
	newer := &RunRecord{
		ID:        "bbbbbbbb-newer",
		Status:    RunCompleted,
		StartedAt: base.Add(time.Hour),
	}

	for _, r := range []*RunRecord{newer, older} { // out of order on purpose
		if err := p.SaveRun(r); err != nil {
			t.Fatalf("SaveRun %s: %v", r.ID, err)
		}
	}

	runs, err := p.LoadRuns()
	if err != nil {
		t.Fatalf("LoadRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("LoadRuns len = %d, want 2", len(runs))
	}
	// Filename timestamp prefix yields chronological order.
	if runs[0].ID != "aaaaaaaa-older" || runs[1].ID != "bbbbbbbb-newer" {
		t.Fatalf("ordering = [%s, %s], want older then newer", runs[0].ID, runs[1].ID)
	}
	// streaming_chunk + reasoning_chunk stripped: 4 in → 2 persisted.
	if len(runs[0].Steps) != 2 {
		t.Fatalf("persisted steps = %d, want 2 (chunks stripped)", len(runs[0].Steps))
	}
	for _, s := range runs[0].Steps {
		if s.Kind == StepKindStreamingChunk || s.Kind == StepKindReasoning {
			t.Fatalf("chunk step survived persistence: %s", s.Kind)
		}
	}
}

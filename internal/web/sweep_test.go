package web

import (
	"testing"
	"time"
)

func TestSweepStuckRuns_finalizesIdleRunning(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	store.Add(&RunRecord{
		ID:        "a",
		Status:    RunRunning,
		StartedAt: now.Add(-10 * time.Minute),
		Steps: []TimelineStep{
			{Kind: StepKindLLMCall, At: now.Add(-9 * time.Minute)},
		},
	})
	store.Add(&RunRecord{
		ID:        "b",
		Status:    RunRunning,
		StartedAt: now.Add(-10 * time.Second),
	})
	store.Add(&RunRecord{
		ID:        "c",
		Status:    RunCompleted,
		StartedAt: now.Add(-1 * time.Hour),
	})

	finalized := store.SweepStuckRuns(3*time.Minute, now)
	if len(finalized) != 1 || finalized[0] != "a" {
		t.Fatalf("expected only run a to be finalized, got %v", finalized)
	}

	a := store.Find("a")
	if a.Status != RunUnknown {
		t.Fatalf("run a should be RunUnknown, got %s", a.Status)
	}
	if a.EndedAt != now {
		t.Fatalf("run a should be ended at sweep time")
	}
	if len(a.Steps) != 2 || a.Steps[1].Kind != StepKindError {
		t.Fatalf("run a should have a synthetic error step appended, got %+v", a.Steps)
	}

	b := store.Find("b")
	if b.Status != RunRunning {
		t.Fatalf("recent run b should still be RunRunning, got %s", b.Status)
	}
	c := store.Find("c")
	if c.Status != RunCompleted {
		t.Fatalf("completed run c should not be touched, got %s", c.Status)
	}
}

func TestSweepStuckRuns_busyChildKeepsParentAlive(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	// Parent's own last activity is 20 min ago (well past the cap)...
	store.Add(&RunRecord{
		ID:         "parent",
		Status:     RunRunning,
		StartedAt:  now.Add(-30 * time.Minute),
		LastSeenAt: now.Add(-20 * time.Minute),
	})
	// ...but a sub-agent is still running and emitted 1s ago.
	store.Add(&RunRecord{
		ID:          "child",
		ParentRunID: "parent",
		Status:      RunRunning,
		StartedAt:   now.Add(-25 * time.Minute),
		LastSeenAt:  now.Add(-1 * time.Second),
	})

	finalized := store.SweepStuckRuns(10*time.Minute, now)
	if len(finalized) != 0 {
		t.Fatalf("a parent with a busy running child must not be swept, got %v", finalized)
	}
	if store.Find("parent").Status != RunRunning {
		t.Fatalf("parent should stay running while its sub-agent works")
	}
	if store.Find("child").Status != RunRunning {
		t.Fatalf("a recently-active child should stay running")
	}
}

func TestSweepStuckRuns_sweepsSilentLeafButNotParent(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	store.Add(&RunRecord{
		ID:         "parent",
		Status:     RunRunning,
		StartedAt:  now.Add(-30 * time.Minute),
		LastSeenAt: now.Add(-20 * time.Minute),
	})
	// Child is still "running" but has gone silent past the cap.
	store.Add(&RunRecord{
		ID:          "child",
		ParentRunID: "parent",
		Status:      RunRunning,
		StartedAt:   now.Add(-25 * time.Minute),
		LastSeenAt:  now.Add(-15 * time.Minute),
	})

	finalized := store.SweepStuckRuns(10*time.Minute, now)
	if len(finalized) != 1 || finalized[0] != "child" {
		t.Fatalf("only the silent leaf child should be swept, got %v", finalized)
	}
	if store.Find("parent").Status != RunRunning {
		t.Fatalf("parent must be kept alive while it has a running descendant")
	}
	if store.Find("child").Status != RunUnknown {
		t.Fatalf("silent running leaf should be marked unknown, got %s", store.Find("child").Status)
	}
}

func TestSweepStuckRuns_sweepsFullyIdleTree(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	store := NewStore()
	store.Add(&RunRecord{
		ID:         "parent",
		Status:     RunRunning,
		StartedAt:  now.Add(-30 * time.Minute),
		LastSeenAt: now.Add(-20 * time.Minute),
	})
	// Child already finished — no running descendant keeps the parent alive.
	store.Add(&RunRecord{
		ID:          "child",
		ParentRunID: "parent",
		Status:      RunCompleted,
		StartedAt:   now.Add(-25 * time.Minute),
		LastSeenAt:  now.Add(-22 * time.Minute),
	})

	finalized := store.SweepStuckRuns(10*time.Minute, now)
	if len(finalized) != 1 || finalized[0] != "parent" {
		t.Fatalf("a fully-idle tree should finalize the running parent, got %v", finalized)
	}
	if store.Find("parent").Status != RunUnknown {
		t.Fatalf("idle parent with no running descendant should be unknown")
	}
}

func TestIsStuck(t *testing.T) {
	now := time.Now()
	notStuck := &RunRecord{Status: RunRunning, StartedAt: now}
	if notStuck.IsStuck(90 * time.Second) {
		t.Fatalf("a just-started run is not stuck")
	}
	old := &RunRecord{Status: RunRunning, StartedAt: now.Add(-5 * time.Minute)}
	if !old.IsStuck(90 * time.Second) {
		t.Fatalf("a 5-minute idle run should be stuck")
	}
	completed := &RunRecord{Status: RunCompleted, StartedAt: now.Add(-1 * time.Hour)}
	if completed.IsStuck(90 * time.Second) {
		t.Fatalf("a completed run is never stuck")
	}
}

func TestLooksLikeToolError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain ok", "some result", false},
		{"prefix lowercase", "error: thing failed", true},
		{"prefix mixed", "Error: something", true},
		{"json error key", `{"error":"bad"}`, true},
		{"json ok false", `{"ok":false,"message":"x"}`, true},
		{"json status error", `{"status":"error"}`, true},
		{"json success false", `{"success":false}`, true},
		{"json clean", `{"ok":true,"data":{}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeToolError(tc.in); got != tc.want {
				t.Errorf("looksLikeToolError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestToolCallNode_HasError(t *testing.T) {
	nilResult := ToolCallNode{Call: TimelineStep{Kind: StepKindToolCall, ToolName: "x"}}
	if nilResult.HasError() {
		t.Fatalf("nil result should not be flagged")
	}
	withErr := ToolCallNode{
		Call:   TimelineStep{Kind: StepKindToolCall, ToolName: "x"},
		Result: &TimelineStep{Kind: StepKindToolResult, Err: "boom"},
	}
	if !withErr.HasError() {
		t.Fatalf("explicit Err should flag the node")
	}
	withErrContent := ToolCallNode{
		Call:   TimelineStep{Kind: StepKindToolCall, ToolName: "x"},
		Result: &TimelineStep{Kind: StepKindToolResult, Content: `{"ok":false}`},
	}
	if !withErrContent.HasError() {
		t.Fatalf("error-shaped content should flag the node")
	}
}

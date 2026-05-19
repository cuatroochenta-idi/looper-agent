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
	if a.Status != RunError {
		t.Fatalf("run a should be RunError, got %s", a.Status)
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

package pause

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestPauseManager_ConcurrentPausesRouteByRequestID asserts that when two
// runs pause concurrently, each Resume call addressed by RequestID reaches
// the right caller — the single shared respCh used to be a cross-talk
// surface (one resume consumed by whichever goroutine raced first).
func TestPauseManager_ConcurrentPausesRouteByRequestID(t *testing.T) {
	pm := NewPauseManager()

	var (
		gotA, gotB *PauseResponse
		wg         sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		r, _ := pm.Pause(context.Background(), PauseRequest{
			RequestID: "run-A",
			ToolName:  "search",
			Timeout:   2 * time.Second,
		})
		gotA = r
	}()
	go func() {
		defer wg.Done()
		r, _ := pm.Pause(context.Background(), PauseRequest{
			RequestID: "run-B",
			ToolName:  "search",
			Timeout:   2 * time.Second,
		})
		gotB = r
	}()

	// Wait briefly so both Pause calls have registered.
	time.Sleep(50 * time.Millisecond)

	// Resume with mismatched verdicts; if the manager routes by RequestID,
	// each goroutine sees its own answer.
	if err := pm.Resume(&PauseResponse{RequestID: "run-A", Action: "ok"}); err != nil {
		t.Fatalf("resume A: %v", err)
	}
	if err := pm.Resume(&PauseResponse{RequestID: "run-B", Action: "cancel"}); err != nil {
		t.Fatalf("resume B: %v", err)
	}

	wg.Wait()
	if gotA == nil || gotA.Action != "ok" {
		t.Errorf("run-A expected ok, got %+v", gotA)
	}
	if gotB == nil || gotB.Action != "cancel" {
		t.Errorf("run-B expected cancel, got %+v", gotB)
	}
}

// TestPauseManager_LegacyResumeWithoutRequestID asserts that callers that
// don't set RequestID (i.e. existing single-session examples) keep working
// — Resume without a RequestID falls back to whatever pause is pending.
func TestPauseManager_LegacyResumeWithoutRequestID(t *testing.T) {
	pm := NewPauseManager()

	var got *PauseResponse
	done := make(chan struct{})
	go func() {
		r, _ := pm.Pause(context.Background(), PauseRequest{
			ToolName: "search",
			Timeout:  2 * time.Second,
		})
		got = r
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := pm.Resume(&PauseResponse{Action: "ok"}); err != nil {
		t.Fatalf("legacy resume: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pause never resumed (legacy fallback broken)")
	}
	if got == nil || got.Action != "ok" {
		t.Errorf("legacy: expected ok, got %+v", got)
	}
}

// TestPauseManager_ResumeBeforePauseReturnsError asserts that calling
// Resume when no caller is waiting reports a useful error instead of
// silently dropping the response (the legacy behavior).
func TestPauseManager_ResumeBeforePauseReturnsError(t *testing.T) {
	pm := NewPauseManager()
	err := pm.Resume(&PauseResponse{RequestID: "ghost", Action: "ok"})
	if err == nil {
		t.Error("expected error resuming with no pending pause")
	}
}

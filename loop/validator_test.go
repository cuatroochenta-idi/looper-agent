package loop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// scriptedValidator is a TurnValidator whose Validate behavior is fully
// specified by the test. It records every snapshot it received so the test
// can assert on the order of invocations.
type scriptedValidator struct {
	mu       sync.Mutex
	verdict  func(snap TurnSnapshot) Outcome
	receivedSnapshots []TurnSnapshot
}

func (v *scriptedValidator) Validate(_ context.Context, snap TurnSnapshot) Outcome {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.receivedSnapshots = append(v.receivedSnapshots, snap)
	return v.verdict(snap)
}

// TestValidator_AcceptsFirstTurn asserts the happy path: validator says OK
// on the first turn and the loop returns the assistant's final text without
// any system hint injected.
func TestValidator_AcceptsFirstTurn(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{{Content: "all good", IsFinal: true}},
	}
	v := &scriptedValidator{verdict: func(_ TurnSnapshot) Outcome {
		return Outcome{OK: true}
	}}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil,
		WithLoopTurnValidator(v, 2))

	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "all good" {
		t.Errorf("expected pass-through output, got %q", res.Output)
	}
	if res.Status != "completed" {
		t.Errorf("expected completed status, got %q", res.Status)
	}
	if len(v.receivedSnapshots) != 1 {
		t.Errorf("validator should have been called once, got %d invocations", len(v.receivedSnapshots))
	}
}

// TestValidator_RejectsThenAccepts asserts the re-prompt loop: first turn
// rejected (hint added as system message), second turn accepted.
func TestValidator_RejectsThenAccepts(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "bad answer", IsFinal: true},
			{Content: "good answer", IsFinal: true},
		},
	}
	v := &scriptedValidator{}
	calls := 0
	v.verdict = func(_ TurnSnapshot) Outcome {
		calls++
		if calls == 1 {
			return Outcome{OK: false, Reason: "off-topic", Hint: "stay on topic please"}
		}
		return Outcome{OK: true}
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil,
		WithLoopTurnValidator(v, 2))

	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "good answer" {
		t.Errorf("expected second turn's output, got %q", res.Output)
	}
	if res.Status != "completed" {
		t.Errorf("expected completed status, got %q", res.Status)
	}

	// History should contain the system message with the hint.
	found := false
	for _, m := range res.History.Messages() {
		if m.Type == message.MessageSystem && strings.Contains(m.Content, "stay on topic") {
			found = true
		}
	}
	if !found {
		t.Error("expected validator hint to appear as system message in history")
	}
}

// TestValidator_ExhaustsRetries asserts that once the validator rejects
// maxRetries+1 turns in a row, the loop stops with a validation_exhausted
// status and returns the last attempted output (so the caller can still
// observe what the model produced).
func TestValidator_ExhaustsRetries(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "bad 1", IsFinal: true},
			{Content: "bad 2", IsFinal: true},
			{Content: "bad 3", IsFinal: true},
		},
	}
	v := &scriptedValidator{verdict: func(_ TurnSnapshot) Outcome {
		return Outcome{OK: false, Reason: "always-bad", Hint: "try again"}
	}}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil,
		WithLoopTurnValidator(v, 2),
		WithLoopMaxTurns(5),
	)

	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "validation_exhausted" {
		t.Errorf("expected validation_exhausted status, got %q", res.Status)
	}
	// Validator runs 1 (initial) + 2 (retries) = 3 calls total.
	if len(v.receivedSnapshots) != 3 {
		t.Errorf("expected 3 validator invocations (1 initial + 2 retries), got %d", len(v.receivedSnapshots))
	}
	if res.Output == "" {
		t.Error("expected the last attempted output to be surfaced even on validation_exhausted")
	}
}

// TestValidator_ResetsOnSuccess asserts the retry counter resets after an
// accepting turn — so a later rejection still gets a fresh budget.
func TestValidator_ResetsOnSuccess(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "bad 1", IsFinal: true},
			{Content: "good 1", IsFinal: true},
			{Content: "bad 2", IsFinal: true},
			{Content: "good 2", IsFinal: true},
		},
	}
	v := &scriptedValidator{}
	pattern := []bool{false, true, false, true} // reject, accept, reject, accept
	i := 0
	v.verdict = func(_ TurnSnapshot) Outcome {
		ok := pattern[i]
		i++
		return Outcome{OK: ok, Hint: "fix it"}
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil,
		WithLoopTurnValidator(v, 1), // budget of 1 retry per consecutive failure streak
		WithLoopMaxTurns(10),
	)

	// We need a second user turn to exercise the reset semantics. Run twice
	// against the same history so the second call sees the reset budget.
	res, _ := lp.Run(context.Background(), "first")
	res2, _ := lp.Run(context.Background(), "second", WithHistory(res.History))

	if res2.Status != "completed" {
		t.Errorf("second run should complete after reset+retry, got %q", res2.Status)
	}
}

// TestValidator_StreamingPath_RejectThenAccept asserts the validator also
// fires in the Iterator (streaming) path used by Agent.Run — not just the
// non-streaming Run() path. The first turn is rejected, hint goes in, the
// second turn passes.
func TestValidator_StreamingPath_RejectThenAccept(t *testing.T) {
	prov := &mockProvider{
		model: "mock",
		responses: []*provider.LLMResponse{
			{Content: "first", IsFinal: true},
			{Content: "second", IsFinal: true},
		},
	}
	v := &scriptedValidator{}
	calls := 0
	v.verdict = func(_ TurnSnapshot) Outcome {
		calls++
		if calls == 1 {
			return Outcome{OK: false, Reason: "stream-r1", Hint: "redo it"}
		}
		return Outcome{OK: true}
	}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil,
		WithLoopTurnValidator(v, 2))

	it := lp.Iterate(context.Background(), "hi")
	for range it.Next() { //nolint:revive // drain only
	}
	res := it.Result()
	if res.Output != "second" {
		t.Errorf("streaming path: expected second-turn output, got %q", res.Output)
	}
	if res.Status != "completed" {
		t.Errorf("streaming path: expected completed status, got %q", res.Status)
	}
	if calls != 2 {
		t.Errorf("expected 2 validator calls in streaming path, got %d", calls)
	}
}

// TestValidator_NilDefault asserts that omitting WithLoopTurnValidator
// keeps legacy behavior — no validation, no retry, no hint injection.
func TestValidator_NilDefault(t *testing.T) {
	prov := &mockProvider{
		model:     "mock",
		responses: []*provider.LLMResponse{{Content: "ok", IsFinal: true}},
	}
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, nil)
	res, err := lp.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "ok" {
		t.Errorf("legacy path broken: got %q", res.Output)
	}
	if res.Status != "completed" {
		t.Errorf("legacy status broken: got %q", res.Status)
	}
}

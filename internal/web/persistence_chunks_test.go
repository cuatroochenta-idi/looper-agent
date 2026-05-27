package web

import (
	"testing"
	"time"
)

func TestStripChunkSteps(t *testing.T) {
	now := time.Now()
	in := []TimelineStep{
		{Kind: StepKindUserInput, At: now, Content: "hi"},
		{Kind: StepKindLLMCall, At: now, Turn: 1},
		{Kind: StepKindStreamingChunk, At: now, Turn: 1, Content: "He"},
		{Kind: StepKindStreamingChunk, At: now, Turn: 1, Content: "llo"},
		{Kind: StepKindReasoning, At: now, Turn: 1, Content: "..."},
		{Kind: StepKindLLMResponse, At: now, Turn: 1, Content: "Hello"},
		{Kind: StepKindFinal, At: now, Turn: 1, Content: "Hello"},
	}
	out := stripChunkSteps(in)
	if len(out) != 4 {
		t.Fatalf("stripChunkSteps kept %d steps, want 4", len(out))
	}
	for _, s := range out {
		if s.Kind == StepKindStreamingChunk || s.Kind == StepKindReasoning {
			t.Fatalf("found chunk step in stripped output: %+v", s)
		}
	}
	// The original slice must not be mutated — callers depend on the
	// live in-memory store keeping its full streaming history for the UI.
	for i, s := range in {
		if i == 2 || i == 3 {
			if s.Kind != StepKindStreamingChunk {
				t.Fatalf("input slice mutated at index %d", i)
			}
		}
	}
}

func TestStripChunkStepsNoOpWhenClean(t *testing.T) {
	in := []TimelineStep{
		{Kind: StepKindUserInput},
		{Kind: StepKindLLMResponse, Content: "ok"},
	}
	out := stripChunkSteps(in)
	if &out[0] != &in[0] {
		t.Fatalf("expected stripChunkSteps to return the input slice when no chunks present (cheap path)")
	}
}

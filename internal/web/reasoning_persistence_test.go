package web

import "testing"

// TestReasoningSurvivesChunkStripping is the whole point of moving the think
// onto the usage-bearing steps: stripChunkSteps drops every reasoning_chunk
// before a run is written to disk, so a trace reloaded later would show no
// thinking at all if that were the only copy.
func TestReasoningSurvivesChunkStripping(t *testing.T) {
	steps := []TimelineStep{
		{Kind: StepKindLLMCall, Turn: 0},
		{Kind: StepKindReasoning, Turn: 0, Content: "I need "},
		{Kind: StepKindReasoning, Turn: 0, Content: "the time"},
		{Kind: StepKindLLMResponse, Turn: 0, Reasoning: "I need the time", FirstChunkMs: 700, LatencyMs: 12300, InputTokens: 10, OutputTokens: 4},
		{Kind: StepKindToolCall, Turn: 0, ToolName: "clock", ToolCallID: "c1", Reasoning: "I need the time"},
		{Kind: StepKindToolResult, Turn: 0, ToolCallID: "c1", Content: "12:00"},
	}

	kept := stripChunkSteps(steps)
	for _, s := range kept {
		if s.Kind == StepKindReasoning {
			t.Fatal("stripChunkSteps kept a reasoning_chunk")
		}
	}

	tl := BuildTimeline(kept)
	if len(tl.Turns) != 1 {
		t.Fatalf("built %d turns, want 1", len(tl.Turns))
	}
	turn := tl.Turns[0]
	if turn.Reasoning != "I need the time" {
		t.Errorf("turn.Reasoning = %q, want the think lifted off the llm_response step", turn.Reasoning)
	}
	if turn.FirstChunkMs != 700 || turn.LatencyMs != 12300 {
		t.Errorf("turn timings = %d/%d, want 700/12300", turn.FirstChunkMs, turn.LatencyMs)
	}
}

// TestLiveReasoningChunksAreNotDoubled guards the other direction: while a
// run is live both the per-delta chunks and the whole-think step are present,
// and the turn must show the think once.
func TestLiveReasoningChunksAreNotDoubled(t *testing.T) {
	tl := BuildTimeline([]TimelineStep{
		{Kind: StepKindReasoning, Turn: 0, Content: "I need "},
		{Kind: StepKindReasoning, Turn: 0, Content: "the time"},
		{Kind: StepKindLLMResponse, Turn: 0, Reasoning: "I need the time"},
	})
	if len(tl.Turns) != 1 {
		t.Fatalf("built %d turns, want 1", len(tl.Turns))
	}
	if got := tl.Turns[0].Reasoning; got != "I need the time" {
		t.Errorf("turn.Reasoning = %q, want it once", got)
	}
}

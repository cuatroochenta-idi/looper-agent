package looper

import (
	"encoding/json"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// TestStepDataFrom_CarriesReasoningAndLatency pins the trace wire format:
// the whole think and the call's timings survive the loop.Step → StepData
// conversion, because the per-delta reasoning_chunk events are dropped
// before a run is persisted and this is the only copy left.
func TestStepDataFrom_CarriesReasoningAndLatency(t *testing.T) {
	usage := provider.Usage{InputTokens: 100, OutputTokens: 40}
	step := loop.Step{
		Type:         loop.StepToolCall,
		Turn:         2,
		ToolName:     "clock",
		ToolCallID:   "call_1",
		ProviderID:   "openrouter",
		ModelID:      "deepseek/deepseek-v4.1-flash",
		Reasoning:    "I need the time before answering",
		FirstChunkMs: 740,
		LatencyMs:    12300,
		Usage:        &usage,
	}

	out := stepDataFrom(step)

	if out.Reasoning != "I need the time before answering" {
		t.Errorf("Reasoning = %q", out.Reasoning)
	}
	if out.FirstChunkMs != 740 {
		t.Errorf("FirstChunkMs = %d, want 740", out.FirstChunkMs)
	}
	if out.LatencyMs != 12300 {
		t.Errorf("LatencyMs = %d, want 12300", out.LatencyMs)
	}
}

// TestStepDataFrom_OmitsEmptyReasoningAndLatency keeps the wire quiet for
// the steps that carry none of it — every consumer decodes these payloads.
func TestStepDataFrom_OmitsEmptyReasoningAndLatency(t *testing.T) {
	raw, err := json.Marshal(stepDataFrom(loop.Step{Type: loop.StepToolResult, Turn: 1}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"Reasoning", "first_chunk_ms", "latency_ms"} {
		if _, present := decoded[key]; present {
			t.Errorf("empty step serialized %q: %s", key, raw)
		}
	}
}

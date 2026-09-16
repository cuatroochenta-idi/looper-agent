package looper

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

// thinkingProvider streams a burst of reasoning deltas before its answer,
// the shape an open model produces with reasoning surfaced.
type thinkingProvider struct {
	deltas int
}

func (p *thinkingProvider) Model() string                   { return "thinking" }
func (p *thinkingProvider) Translator() provider.Translator { return nil }

func (p *thinkingProvider) Chat(context.Context, provider.LLMRequest) (*provider.LLMResponse, error) {
	return &provider.LLMResponse{Content: "done", Reasoning: "whole think", IsFinal: true, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func (p *thinkingProvider) ChatStream(context.Context, provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, p.deltas+1)
	go func() {
		defer close(ch)
		for i := 0; i < p.deltas; i++ {
			ch <- provider.StreamChunk{Reasoning: "."}
		}
		ch <- provider.StreamChunk{Content: "done", Reasoning: "whole think", IsFinal: true, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}}
	}()
	return ch, nil
}

// recordingSink keeps every trace event it receives.
type recordingSink struct {
	mu     sync.Mutex
	events []TraceEvent
}

func (s *recordingSink) TraceEvent(ev TraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func TestIterate_ReasoningDeltasNeverReachTheTraceSink(t *testing.T) {
	// More deltas than the trace writer's queue holds: forwarding them
	// would evict the response step that follows.
	const deltas = 3000
	sink := &recordingSink{}
	agent := MustNewAgent(&thinkingProvider{deltas: deltas}, "think out loud", WithTraceSink(sink))

	it := agent.Iterate(context.Background(), "go")
	for range it.Next() {
	}
	if res := it.Result(); res.Output != "done" {
		t.Fatalf("output = %q, want done", res.Output)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var responses, chunks int
	for _, ev := range sink.events {
		if ev.Type != TraceStep {
			continue
		}
		var d StepData
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("decode step: %v", err)
		}
		switch d.Kind {
		case string(loop.StepReasoningChunk), string(loop.StepStreamingChunk):
			chunks++
		case string(loop.StepLLMResponse), string(loop.StepFinalResponse):
			responses++
			if d.Reasoning != "whole think" {
				t.Fatalf("response step lost the think: %+v", d)
			}
		}
	}
	if chunks != 0 {
		t.Fatalf("%d delta steps reached the sink", chunks)
	}
	if responses == 0 {
		t.Fatal("no response step reached the sink")
	}
}

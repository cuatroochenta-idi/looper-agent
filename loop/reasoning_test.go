package loop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// thinkingProvider answers one tool-calling turn carrying reasoning, then a
// plain final. firstChunkDelay stalls the stream before its first chunk so
// the latency assertions have something to measure.
type thinkingProvider struct {
	calls           int
	details         json.RawMessage
	stream          bool
	firstChunkDelay time.Duration
}

func (p *thinkingProvider) Model() string                   { return "thinker" }
func (p *thinkingProvider) Translator() provider.Translator { return nil }

func (p *thinkingProvider) Chat(_ context.Context, _ provider.LLMRequest) (*provider.LLMResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &provider.LLMResponse{
			Content:          "",
			Reasoning:        "the user wants the time, so I call the clock",
			ReasoningDetails: p.details,
			ToolCalls: []message.ToolCall{{
				ID: "call_1", Name: "clock", Arguments: json.RawMessage(`{}`),
			}},
			Usage: provider.Usage{InputTokens: 10, OutputTokens: 4},
		}, nil
	}
	return &provider.LLMResponse{Content: "it is noon", IsFinal: true, Usage: provider.Usage{InputTokens: 12, OutputTokens: 3}}, nil
}

func (p *thinkingProvider) ChatStream(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	if !p.stream {
		return nil, context.Canceled // force the loop's non-streaming fallback
	}
	resp, _ := p.Chat(ctx, req)
	ch := make(chan provider.StreamChunk, 4)
	go func() {
		defer close(ch)
		if p.firstChunkDelay > 0 {
			time.Sleep(p.firstChunkDelay)
		}
		if resp.Reasoning != "" {
			// Two deltas, so the test also proves the loop concatenates
			// rather than keeping only the last one.
			ch <- provider.StreamChunk{Reasoning: resp.Reasoning[:10]}
			ch <- provider.StreamChunk{Reasoning: resp.Reasoning[10:]}
		}
		ch <- provider.StreamChunk{
			Content:          resp.Content,
			ToolCalls:        resp.ToolCalls,
			Reasoning:        resp.Reasoning,
			ReasoningDetails: resp.ReasoningDetails,
			IsFinal:          resp.IsFinal || len(resp.ToolCalls) > 0,
			Usage:            &resp.Usage,
		}
	}()
	return ch, nil
}

func clockTool(t *testing.T) *tool.Tool {
	t.Helper()
	return tool.MustNewTool(struct{}{},
		func(_ context.Context, _ struct{}) (string, error) { return "12:00", nil },
		tool.ToolConfig{Name: "clock", Description: "tells the time"})
}

// TestIterator_StreamingCarriesReasoningIntoHistoryAndSteps is the
// interleaved-thinking contract at the loop layer: the thinking that came
// back with a tool-calling turn is stored on the assistant message (so the
// provider can echo it) and stamped on the turn's steps (so the trace keeps
// it after the chunk steps are stripped).
func TestIterator_StreamingCarriesReasoningIntoHistoryAndSteps(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"plan","signature":"sha256:keep"}]`)
	prov := &thinkingProvider{details: details, stream: true, firstChunkDelay: 15 * time.Millisecond}

	hist := message.NewHistory()
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, []*tool.Tool{clockTool(t)},
		WithLoopMaxTurns(3))

	it := lp.Iterate(context.Background(), "what time is it", WithHistory(hist))

	var toolStep *Step
	var llmRespStep *Step
	var chunkText string
	for s := range it.Next() {
		step := s
		switch step.Type {
		case StepToolCall:
			if toolStep == nil {
				toolStep = &step
			}
		case StepLLMResponse:
			if llmRespStep == nil {
				llmRespStep = &step
			}
		case StepReasoningChunk:
			chunkText += step.Content
		}
	}

	const wantReasoning = "the user wants the time, so I call the clock"

	if chunkText != wantReasoning {
		t.Errorf("live reasoning deltas = %q, want %q", chunkText, wantReasoning)
	}
	if toolStep == nil {
		t.Fatal("no StepToolCall emitted")
	}
	if toolStep.Reasoning != wantReasoning {
		t.Errorf("StepToolCall.Reasoning = %q, want %q", toolStep.Reasoning, wantReasoning)
	}
	if llmRespStep == nil {
		t.Fatal("no StepLLMResponse emitted")
	}
	if llmRespStep.Reasoning != wantReasoning {
		t.Errorf("StepLLMResponse.Reasoning = %q", llmRespStep.Reasoning)
	}

	var assistant *message.Message
	for _, m := range hist.Messages() {
		if m.Type == message.MessageAssistant && len(m.ToolCalls) > 0 {
			cp := m
			assistant = &cp
			break
		}
	}
	if assistant == nil {
		t.Fatal("no assistant tool-call message in history")
	}
	if assistant.Reasoning != wantReasoning {
		t.Errorf("history assistant Reasoning = %q", assistant.Reasoning)
	}
	if string(assistant.ReasoningDetails) != string(details) {
		t.Errorf("history assistant ReasoningDetails = %s, want %s", assistant.ReasoningDetails, details)
	}
}

// TestIterator_StreamingRecordsLatency checks the per-call timings ride on
// the same usage-bearing steps.
func TestIterator_StreamingRecordsLatency(t *testing.T) {
	prov := &thinkingProvider{stream: true, firstChunkDelay: 20 * time.Millisecond}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, []*tool.Tool{clockTool(t)},
		WithLoopMaxTurns(3))
	it := lp.Iterate(context.Background(), "what time is it")

	var seen int
	for s := range it.Next() {
		if s.Type != StepLLMResponse {
			continue
		}
		seen++
		if s.FirstChunkMs <= 0 {
			t.Errorf("StepLLMResponse.FirstChunkMs = %d, want > 0", s.FirstChunkMs)
		}
		if s.LatencyMs < s.FirstChunkMs {
			t.Errorf("LatencyMs (%d) < FirstChunkMs (%d)", s.LatencyMs, s.FirstChunkMs)
		}
	}
	if seen == 0 {
		t.Fatal("no StepLLMResponse emitted")
	}
}

// TestIterator_NonStreamingCarriesReasoning covers the fallback path, where
// time-to-first-chunk and total latency are by definition the same figure.
func TestIterator_NonStreamingCarriesReasoning(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"plan"}]`)
	prov := &thinkingProvider{details: details, stream: false}

	hist := message.NewHistory()
	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, []*tool.Tool{clockTool(t)},
		WithLoopMaxTurns(3))
	it := lp.Iterate(context.Background(), "what time is it", WithHistory(hist))

	var toolStep *Step
	for s := range it.Next() {
		if s.Type == StepToolCall && toolStep == nil {
			step := s
			toolStep = &step
		}
	}
	if toolStep == nil {
		t.Fatal("no StepToolCall emitted")
	}
	if toolStep.Reasoning == "" {
		t.Error("non-streaming StepToolCall lost the reasoning")
	}
	if toolStep.FirstChunkMs != toolStep.LatencyMs {
		t.Errorf("non-streaming FirstChunkMs (%d) != LatencyMs (%d)", toolStep.FirstChunkMs, toolStep.LatencyMs)
	}

	for _, m := range hist.Messages() {
		if m.Type == message.MessageAssistant && len(m.ToolCalls) > 0 {
			if string(m.ReasoningDetails) != string(details) {
				t.Errorf("history lost the details: %s", m.ReasoningDetails)
			}
			return
		}
	}
	t.Fatal("no assistant tool-call message in history")
}

// TestAgentLoopRun_StoresReasoningOnAssistantMessage covers the blocking
// Run path, which has no steps to stamp but still owns the history.
func TestAgentLoopRun_StoresReasoningOnAssistantMessage(t *testing.T) {
	details := json.RawMessage(`[{"type":"reasoning.text","text":"plan"}]`)
	prov := &thinkingProvider{details: details}

	lp := NewAgentLoop(prov, func(_ context.Context) string { return "p" }, []*tool.Tool{clockTool(t)},
		WithLoopMaxTurns(3))

	res, err := lp.Run(context.Background(), "what time is it")
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, m := range res.History.Messages() {
		if m.Type == message.MessageAssistant && len(m.ToolCalls) > 0 {
			if m.Reasoning == "" {
				t.Error("Run dropped the reasoning text")
			}
			if string(m.ReasoningDetails) != string(details) {
				t.Errorf("Run dropped the details: %s", m.ReasoningDetails)
			}
			return
		}
	}
	t.Fatal("no assistant tool-call message in history")
}

// TestAddAssistantTurn_KeepsPlainMessagesPlain pins the additive contract:
// a turn with no thinking still produces the message the old constructor
// made, so nothing new appears in existing traces or stored histories.
func TestAddAssistantTurn_KeepsPlainMessagesPlain(t *testing.T) {
	h := message.NewHistory()
	addAssistantTurn(h, "answer", nil, "", nil)

	msgs := h.Messages()
	if len(msgs) != 1 {
		t.Fatalf("history has %d messages, want 1", len(msgs))
	}
	if msgs[0].Reasoning != "" || msgs[0].ReasoningDetails != nil {
		t.Errorf("plain turn grew reasoning fields: %q / %s", msgs[0].Reasoning, msgs[0].ReasoningDetails)
	}
}

package looper

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

type publicMemoryProvider struct {
	calls atomic.Int32
}

func (p *publicMemoryProvider) Model() string                   { return "memory-test" }
func (p *publicMemoryProvider) Translator() provider.Translator { return nil }
func (p *publicMemoryProvider) Chat(context.Context, provider.LLMRequest) (*provider.LLMResponse, error) {
	p.calls.Add(1)
	return &provider.LLMResponse{Content: "must not run", IsFinal: true}, nil
}
func (p *publicMemoryProvider) ChatStream(context.Context, provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	p.calls.Add(1)
	return nil, errors.New("must not run")
}

type publicFailingMemory struct{ err error }

func (m publicFailingMemory) Manage(context.Context, *message.History) error { return m.err }

func TestAgentRunMemoryFailurePreservesResultCauseAndJournal(t *testing.T) {
	providerStub := &publicMemoryProvider{}
	want := &memory.MemoryBudgetExceededError{Budget: 10, EstimatedTokens: 11}
	agent := MustNewAgent(providerStub, "system", WithAgentMemory(publicFailingMemory{err: want}))

	result, err := agent.Run(context.Background(), "request")
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want cause %v", err, want)
	}
	if result == nil || result.Status != "usage_exceeded" {
		t.Fatalf("result = %+v, want usage_exceeded result", result)
	}
	if providerStub.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerStub.calls.Load())
	}
	if len(result.NewMessages) != 1 || result.NewMessages[0].Content != "request" {
		t.Fatalf("new messages = %+v, want appended request", result.NewMessages)
	}
}

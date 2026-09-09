package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

type failingMemoryManager struct {
	err error
}

func (m failingMemoryManager) Manage(context.Context, *message.History) error {
	return m.err
}

func TestRunMemoryBudgetFailureStopsBeforeProviderAndPreservesJournal(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: []*provider.LLMResponse{{Content: "must not run", IsFinal: true}}}
	want := &memory.MemoryBudgetExceededError{Budget: 10, EstimatedTokens: 11}
	l := NewAgentLoop(providerStub, func(context.Context) string { return "system" }, nil,
		WithLoopMemory(failingMemoryManager{err: want}))

	result, err := l.Run(context.Background(), "request")
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want cause %v", err, want)
	}
	if result == nil || result.Status != "usage_exceeded" {
		t.Fatalf("result = %+v, want usage_exceeded result", result)
	}
	if providerStub.callCount != 0 {
		t.Fatalf("provider calls = %d, want zero", providerStub.callCount)
	}
	if len(result.NewMessages) != 1 || result.NewMessages[0].Content != "request" {
		t.Fatalf("new messages = %+v, want appended request", result.NewMessages)
	}
}

func TestRunOtherMemoryFailureStopsBeforeProviderWithErrorStatus(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: []*provider.LLMResponse{{Content: "must not run", IsFinal: true}}}
	want := errors.New("memory backend offline")
	l := NewAgentLoop(providerStub, func(context.Context) string { return "system" }, nil,
		WithLoopMemory(failingMemoryManager{err: want}))

	result, err := l.Run(context.Background(), "request")
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want cause %v", err, want)
	}
	if result == nil || result.Status != "error" {
		t.Fatalf("result = %+v, want error result", result)
	}
	if providerStub.callCount != 0 {
		t.Fatalf("provider calls = %d, want zero", providerStub.callCount)
	}
}

func TestIteratorMemoryBudgetFailureStopsBeforeProviderAndPreservesJournal(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: []*provider.LLMResponse{{Content: "must not run", IsFinal: true}}}
	want := &memory.MemoryBudgetExceededError{Budget: 10, EstimatedTokens: 11}
	l := NewAgentLoop(providerStub, func(context.Context) string { return "system" }, nil,
		WithLoopMemory(failingMemoryManager{err: want}))

	it := l.Iterate(context.Background(), "request")
	var stepErr error
	for step := range it.Next() {
		if step.Type == StepError && step.Error != nil {
			stepErr = step.Error
		}
	}
	result := it.Result()
	if !errors.Is(stepErr, want) {
		t.Fatalf("step error = %v, want cause %v", stepErr, want)
	}
	if result.Status != "usage_exceeded" {
		t.Fatalf("result status = %q, want usage_exceeded", result.Status)
	}
	if providerStub.callCount != 0 {
		t.Fatalf("provider calls = %d, want zero", providerStub.callCount)
	}
	if len(result.NewMessages) != 1 || result.NewMessages[0].Content != "request" {
		t.Fatalf("new messages = %+v, want appended request", result.NewMessages)
	}
}

func TestIteratorOtherMemoryFailureStopsBeforeProviderWithErrorStatus(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: []*provider.LLMResponse{{Content: "must not run", IsFinal: true}}}
	want := errors.New("memory backend offline")
	l := NewAgentLoop(providerStub, func(context.Context) string { return "system" }, nil,
		WithLoopMemory(failingMemoryManager{err: want}))

	it := l.Iterate(context.Background(), "request")
	var stepErr error
	for step := range it.Next() {
		if step.Type == StepError && step.Error != nil {
			stepErr = step.Error
		}
	}
	result := it.Result()
	if !errors.Is(stepErr, want) {
		t.Fatalf("step error = %v, want cause %v", stepErr, want)
	}
	if result.Status != "error" {
		t.Fatalf("result status = %q, want error", result.Status)
	}
	if providerStub.callCount != 0 {
		t.Fatalf("provider calls = %d, want zero", providerStub.callCount)
	}
}

type compactOnSecondMemory struct {
	calls atomic.Int32
}

type failOnSecondMemory struct {
	calls atomic.Int32
	err   error
}

func (m *failOnSecondMemory) Manage(_ context.Context, _ *message.History) error {
	if m.calls.Add(1) == 2 {
		return m.err
	}
	return nil
}

func (m *compactOnSecondMemory) Manage(_ context.Context, history *message.History) error {
	if m.calls.Add(1) != 2 {
		return nil
	}
	compacted := message.NewHistory()
	compacted.AddSystemMessage("compacted")
	raw, err := json.Marshal(compacted.Messages())
	if err != nil {
		return err
	}
	return history.UnmarshalJSON(raw)
}

func newCompactionLoop(providerStub provider.LLMProvider, memoryManager memory.MemoryManager) *AgentLoop {
	return NewAgentLoop(providerStub, func(context.Context) string { return "system" }, nil,
		WithLoopMemory(memoryManager), WithLoopMaxTurns(3))
}

func compactionResponses() []*provider.LLMResponse {
	return []*provider.LLMResponse{
		{ToolCalls: []message.ToolCall{{ID: "missing", Name: "missing", Arguments: json.RawMessage(`{}`)}}, IsFinal: true},
		{Content: "done", IsFinal: true},
	}
}

func TestRunJournalExcludesMemoryCompaction(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: compactionResponses()}
	result, err := newCompactionLoop(providerStub, &compactOnSecondMemory{}).Run(context.Background(), "request")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.NewMessages) != 4 {
		t.Fatalf("new messages = %d, want input + assistant/tool + final assistant: %+v", len(result.NewMessages), result.NewMessages)
	}
	if result.NewMessages[0].Content != "request" || result.NewMessages[3].Content != "done" {
		t.Fatalf("journal = %+v", result.NewMessages)
	}
}

func TestRunMemoryFailurePreservesPriorUsageAndProviders(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: []*provider.LLMResponse{
		{ToolCalls: []message.ToolCall{{ID: "missing", Name: "missing", Arguments: json.RawMessage(`{}`)}}, Usage: provider.Usage{InputTokens: 4, OutputTokens: 3}},
		{Content: "must not run", IsFinal: true, Usage: provider.Usage{InputTokens: 20, OutputTokens: 20}},
	}}
	want := &memory.MemoryBudgetExceededError{Budget: 10, EstimatedTokens: 11}
	result, err := newCompactionLoop(providerStub, &failOnSecondMemory{err: want}).Run(context.Background(), "request")
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want cause %v", err, want)
	}
	if result == nil || result.Status != "usage_exceeded" {
		t.Fatalf("result = %+v, want usage_exceeded result", result)
	}
	if result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want prior provider usage", result.Usage)
	}
	if len(result.Providers) != 1 {
		t.Fatalf("providers = %+v, want prior provider bucket", result.Providers)
	}
	if providerStub.callCount != 1 {
		t.Fatalf("provider calls = %d, want one prior call", providerStub.callCount)
	}
}

func TestIteratorJournalExcludesMemoryCompaction(t *testing.T) {
	providerStub := &mockProvider{model: "memory-test", responses: compactionResponses()}
	manager := &compactOnSecondMemory{}
	it := newCompactionLoop(providerStub, manager).Iterate(context.Background(), "request")
	for range it.Next() {
	}
	result := it.Result()
	if providerStub.callCount != 2 || manager.calls.Load() != 2 {
		t.Fatalf("provider calls=%d memory calls=%d result=%+v", providerStub.callCount, manager.calls.Load(), result)
	}
	if result.Status != "completed" {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if len(result.NewMessages) != 3 {
		t.Fatalf("new messages = %d, want input + assistant/tool: %+v", len(result.NewMessages), result.NewMessages)
	}
}

package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type budgetTestProvider struct {
	calls     atomic.Int32
	usage     Usage
	streamErr error
}

func (p *budgetTestProvider) Model() string                { return "test" }
func (p *budgetTestProvider) Translator() Translator       { return nil }
func (p *budgetTestProvider) SupportsResponseFormat() bool { return true }
func (p *budgetTestProvider) Chat(context.Context, LLMRequest) (*LLMResponse, error) {
	p.calls.Add(1)
	return &LLMResponse{Usage: p.usage, Content: "answer"}, nil
}
func (p *budgetTestProvider) ChatStream(context.Context, LLMRequest) (<-chan StreamChunk, error) {
	p.calls.Add(1)
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Usage: &p.usage, Content: "answer", Error: p.streamErr, IsFinal: true}
	close(ch)
	return ch, nil
}

func TestSharedBudgetCountsChildrenAndAuxiliaryCalls(t *testing.T) {
	b := NewSharedBudget(SharedBudgetLimits{MaxTotalTokens: 10})
	ctx := WithSharedBudget(context.Background(), b)
	parent, child := &budgetTestProvider{usage: Usage{InputTokens: 3}}, &budgetTestProvider{usage: Usage{OutputTokens: 7}}
	if _, err := NewBudgetProvider(parent).Chat(ctx, LLMRequest{}); err != nil {
		t.Fatal(err)
	}
	stream, err := NewBudgetProvider(child).ChatStream(ctx, LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var terminal error
	for c := range stream {
		terminal = c.Error
	}
	var exhausted *SharedBudgetExceededError
	if !errors.As(terminal, &exhausted) {
		t.Fatalf("terminal error = %v", terminal)
	}
	if _, err := NewBudgetProvider(parent).Chat(ctx, LLMRequest{}); !errors.As(err, &exhausted) {
		t.Fatalf("retry error=%v", err)
	}
	if parent.calls.Load() != 1 || child.calls.Load() != 1 {
		t.Fatal("budget allowed another provider call")
	}
	if got := b.Snapshot(); got.Requests != 2 || got.Usage.InputTokens+got.Usage.OutputTokens != 10 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestSharedBudgetConcurrentReservationIsAtomic(t *testing.T) {
	b := NewSharedBudget(SharedBudgetLimits{MaxRequests: 3})
	ctx := WithSharedBudget(context.Background(), b)
	p := &budgetTestProvider{}
	wrapped := NewBudgetProvider(p)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = wrapped.Chat(ctx, LLMRequest{}) }()
	}
	wg.Wait()
	if p.calls.Load() != 3 || b.Snapshot().Requests != 3 {
		t.Fatalf("calls=%d snapshot=%+v", p.calls.Load(), b.Snapshot())
	}
}

func TestSharedBudgetWrappersAreTransparentAndDoNotDoubleCount(t *testing.T) {
	p := &budgetTestProvider{usage: Usage{InputTokens: 4, CachedTokens: 3}}
	b := NewSharedBudget(SharedBudgetLimits{})
	w := NewBudgetProvider(NewBudgetProvider(p))
	if !SupportsNativeResponseFormat(w) {
		t.Fatal("lost native format capability")
	}
	if _, err := w.Chat(WithSharedBudget(context.Background(), b), LLMRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := b.Snapshot(); got.Requests != 1 || got.Usage.InputTokens != 4 || got.Usage.CachedTokens != 3 {
		t.Fatalf("double counting: %+v", got)
	}
	if _, err := w.Chat(context.Background(), LLMRequest{}); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().Requests != 1 {
		t.Fatal("budget leaked outside context")
	}
}

func TestSharedBudgetRecordsFailedStreamUsage(t *testing.T) {
	p := &budgetTestProvider{usage: Usage{InputTokens: 5}, streamErr: errors.New("stream failed")}
	b := NewSharedBudget(SharedBudgetLimits{MaxTotalTokens: 100})
	ch, err := NewBudgetProvider(p).ChatStream(WithSharedBudget(context.Background(), b), LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for c := range ch {
		if !errors.Is(c.Error, p.streamErr) {
			t.Fatalf("lost cause: %v", c.Error)
		}
	}
	if b.Snapshot().Usage.InputTokens != 5 {
		t.Fatal("failed call was not charged")
	}
}

func TestSharedBudgetExhaustionIsNotRetriedByNumericText(t *testing.T) {
	err := &SharedBudgetExceededError{Limit: sharedTokenLimit, Maximum: 500000, Used: 500001}
	if got := DefaultRetryClassifier(err); got != Permanent {
		t.Fatalf("budget classified as %v", got)
	}
}

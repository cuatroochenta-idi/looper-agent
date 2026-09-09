package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func TestSharedBudgetExhaustionIsTerminalAcrossIterators(t *testing.T) {
	b := provider.NewSharedBudget(provider.SharedBudgetLimits{MaxTotalTokens: 10})
	ctx := provider.WithSharedBudget(context.Background(), b)
	prov := &limitProvider{mockProvider: mockProvider{model: "test", responses: []*provider.LLMResponse{{Content: "done", IsFinal: true}}}, tokensPerCall: 5}
	lp := NewAgentLoop(provider.NewBudgetProvider(prov), func(context.Context) string { return "test" }, nil)
	for i := 0; i < 2; i++ {
		it := lp.Iterate(ctx, "continue")
		var gotErr error
		for step := range it.Next() {
			if step.Type == StepError {
				gotErr = step.Error
			}
		}
		var exhausted *provider.SharedBudgetExceededError
		if !errors.As(gotErr, &exhausted) {
			t.Fatalf("attempt %d error = %v", i, gotErr)
		}
		if got := it.Result(); got.Status != "usage_exceeded" {
			t.Fatalf("attempt %d status = %s", i, got.Status)
		}
	}
	if got := b.Snapshot(); got.Requests != 1 || got.Usage.InputTokens+got.Usage.OutputTokens != 10 {
		t.Fatalf("budget reset or double counted: %+v", got)
	}
}

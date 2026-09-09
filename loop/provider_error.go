package loop

import (
	"errors"

	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/provider"
)

func providerErrorStatus(err error) string {
	var memoryBudgetErr *memory.MemoryBudgetExceededError
	if errors.As(err, &memoryBudgetErr) {
		return "usage_exceeded"
	}
	var budgetErr *provider.SharedBudgetExceededError
	if errors.As(err, &budgetErr) {
		return "usage_exceeded"
	}
	return "error"
}

func (it *Iterator) recordProviderError(turn int, err error) {
	it.resMu.Lock()
	defer it.resMu.Unlock()
	it.turns = turn + 1
	it.status = providerErrorStatus(err)
}

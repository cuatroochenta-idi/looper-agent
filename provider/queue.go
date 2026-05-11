package provider

import (
	"context"
	"fmt"
	"sync/atomic"
)

// KeyRotationStrategy determines how API keys are rotated.
type KeyRotationStrategy int

const (
	// RotationRoundRobin cycles through keys sequentially.
	RotationRoundRobin KeyRotationStrategy = iota

	// RotationRandom selects keys randomly.
	RotationRandom
)

// ProviderQueue provides failover across multiple LLM providers.
// If the primary provider fails, the next in the queue takes over.
// Also supports API key rotation for providers with multiple keys.
type ProviderQueue struct {
	providers []LLMProvider
	index     atomic.Uint64
}

// NewProviderQueue creates a provider queue with failover support.
func NewProviderQueue(providers ...LLMProvider) *ProviderQueue {
	return &ProviderQueue{
		providers: providers,
	}
}

// Execute tries each provider in order until one succeeds.
// If all providers fail, returns the last error.
func (q *ProviderQueue) Execute(ctx context.Context, fn func(LLMProvider) error) error {
	if len(q.providers) == 0 {
		return fmt.Errorf("no providers in queue")
	}

	var lastErr error
	for _, p := range q.providers {
		if err := fn(p); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("all providers failed: %w", lastErr)
}

// Next returns the next provider in round-robin order.
func (q *ProviderQueue) Next() LLMProvider {
	idx := q.index.Add(1) % uint64(len(q.providers))
	return q.providers[idx]
}

// Len returns the number of providers in the queue.
func (q *ProviderQueue) Len() int {
	return len(q.providers)
}

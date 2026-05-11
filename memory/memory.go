// Package memory provides configurable memory management strategies
// to prevent token overflow in long-running conversations.
//
// Strategies include sliding window (keep last N messages), summarization
// (compress old messages via LLM), and token budget (hybrid approach).
package memory

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// Strategy identifies the memory management strategy.
type Strategy string

const (
	// StrategySlidingWindow keeps only the last N messages or tokens.
	StrategySlidingWindow Strategy = "sliding_window"

	// StrategySummarization compresses old messages into a summary.
	StrategySummarization Strategy = "summarization"

	// StrategyTokenBudget combines sliding window with summarization
	// when the token budget is exceeded.
	StrategyTokenBudget Strategy = "token_budget"
)

// MemoryManager controls the size of the conversation history to prevent
// token overflow. It is invoked before each LLM call in the agentic loop.
type MemoryManager interface {
	// Manage trims or compresses the history to fit within constraints.
	// The implementation is responsible for thread safety.
	Manage(ctx context.Context, history *message.History) error
}

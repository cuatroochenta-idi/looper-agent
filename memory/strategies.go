package memory

import (
	"context"
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// SlidingWindow keeps only the last N messages, discarding older ones.
// Simple and zero-cost strategy for linear conversations.
type SlidingWindow struct {
	// MaxMessages is the maximum number of messages to retain.
	MaxMessages int

	// MaxTokens is the approximate maximum tokens to retain.
	// If 0, only MaxMessages is used.
	MaxTokens int
}

// Manage trims the history to the sliding window.
func (s *SlidingWindow) Manage(_ context.Context, history *message.History) error {
	if s.MaxMessages > 0 {
		history.Truncate(s.MaxMessages)
	}
	return nil
}

// TokenBudget keeps messages within a token budget. When exceeded,
// older messages are summarized or truncated.
type TokenBudget struct {
	// Budget is the maximum approximate token count.
	Budget int

	// Summarizer compresses old messages when the budget is exceeded.
	// If nil, messages are simply truncated.
	Summarizer *Summarizer
}

// Manage ensures the history stays within the token budget.
func (t *TokenBudget) Manage(ctx context.Context, history *message.History) error {
	if t.Budget <= 0 {
		return nil
	}
	// Truncation-based implementation; summarization is a future enhancement
	if history.Len() > t.Budget/4 { // rough heuristic: ~4 tokens per message
		if t.Summarizer != nil {
			return t.Summarizer.summarize(ctx, history)
		}
		history.Truncate(t.Budget / 4)
	}
	return nil
}

// Summarizer compresses conversation history using an LLM call.
type Summarizer struct {
	// SummaryPrompt is injected before the summary request.
	SummaryPrompt string
}

// NewSummarizer creates a new summarizer.
func NewSummarizer(summaryPrompt string) *Summarizer {
	if summaryPrompt == "" {
		summaryPrompt = "Summarize the previous conversation concisely."
	}
	return &Summarizer{SummaryPrompt: summaryPrompt}
}

func (s *Summarizer) summarize(_ context.Context, history *message.History) error {
	// Placeholder: in production, this would call the LLM to generate a summary,
	// replace old messages with the summary as a system message.
	_ = history
	return fmt.Errorf("summarization not yet implemented")
}

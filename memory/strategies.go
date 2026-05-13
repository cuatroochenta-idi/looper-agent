package memory

import (
	"context"
	"fmt"
	"strings"

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

// Manage ensures the history stays within the token budget. When a
// Summarizer is configured it compacts older messages instead of
// truncating, preserving context the model still needs.
func (t *TokenBudget) Manage(ctx context.Context, history *message.History) error {
	if t.Budget <= 0 {
		return nil
	}
	// Rough heuristic: ~4 tokens per message. Once the message count
	// exceeds Budget/4 we compact (or truncate as fallback).
	if history.Len() > t.Budget/4 {
		if t.Summarizer != nil {
			return t.Summarizer.Summarize(ctx, history)
		}
		history.Truncate(t.Budget / 4)
	}
	return nil
}

// SummarizeFunc produces a single summary string from a slice of older
// messages. Implementations typically call out to an LLM but can be any
// pure function for testing or non-AI summarisers.
type SummarizeFunc func(ctx context.Context, messages []message.Message) (string, error)

// Summarizer compresses conversation history by replacing a window of
// older messages with a single system message carrying their summary.
// The user supplies the summarisation function so callers can pick a
// cheap model (or even a non-LLM heuristic) independent of the agent's
// main model.
type Summarizer struct {
	// SummaryPrompt is prepended to the summary text the framework
	// stores. Useful to label compacted history ("Summary so far: …").
	SummaryPrompt string

	// Fn is the user-supplied function that turns older messages into
	// a concise summary. Renamed from "Summarize" so it doesn't collide
	// with the method of the same name on this struct.
	Fn SummarizeFunc

	// KeepLast is the number of recent messages preserved verbatim.
	// Older messages get compacted. Defaults to 6 when zero.
	KeepLast int
}

// SummarizerOption configures a Summarizer at construction.
type SummarizerOption func(*Summarizer)

// WithKeepLast sets how many recent messages stay verbatim. Older
// messages get folded into the summary.
func WithKeepLast(n int) SummarizerOption {
	return func(s *Summarizer) { s.KeepLast = n }
}

// WithSummaryPrompt prepends a label to the stored summary, e.g.
// "[summary up to turn N]:". Mostly useful when the same agent has
// multiple summarisation passes.
func WithSummaryPrompt(prompt string) SummarizerOption {
	return func(s *Summarizer) { s.SummaryPrompt = prompt }
}

// NewSummarizer constructs a Summarizer with the supplied summarise
// function and options.
func NewSummarizer(fn SummarizeFunc, opts ...SummarizerOption) *Summarizer {
	s := &Summarizer{Fn: fn, KeepLast: 6}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Summarize replaces older messages in history with a single system
// message carrying the summary text. No-op when there are fewer
// messages than KeepLast.
func (s *Summarizer) Summarize(ctx context.Context, history *message.History) error {
	if s == nil || s.Fn == nil || history == nil {
		return nil
	}
	total := history.Len()
	if total <= s.KeepLast {
		return nil
	}
	msgs := history.Messages()
	older := msgs[:total-s.KeepLast]
	tail := msgs[total-s.KeepLast:]

	summary, err := s.Fn(ctx, older)
	if err != nil {
		return err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("summarizer: empty summary produced")
	}
	if s.SummaryPrompt != "" {
		summary = s.SummaryPrompt + "\n" + summary
	}

	// Rebuild history: summary first, then preserved tail.
	rebuilt := message.NewHistory()
	rebuilt.AddSystemMessage(summary)
	for _, m := range tail {
		rebuilt.AddMessage(m)
	}
	// Adopt rebuilt contents into the caller's history pointer so any
	// concurrent readers / writers stay coordinated with the same
	// underlying mutex.
	raw, _ := rebuilt.MarshalJSON()
	if err := history.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("summarizer: re-marshal history: %w", err)
	}
	return nil
}

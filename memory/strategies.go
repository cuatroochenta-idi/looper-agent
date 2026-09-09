package memory

import (
	"context"
	"encoding/json"
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

// MemoryBudgetExceededError reports that the messages that must be retained
// to keep the conversation valid are larger than the configured budget.
type MemoryBudgetExceededError struct {
	Budget          int
	EstimatedTokens int
}

func (e *MemoryBudgetExceededError) Error() string {
	return fmt.Sprintf("memory budget exceeded: estimated %d tokens, budget %d", e.EstimatedTokens, e.Budget)
}

const (
	// estimatedTokenBytes is a deliberately conservative bytes/4 heuristic for
	// ordinary text and JSON tool arguments. Tokenization is provider-specific,
	// so this is not universal or exact; it only decides when to compact.
	estimatedTokenBytes = 4
	// estimatedMessageOverhead covers role and framing tokens not represented
	// by message content or tool payloads.
	estimatedMessageOverhead = 4
)

// Manage ensures the history stays within the token budget. When a
// Summarizer is configured it compacts older messages instead of
// truncating, preserving context the model still needs.
func (t *TokenBudget) Manage(ctx context.Context, history *message.History) error {
	if t == nil || history == nil || t.Budget <= 0 {
		return nil
	}
	if estimated := estimateHistoryTokens(history.Messages()); estimated <= t.Budget {
		return nil
	}

	if t.Summarizer != nil {
		before := history.Messages()
		if err := t.Summarizer.Summarize(ctx, history); err != nil {
			return err
		}
		estimated := estimateHistoryTokens(history.Messages())
		if estimated > t.Budget {
			// A summary is allowed to fail the budget check, but it must not
			// silently discard the preserved anchors or leave an over-budget
			// history for the next provider call.
			_ = replaceHistory(history, before)
			return &MemoryBudgetExceededError{Budget: t.Budget, EstimatedTokens: estimated}
		}
		return nil
	}
	return trimHistoryToBudget(history, t.Budget)
}

func estimateHistoryTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

func estimateMessageTokens(msg message.Message) int {
	bytes := len(msg.ToolID) + len(msg.Name)
	hasTextPart := false
	for _, part := range msg.Parts {
		if part.Type == message.PartText {
			hasTextPart = true
		}
		bytes += len(part.Text)
		bytes += len(part.URL) + len(part.MimeType) + len(part.Data) + len(part.Name)
	}
	// Parts is the provider-facing source of truth. Older callers may only
	// populate Content, so use it only when no text part is available.
	if !hasTextPart {
		bytes += len(msg.Content)
	}
	for _, call := range msg.ToolCalls {
		bytes += len(call.ID) + len(call.Name) + len(call.Arguments) + len(call.Signature)
	}
	return estimatedMessageOverhead + (bytes+estimatedTokenBytes-1)/estimatedTokenBytes
}

// anchorEnd returns the first position after leading system messages and the
// first user message. These messages seed the conversation and remain
// available after either kind of compaction.
func anchorEnd(messages []message.Message) int {
	end := 0
	for end < len(messages) && messages[end].Type == message.MessageSystem {
		end++
	}
	if end < len(messages) && messages[end].Type == message.MessageUser {
		end++
	}
	return end
}

// completeTailStart moves a tail boundary to the beginning of an assistant
// tool-call group. This keeps its immediate results together, even when
// KeepLast lands in the middle of that group.
func completeTailStart(messages []message.Message, start, minimum int) int {
	if start >= len(messages) {
		return len(messages)
	}
	if start <= minimum {
		return minimum
	}
	if messages[start].Type != message.MessageTool {
		return start
	}
	for start > minimum && messages[start-1].Type == message.MessageTool {
		start--
	}
	if start > minimum && messages[start-1].Type == message.MessageAssistant && len(messages[start-1].ToolCalls) > 0 {
		start--
	}
	return start
}

func replaceHistory(history *message.History, messages []message.Message) error {
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return history.UnmarshalJSON(raw)
}

func trimHistoryToBudget(history *message.History, budget int) error {
	messages := history.Messages()
	prefixEnd := anchorEnd(messages)
	prefix := messages[:prefixEnd]
	latestUser := latestUserAfterAnchor(messages, prefixEnd)
	anchors := append([]message.Message(nil), prefix...)
	if latestUser >= 0 {
		anchors = append(anchors, messages[latestUser])
	}
	used := estimateHistoryTokens(anchors)
	if used > budget {
		return &MemoryBudgetExceededError{Budget: budget, EstimatedTokens: used}
	}

	// A unit is a user exchange, or an assistant tool call plus its immediate
	// tool results. Selecting a suffix of whole units avoids producing an
	// invalid half-exchange while still compacting repeated tool cycles after a
	// single seed user message.
	unitStart := prefixEnd
	if latestUser >= 0 {
		unitStart = latestUser + 1
	}
	units := conversationUnits(messages, unitStart)
	selectedStart := len(units)
	for i := len(units) - 1; i >= 0; i-- {
		unitTokens := estimateHistoryTokens(units[i])
		if used+unitTokens > budget {
			if i == len(units)-1 {
				return &MemoryBudgetExceededError{Budget: budget, EstimatedTokens: used + unitTokens}
			}
			break
		}
		used += unitTokens
		selectedStart = i
	}

	trimmed := append([]message.Message(nil), anchors...)
	for _, unit := range units[selectedStart:] {
		trimmed = append(trimmed, unit...)
	}
	if estimated := estimateHistoryTokens(trimmed); estimated > budget {
		return &MemoryBudgetExceededError{Budget: budget, EstimatedTokens: estimated}
	}
	if len(trimmed) == len(messages) {
		return nil
	}
	return replaceHistory(history, trimmed)
}

func latestUserAfterAnchor(messages []message.Message, prefixEnd int) int {
	firstUser := -1
	if prefixEnd > 0 && messages[prefixEnd-1].Type == message.MessageUser {
		firstUser = prefixEnd - 1
	}
	for i := len(messages) - 1; i > firstUser; i-- {
		if messages[i].Type == message.MessageUser {
			return i
		}
	}
	return -1
}

func conversationUnits(messages []message.Message, start int) [][]message.Message {
	units := make([][]message.Message, 0)
	for start < len(messages) {
		unitStart := start
		start++
		if messages[unitStart].Type == message.MessageUser {
			if start < len(messages) && messages[start].Type == message.MessageAssistant {
				start++
				if len(messages[start-1].ToolCalls) > 0 {
					for start < len(messages) && messages[start].Type == message.MessageTool {
						start++
					}
				}
			}
		} else if messages[unitStart].Type == message.MessageAssistant && len(messages[unitStart].ToolCalls) > 0 {
			for start < len(messages) && messages[start].Type == message.MessageTool {
				start++
			}
		}
		units = append(units, messages[unitStart:start])
	}
	return units
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
	msgs := history.Messages()
	total := len(msgs)
	if total <= s.KeepLast {
		return nil
	}
	prefixEnd := anchorEnd(msgs)
	tailStart := total - s.KeepLast
	if tailStart > total {
		tailStart = total
	}
	if tailStart < prefixEnd {
		tailStart = prefixEnd
	}
	tailStart = completeTailStart(msgs, tailStart, prefixEnd)
	if tailStart <= prefixEnd {
		return nil
	}
	latestUser := latestUserAfterAnchor(msgs, prefixEnd)
	older := make([]message.Message, 0, tailStart-prefixEnd)
	tail := append([]message.Message(nil), msgs[tailStart:]...)
	if latestUser >= 0 && latestUser < tailStart {
		older = append(older, msgs[prefixEnd:latestUser]...)
		older = append(older, msgs[latestUser+1:tailStart]...)
		tail = append([]message.Message{msgs[latestUser]}, tail...)
	} else {
		older = append(older, msgs[prefixEnd:tailStart]...)
	}

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

	// Rebuild history: preserved anchors, one current summary, then the
	// complete preserved tail. A previous summary in the middle is replaced,
	// so repeated compaction never accumulates duplicate summary messages.
	rebuilt := append([]message.Message(nil), msgs[:prefixEnd]...)
	rebuilt = append(rebuilt, message.NewSystemMessage(summary))
	rebuilt = append(rebuilt, tail...)
	if err := replaceHistory(history, rebuilt); err != nil {
		return fmt.Errorf("summarizer: re-marshal history: %w", err)
	}
	return nil
}

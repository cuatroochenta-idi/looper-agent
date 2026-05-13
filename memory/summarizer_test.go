package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// buildHistory creates a small conversation with deterministic content so
// summarizer tests can assert which messages survived.
func buildHistory(n int) *message.History {
	h := message.NewHistory()
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			h.AddUserMessage("user-msg-" + itoa(i))
		} else {
			h.AddAssistantMessage("assistant-msg-"+itoa(i), nil)
		}
	}
	return h
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestSummarizer_ReplacesOlderMessagesWithSummary asserts the core
// behavior: when KeepLast=2 and history has 6 messages, the first 4 get
// collapsed into a single system-message summary and the last 2 stay
// verbatim.
func TestSummarizer_ReplacesOlderMessagesWithSummary(t *testing.T) {
	hist := buildHistory(6)

	s := NewSummarizer(func(_ context.Context, msgs []message.Message) (string, error) {
		// Confirm the function gets the OLDER messages, not the kept tail.
		if len(msgs) != 4 {
			t.Fatalf("summarize got %d msgs, expected 4 oldest", len(msgs))
		}
		return "SUMMARY: " + msgs[0].Content + " ... " + msgs[3].Content, nil
	}, WithKeepLast(2))

	if err := s.Summarize(context.Background(), hist); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	msgs := hist.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 1 summary + 2 kept = 3 messages, got %d (%v)", len(msgs), msgs)
	}
	if msgs[0].Type != message.MessageSystem {
		t.Errorf("first message should be the summary as system, got %v", msgs[0].Type)
	}
	if !strings.Contains(msgs[0].Content, "SUMMARY") {
		t.Errorf("summary content lost, got %q", msgs[0].Content)
	}
	if msgs[1].Content != "user-msg-4" || msgs[2].Content != "assistant-msg-5" {
		t.Errorf("recent tail not preserved: %+v", msgs)
	}
}

// TestSummarizer_BelowThreshold_NoOp asserts that when history has
// fewer messages than KeepLast (or equal), the summarizer skips work.
func TestSummarizer_BelowThreshold_NoOp(t *testing.T) {
	hist := buildHistory(2)
	called := false
	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		called = true
		return "should-not-run", nil
	}, WithKeepLast(2))

	if err := s.Summarize(context.Background(), hist); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("summarize fn must not be called when nothing to compact")
	}
	if hist.Len() != 2 {
		t.Errorf("history mutated: got %d messages", hist.Len())
	}
}

// TestSummarizer_PropagatesError asserts a summarize-function error
// surfaces back to the caller and the history is left untouched.
func TestSummarizer_PropagatesError(t *testing.T) {
	hist := buildHistory(4)
	want := errors.New("summarizer offline")
	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		return "", want
	}, WithKeepLast(1))

	err := s.Summarize(context.Background(), hist)
	if !errors.Is(err, want) {
		t.Errorf("expected error to propagate, got %v", err)
	}
	if hist.Len() != 4 {
		t.Errorf("history should be untouched on failure, got %d", hist.Len())
	}
}

// TestSummarizer_EmptyResultIsRejected asserts the summarizer does not
// silently replace messages with an empty summary — an empty string is
// reported as an error so the caller knows the run produced nothing.
func TestSummarizer_EmptyResultIsRejected(t *testing.T) {
	hist := buildHistory(4)
	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		return "", nil
	}, WithKeepLast(1))

	err := s.Summarize(context.Background(), hist)
	if err == nil {
		t.Error("empty summary should produce a typed error, not silently shrink history")
	}
	if hist.Len() != 4 {
		t.Errorf("history should be untouched on empty summary, got %d", hist.Len())
	}
}

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

func TestTokenBudgetUsesMessageSizeToTriggerCompaction(t *testing.T) {
	h := message.NewHistory()
	for i := 0; i < 16; i++ {
		h.AddUserMessage(strings.Repeat("large conversation content ", 5000))
	}
	called := false
	s := NewSummarizer(func(_ context.Context, msgs []message.Message) (string, error) {
		called = true
		if len(msgs) == 0 {
			t.Fatal("summarizer received no messages")
		}
		return "compact", nil
	}, WithKeepLast(2))

	if err := (&TokenBudget{Budget: 500_000, Summarizer: s}).Manage(context.Background(), h); err != nil {
		t.Fatalf("manage: %v", err)
	}
	if !called {
		t.Fatal("expected message size to trigger compaction")
	}
}

func TestTokenEstimatorUsesPartsWhenContentIsStale(t *testing.T) {
	contentOnly := message.Message{Type: message.MessageUser, Content: strings.Repeat("content ", 1_000)}
	staleContent := message.Message{
		Type:    message.MessageUser,
		Content: "short stale view",
		Parts:   []message.Part{message.TextPart(strings.Repeat("provider source ", 1_000))},
	}
	if estimateMessageTokens(contentOnly) < 1_000 || estimateMessageTokens(staleContent) < 1_000 {
		t.Fatalf("large message representations were underestimated: content=%d parts=%d", estimateMessageTokens(contentOnly), estimateMessageTokens(staleContent))
	}
}

func TestSummarizerPreservesAnchorsAndCompleteToolGroup(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	h.AddUserMessage("old user")
	h.AddAssistantMessage("old answer", nil)
	h.AddUserMessage("tool turn")
	h.AddAssistantMessage("", []message.ToolCall{
		{ID: "a", Name: "tool-a", Arguments: json.RawMessage(`{"input":"a"}`)},
		{ID: "b", Name: "tool-b", Arguments: json.RawMessage(`{"input":"b"}`)},
		{ID: "c", Name: "tool-c", Arguments: json.RawMessage(`{"input":"c"}`)},
	})
	h.AddToolResult("a", "tool-a", "result-a", false)
	h.AddToolResult("b", "tool-b", "result-b", false)
	h.AddToolResult("c", "tool-c", "result-c", false)
	h.AddUserMessage("new user")
	h.AddAssistantMessage("new answer", nil)

	var summarized []message.Message
	s := NewSummarizer(func(_ context.Context, msgs []message.Message) (string, error) {
		summarized = append([]message.Message(nil), msgs...)
		return "compact", nil
	}, WithKeepLast(3))
	if err := s.Summarize(context.Background(), h); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	msgs := h.Messages()
	if msgs[0].Type != message.MessageSystem || msgs[0].Content != "system anchor" {
		t.Fatalf("system anchor was not preserved: %+v", msgs)
	}
	if msgs[1].Type != message.MessageUser || msgs[1].Content != "seed user" {
		t.Fatalf("first user anchor was not preserved: %+v", msgs)
	}
	if len(summarized) != 3 || summarized[0].Content != "old user" {
		t.Fatalf("unexpected summarized middle: %+v", summarized)
	}
	if len(msgs) != 9 || msgs[2].Type != message.MessageSystem || msgs[2].Content != "compact" {
		t.Fatalf("unexpected compacted history: %s", summarizeMemoryMessages(msgs))
	}
	toolCalls := 0
	toolResults := 0
	for _, m := range msgs {
		if m.Type == message.MessageAssistant && len(m.ToolCalls) > 0 {
			toolCalls += len(m.ToolCalls)
		}
		if m.Type == message.MessageTool {
			toolResults++
		}
	}
	if toolCalls != 3 || toolResults != 3 {
		t.Fatalf("tool group was split or lost: %s", summarizeMemoryMessages(msgs))
	}
}

func TestSummarizerCompactsRepeatedToolGroupsAfterSingleSeed(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	for i := 0; i < 16; i++ {
		callID := "call-" + itoa(i)
		h.AddAssistantMessage("", []message.ToolCall{{
			ID:        callID,
			Name:      "large-tool",
			Arguments: json.RawMessage(`{"input":"large tool arguments"}`),
		}})
		h.AddToolResult(callID, "large-tool", strings.Repeat("large result ", 100), false)
	}

	called := false
	s := NewSummarizer(func(_ context.Context, msgs []message.Message) (string, error) {
		called = true
		if len(msgs) != 26 {
			t.Fatalf("summarizer should receive all but the final 3 tool groups, got %d messages", len(msgs))
		}
		return "compact", nil
	}, WithKeepLast(6))
	if err := (&TokenBudget{Budget: 2_000, Summarizer: s}).Manage(context.Background(), h); err != nil {
		t.Fatalf("manage: %v", err)
	}
	if !called {
		t.Fatal("expected repeated tool groups to trigger compaction")
	}
	if estimated := estimateHistoryTokens(h.Messages()); estimated > 2_000 {
		t.Fatalf("compacted history still exceeds budget: %d", estimated)
	}
	assertCompleteToolPairs(t, h.Messages())
}

func TestSummarizerReplacesPreviousSummaryWithoutDuplicatingIt(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	h.AddUserMessage("old user")
	h.AddAssistantMessage("old answer", nil)
	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		return "compact", nil
	}, WithKeepLast(2))
	if err := s.Summarize(context.Background(), h); err != nil {
		t.Fatalf("first summarize: %v", err)
	}
	h.AddUserMessage("new user")
	h.AddAssistantMessage("new answer", nil)
	if err := s.Summarize(context.Background(), h); err != nil {
		t.Fatalf("second summarize: %v", err)
	}

	msgs := h.Messages()
	summaryCount := 0
	for _, m := range msgs {
		if m.Type == message.MessageSystem && m.Content == "compact" {
			summaryCount++
		}
	}
	if summaryCount != 1 {
		t.Fatalf("expected one current summary, got %d: %s", summaryCount, summarizeMemoryMessages(msgs))
	}
	if msgs[0].Content != "system anchor" || msgs[1].Content != "seed user" {
		t.Fatalf("anchors changed after repeated summary: %s", summarizeMemoryMessages(msgs))
	}
}

func TestSummarizerPreservesLatestUserAlongsideSeed(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	h.AddUserMessage("old user")
	h.AddAssistantMessage("", []message.ToolCall{{
		ID:        "old-call",
		Name:      "old-tool",
		Arguments: json.RawMessage(`{}`),
	}})
	h.AddToolResult("old-call", "old-tool", "old result", false)
	h.AddUserMessage("latest approved scope")
	for i := 0; i < 4; i++ {
		callID := "latest-call-" + itoa(i)
		h.AddAssistantMessage("", []message.ToolCall{{
			ID:        callID,
			Name:      "latest-tool",
			Arguments: json.RawMessage(`{}`),
		}})
		h.AddToolResult(callID, "latest-tool", "latest result", false)
	}

	var summarized []message.Message
	s := NewSummarizer(func(_ context.Context, msgs []message.Message) (string, error) {
		summarized = append([]message.Message(nil), msgs...)
		return "compact", nil
	}, WithKeepLast(2))
	if err := s.Summarize(context.Background(), h); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	msgs := h.Messages()
	if len(msgs) != 6 || msgs[0].Content != "system anchor" || msgs[1].Content != "seed user" ||
		msgs[2].Content != "compact" || msgs[3].Content != "latest approved scope" {
		t.Fatalf("latest user scope was not retained with seed: %s", summarizeMemoryMessages(msgs))
	}
	for _, msg := range summarized {
		if msg.Content == "latest approved scope" {
			t.Fatal("latest user scope was summarized instead of retained")
		}
	}
	assertCompleteToolPairs(t, msgs)
}

func TestTokenBudgetReturnsTypedErrorWhenPreservedAnchorExceedsBudget(t *testing.T) {
	seed := strings.Repeat("seed ", 20_000)
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage(seed)
	h.AddUserMessage("new user")
	h.AddAssistantMessage("new answer", nil)

	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		return "compact", nil
	}, WithKeepLast(2))
	err := (&TokenBudget{Budget: 100, Summarizer: s}).Manage(context.Background(), h)
	var budgetErr *MemoryBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected MemoryBudgetExceededError, got %v", err)
	}
	if h.Messages()[1].Content != seed {
		t.Fatal("history lost the oversized first-user anchor")
	}
}

func TestTokenBudgetWithoutSummarizerKeepsAnchorAndCompleteExchanges(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	h.AddUserMessage(strings.Repeat("old user ", 100))
	h.AddAssistantMessage(strings.Repeat("old answer ", 100), nil)
	h.AddUserMessage("new user")
	h.AddAssistantMessage("new answer", nil)

	if err := (&TokenBudget{Budget: 100}).Manage(context.Background(), h); err != nil {
		t.Fatalf("manage: %v", err)
	}
	msgs := h.Messages()
	if len(msgs) != 4 || msgs[0].Content != "system anchor" || msgs[1].Content != "seed user" ||
		msgs[2].Content != "new user" || msgs[3].Content != "new answer" {
		t.Fatalf("expected anchors plus newest complete exchange, got %s", summarizeMemoryMessages(msgs))
	}
}

func TestTokenBudgetWithoutSummarizerReportsOversizedLatestUnit(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system anchor")
	h.AddUserMessage("seed user")
	h.AddAssistantMessage("", []message.ToolCall{{
		ID:        "latest",
		Name:      "large-tool",
		Arguments: json.RawMessage(`{"input":"large tool arguments"}`),
	}})
	h.AddToolResult("latest", "large-tool", strings.Repeat("large result ", 1000), false)

	err := (&TokenBudget{Budget: 100}).Manage(context.Background(), h)
	var budgetErr *MemoryBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected typed error for oversized latest unit, got %v", err)
	}
	assertCompleteToolPairs(t, h.Messages())
}

func TestTokenBudgetDoesNotCompactWithinBudget(t *testing.T) {
	h := message.NewHistory()
	h.AddSystemMessage("system")
	h.AddUserMessage("hello")
	h.AddAssistantMessage("world", nil)
	before := h.Messages()
	called := false
	s := NewSummarizer(func(_ context.Context, _ []message.Message) (string, error) {
		called = true
		return "unexpected", nil
	})
	if err := (&TokenBudget{Budget: 100, Summarizer: s}).Manage(context.Background(), h); err != nil {
		t.Fatalf("manage: %v", err)
	}
	if called || summarizeMemoryMessages(h.Messages()) != summarizeMemoryMessages(before) {
		t.Fatal("history was compacted despite fitting the budget")
	}
}

func summarizeMemoryMessages(msgs []message.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Type))
		b.WriteByte(':')
		b.WriteString(m.Content)
		b.WriteByte('|')
	}
	return b.String()
}

func assertCompleteToolPairs(t *testing.T, msgs []message.Message) {
	t.Helper()
	for i, msg := range msgs {
		if msg.Type != message.MessageAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		pending := make(map[string]bool, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			pending[call.ID] = true
		}
		for _, following := range msgs[i+1:] {
			if following.Type == message.MessageTool {
				delete(pending, following.ToolID)
				continue
			}
			if len(pending) > 0 && following.Type == message.MessageUser {
				break
			}
		}
		if len(pending) > 0 {
			t.Errorf("tool call at index %d has unmatched results: %v", i, pending)
		}
	}
}

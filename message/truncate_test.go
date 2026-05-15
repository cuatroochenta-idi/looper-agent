package message

import (
	"encoding/json"
	"testing"
)

// buildToolTurnHistory creates a history that exercises the tool-pair edge
// case the truncator must respect:
//
//	user[1]
//	assistant[1] (tool_calls=[X])
//	tool_result[X]
//	user[2]
//	assistant[2] (tool_calls=[Y, Z])
//	tool_result[Y]
//	tool_result[Z]
//	user[3]
//	assistant[3] (final text)
//
// A naive truncation that just keeps the last K messages can land between
// an assistant tool_call and its result(s), which Anthropic rejects with a
// 400 ("each tool_use must have a corresponding tool_result").
func buildToolTurnHistory() *History {
	h := NewHistory()

	h.AddUserMessage("first")
	h.AddAssistantMessage("", []ToolCall{{ID: "X", Name: "search", Arguments: json.RawMessage(`{}`)}})
	h.AddToolResult("X", "search", "result-X", false)

	h.AddUserMessage("second")
	h.AddAssistantMessage("", []ToolCall{
		{ID: "Y", Name: "a", Arguments: json.RawMessage(`{}`)},
		{ID: "Z", Name: "b", Arguments: json.RawMessage(`{}`)},
	})
	h.AddToolResult("Y", "a", "result-Y", false)
	h.AddToolResult("Z", "b", "result-Z", false)

	h.AddUserMessage("third")
	h.AddAssistantMessage("final answer", nil)

	return h
}

// TestTruncateByTurns_KeepsLastNUserTurns asserts that asking for N=2 user
// turns retains the last two user messages and everything that came after
// the user-turn boundary of the kept range.
func TestTruncateByTurns_KeepsLastNUserTurns(t *testing.T) {
	h := buildToolTurnHistory()
	h.TruncateByTurns(2)

	msgs := h.Messages()
	userCount := 0
	for _, m := range msgs {
		if m.Type == MessageUser {
			userCount++
		}
	}
	if userCount != 2 {
		t.Errorf("expected 2 user turns kept, got %d (history=%+v)", userCount, summarize(msgs))
	}
	// First kept user message should be the second one we added.
	for _, m := range msgs {
		if m.Type == MessageUser {
			if m.Content != "second" {
				t.Errorf("first kept user msg should be 'second', got %q", m.Content)
			}
			break
		}
	}
}

// TestTruncateByTurns_NeverSplitsToolPair asserts that the cut point is
// pushed back to avoid landing between an assistant tool_use and its
// tool_result(s).
func TestTruncateByTurns_NeverSplitsToolPair(t *testing.T) {
	h := buildToolTurnHistory()
	h.TruncateByTurns(1) // would naively cut just before "third"

	msgs := h.Messages()
	// Every assistant message with ToolCalls in the result must be followed
	// by tool_result messages whose IDs match each ToolCall.
	for i, m := range msgs {
		if m.Type != MessageAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		pending := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = true
		}
		// Walk forward looking for matching tool results.
		for j := i + 1; j < len(msgs) && len(pending) > 0; j++ {
			if msgs[j].Type == MessageTool {
				delete(pending, msgs[j].ToolID)
			}
		}
		if len(pending) > 0 {
			t.Errorf("tool_use at index %d has unmatched results: %v\nhistory=%+v", i, pending, summarize(msgs))
		}
	}
}

// TestTruncateByTurns_ZeroEmptiesHistory pins the contract for the
// boundary case: N=0 keeps nothing.
func TestTruncateByTurns_ZeroEmptiesHistory(t *testing.T) {
	h := buildToolTurnHistory()
	h.TruncateByTurns(0)
	if h.Len() != 0 {
		t.Errorf("expected empty history, got %d messages", h.Len())
	}
}

// TestTruncateByTurns_MoreThanAvailableKeepsAll asserts that asking for
// more turns than exist is a no-op.
func TestTruncateByTurns_MoreThanAvailableKeepsAll(t *testing.T) {
	h := buildToolTurnHistory()
	before := h.Len()
	h.TruncateByTurns(100)
	if h.Len() != before {
		t.Errorf("expected no truncation, got %d messages (was %d)", h.Len(), before)
	}
}

// TestTruncate_NeverLeavesOrphanToolMessage asserts that Truncate's pair-safe
// behaviour drops a leading tool_result whose preceding assistant tool_calls
// would otherwise be cut off. OpenAI rejects such a request with:
// "messages with role 'tool' must be a response to a preceeding message with
// 'tool_calls'".
func TestTruncate_NeverLeavesOrphanToolMessage(t *testing.T) {
	h := buildToolTurnHistory()
	// History layout (9 messages):
	//   0: user           "first"
	//   1: assistant[X]
	//   2: tool[X]
	//   3: user           "second"
	//   4: assistant[Y,Z]
	//   5: tool[Y]
	//   6: tool[Z]
	//   7: user           "third"
	//   8: assistant      "final answer"
	//
	// A naive Truncate(7) would keep msgs[2..8] starting with tool[X],
	// which is an orphan. The pair-safe rule must drop it.
	h.Truncate(7)
	msgs := h.Messages()
	if len(msgs) == 0 {
		t.Fatalf("history unexpectedly empty after Truncate")
	}
	if msgs[0].Type == MessageTool {
		t.Errorf("leading message is orphan tool result: %+v", summarize(msgs))
	}
}

// TestTruncate_DropsAssistantWithMissingResults asserts that Truncate drops
// a leading assistant message whose ToolCalls aren't all matched by tool
// results within the kept window — OpenAI rejects unmatched tool_calls too.
func TestTruncate_DropsAssistantWithMissingResults(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("u1")
	h.AddAssistantMessage("", []ToolCall{
		{ID: "A", Name: "x", Arguments: json.RawMessage(`{}`)},
		{ID: "B", Name: "y", Arguments: json.RawMessage(`{}`)},
	})
	h.AddToolResult("A", "x", "ra", false)
	h.AddToolResult("B", "y", "rb", false)
	h.AddUserMessage("u2")
	h.AddAssistantMessage("done", nil)
	// Layout: [user, assistant(A,B), tool(A), tool(B), user, assistant]
	// Asking for 4 messages would keep [tool(A), tool(B), user, assistant];
	// the leading tool(A) is an orphan, so the cut must advance past both
	// tool messages, leaving [user, assistant].
	h.Truncate(4)
	msgs := h.Messages()
	for _, m := range msgs {
		if m.Type == MessageTool {
			t.Errorf("kept window still contains orphan tool result: %+v", summarize(msgs))
		}
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (user + final assistant), got %d: %s", len(msgs), summarize(msgs))
	}
}

// TestTruncate_KeepsCompletePair asserts that the cut lands cleanly on a
// boundary that includes the matching assistant+tool pair when possible.
func TestTruncate_KeepsCompletePair(t *testing.T) {
	h := buildToolTurnHistory()
	// Asking for 6 messages keeps msgs[3..8]: user, assistant(Y,Z), tool(Y),
	// tool(Z), user, assistant — a clean user-turn boundary.
	h.Truncate(6)
	msgs := h.Messages()
	if len(msgs) != 6 {
		t.Errorf("expected 6 kept, got %d: %s", len(msgs), summarize(msgs))
	}
	if msgs[0].Type != MessageUser || msgs[0].Content != "second" {
		t.Errorf("expected first kept message to be user('second'), got %+v", msgs[0])
	}
}

// TestTruncate_ZeroEmptiesHistory pins the contract for the boundary case.
func TestTruncate_ZeroEmptiesHistory(t *testing.T) {
	h := buildToolTurnHistory()
	h.Truncate(0)
	if h.Len() != 0 {
		t.Errorf("expected empty history, got %d messages", h.Len())
	}
}

// TestTruncate_MoreThanAvailableKeepsAll asserts that asking for more
// messages than exist is a no-op.
func TestTruncate_MoreThanAvailableKeepsAll(t *testing.T) {
	h := buildToolTurnHistory()
	before := h.Len()
	h.Truncate(100)
	if h.Len() != before {
		t.Errorf("expected no truncation, got %d (was %d)", h.Len(), before)
	}
}

// summarize renders messages in a short form for test output diagnostics.
func summarize(msgs []Message) string {
	var b []byte
	for i, m := range msgs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, byte(m.Type[0]))
		if m.Type == MessageAssistant && len(m.ToolCalls) > 0 {
			b = append(b, '(')
			for j, tc := range m.ToolCalls {
				if j > 0 {
					b = append(b, ',')
				}
				b = append(b, []byte(tc.ID)...)
			}
			b = append(b, ')')
		}
		if m.Type == MessageTool {
			b = append(b, '(')
			b = append(b, []byte(m.ToolID)...)
			b = append(b, ')')
		}
	}
	return string(b)
}

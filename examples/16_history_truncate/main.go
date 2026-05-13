// Example: History.TruncateByTurns — tool-pair-aware history pruning.
//
// Long-running agents accumulate history that eventually busts the model's
// context window. Naïve slice truncation is unsafe: Anthropic returns a 400
// when a tool_use block is split from its matching tool_result across the
// request boundary. TruncateByTurns(n) avoids that by cutting only at user
// message boundaries, so every assistant tool_use stays with its tool
// result inside the retained window.
//
// This example has no LLM call. It builds a synthetic history with 5 user
// turns — some plain Q&A, some with assistant tool_use + tool_result pairs
// — calls TruncateByTurns(2), and prints the surviving messages plus a
// sanity check that every tool_use still has its matching tool_result.
//
// Usage:
//
//	go run examples/16_history_truncate/main.go
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

func main() {
	h := message.NewHistory()

	// Turn 1 — plain Q&A.
	h.AddUserMessage("Hi, what's 2+2?")
	h.AddAssistantMessage("4.", nil)

	// Turn 2 — tool_use + tool_result pair.
	h.AddUserMessage("What's the weather in Barcelona?")
	h.AddAssistantMessage("", []message.ToolCall{
		{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Barcelona"}`)},
	})
	h.AddToolResult("call_1", "weather", "22C, sunny", false)
	h.AddAssistantMessage("It's 22C and sunny.", nil)

	// Turn 3 — plain Q&A.
	h.AddUserMessage("Thanks. What's the capital of France?")
	h.AddAssistantMessage("Paris.", nil)

	// Turn 4 — parallel tool calls + results.
	h.AddUserMessage("Get me prices for AAPL and MSFT.")
	h.AddAssistantMessage("", []message.ToolCall{
		{ID: "call_2", Name: "price", Arguments: json.RawMessage(`{"ticker":"AAPL"}`)},
		{ID: "call_3", Name: "price", Arguments: json.RawMessage(`{"ticker":"MSFT"}`)},
	})
	h.AddToolResult("call_2", "price", "AAPL: 195.32", false)
	h.AddToolResult("call_3", "price", "MSFT: 412.10", false)
	h.AddAssistantMessage("AAPL is 195.32, MSFT is 412.10.", nil)

	// Turn 5 — plain Q&A, will become the second-to-last retained turn.
	h.AddUserMessage("Round both to the nearest dollar.")
	h.AddAssistantMessage("AAPL ≈ 195, MSFT ≈ 412.", nil)

	fmt.Printf("Before: %d messages across %d user turns\n", h.Len(), h.TurnCount())
	printTypes("before", h)

	// Keep only the last 2 user turns. Every tool_use must remain paired
	// with its tool_result inside the retained window.
	h.TruncateByTurns(2)

	fmt.Printf("\nAfter TruncateByTurns(2): %d messages across %d user turns\n",
		h.Len(), h.TurnCount())
	printTypes("after", h)

	// Invariant check: every assistant tool_use ID is followed by a matching
	// tool_result with the same ID inside the retained window.
	if err := assertToolPairs(h); err != nil {
		fmt.Fprintf(os.Stderr, "\nINVARIANT VIOLATED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nInvariant OK: every tool_use is paired with its tool_result.")
}

func printTypes(label string, h *message.History) {
	fmt.Printf("  [%s] message types in order:\n", label)
	for i, m := range h.Messages() {
		extra := ""
		if len(m.ToolCalls) > 0 {
			ids := ""
			for j, tc := range m.ToolCalls {
				if j > 0 {
					ids += ","
				}
				ids += tc.ID
			}
			extra = fmt.Sprintf(" tool_calls=[%s]", ids)
		}
		if m.Type == message.MessageTool {
			extra = fmt.Sprintf(" tool_id=%s", m.ToolID)
		}
		fmt.Printf("    %2d  %-9s%s\n", i, m.Type, extra)
	}
}

// assertToolPairs verifies that every tool_use ID emitted by an assistant
// message has a matching tool_result (same ToolID) somewhere after it in
// the history.
func assertToolPairs(h *message.History) error {
	resultIDs := make(map[string]bool)
	for _, m := range h.Messages() {
		if m.Type == message.MessageTool {
			resultIDs[m.ToolID] = true
		}
	}
	for _, m := range h.Messages() {
		if m.Type != message.MessageAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !resultIDs[tc.ID] {
				return fmt.Errorf("tool_use %q has no matching tool_result", tc.ID)
			}
		}
	}
	return nil
}

// Example: dynamic tool allowlist per turn.
//
// WithDynamicTools lets you decide which tools the model sees on each LLM
// call, based on the current conversation history. Use this to enforce
// state-machine semantics:
//
//   - hide a tool once it has been used (one-shot tools)
//   - hide tools that only make sense after a prerequisite step
//   - swap tools based on a "phase" inferred from history
//
// This example wires two tools — search and summarize — and exposes only
// one at a time. Until search has been called, summarize is hidden. After
// search has run at least once, search is hidden and summarize appears.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/14_dynamic_tools/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type SearchIn struct {
	Query string `json:"query" jsonschema:"description=Topic to search,required"`
}

type SummarizeIn struct {
	Text string `json:"text" jsonschema:"description=Text to summarize,required"`
}

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	searchTool := tool.MustNewTool(SearchIn{},
		func(_ context.Context, in SearchIn) (string, error) {
			return fmt.Sprintf("Found 3 articles about %q (mock).", in.Query), nil
		},
		tool.ToolConfig{Name: "search", Description: "Search the web for a topic."},
	)
	summarizeTool := tool.MustNewTool(SummarizeIn{},
		func(_ context.Context, in SummarizeIn) (string, error) {
			s := strings.TrimSpace(in.Text)
			if len(s) > 80 {
				s = s[:80] + "…"
			}
			return "Summary: " + s, nil
		},
		tool.ToolConfig{Name: "summarize", Description: "Summarize the given text."},
	)

	// Phase machine via WithDynamicTools: hide summarize until search has
	// been called at least once; hide search afterwards.
	phaseFn := func(_ context.Context, h *message.History) []*tool.Tool {
		searched := false
		for _, m := range h.Messages() {
			if m.Type != message.MessageAssistant {
				continue
			}
			for _, tc := range m.ToolCalls {
				if tc.Name == "search" {
					searched = true
				}
			}
		}
		if !searched {
			return []*tool.Tool{searchTool}
		}
		return []*tool.Tool{summarizeTool}
	}

	agent := looper.MustNewAgent(openai.NewProvider(key),
		"You are a research assistant. First search for the topic, then summarize the result. "+
			"Use the tools available on each turn — do not assume tools exist that aren't shown.",
		looper.WithDynamicTools(loop.DynamicToolsFunc(phaseFn)),
	)

	res, err := agent.Run(context.Background(), "Tell me about Go generics in two sentences.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
}

// Example: OnBeforeToolExecution as a loop-detector.
//
// Agents that drive open-ended workflows occasionally fall into a rut —
// calling the same tool with the same arguments over and over because each
// result looks "almost right". OnBeforeToolExecution lets you intercept
// every planned tool call right before it runs and surface that pathology
// to the model as feedback, without crashing the loop.
//
// The hook receives a *loop.ToolExecutionParams whose Cancel(callID, reason)
// method suppresses one tool call and inserts a synthetic, error-flavoured
// tool_result carrying the reason. The model sees that result on the next
// turn and can change strategy. Replace(callID, newCall) is the other
// available mutation — out of scope here.
//
// This example wires two cheap, offline-friendly tools:
//
//   - search: pretends to look up a topic. The model is prompted to keep
//     refining its query, which encourages repeated calls.
//   - answer: emits a final sentence based on the most recent search.
//
// A per-(name+args) counter triggers on the 4th identical call. Cancelling
// it nudges the model to either vary the query or move on to answer.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/15_before_tool_hook/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type SearchIn struct {
	Query string `json:"query" jsonschema:"description=Topic to search,required"`
}

type AnswerIn struct {
	Text string `json:"text" jsonschema:"description=Final answer text,required"`
}

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	// Two cheap mock tools — no network calls.
	searchTool := tool.MustNewTool(SearchIn{},
		func(_ context.Context, in SearchIn) (string, error) {
			// Deliberately vague — invites the model to retry.
			return fmt.Sprintf("Partial result for %q: nothing conclusive.", in.Query), nil
		},
		tool.ToolConfig{Name: "search", Description: "Search the knowledge base for a topic."},
	)
	answerTool := tool.MustNewTool(AnswerIn{},
		func(_ context.Context, in AnswerIn) (string, error) {
			return "Answer recorded.", nil
		},
		tool.ToolConfig{Name: "answer", Description: "Record the final answer for the user."},
	)

	// Loop detector: count consecutive identical calls by (name + raw args).
	// On the 4th occurrence, cancel the call with a corrective reason.
	const limit = 4
	var (
		mu     sync.Mutex
		counts = make(map[string]int)
	)

	loopDetector := func(_ context.Context, params *loop.ToolExecutionParams) error {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range params.Calls {
			key := c.Name + "|" + string(c.Arguments)
			counts[key]++
			if counts[key] >= limit {
				reason := fmt.Sprintf(
					"looped — %q with the same arguments has been called %d times. "+
						"Try a different approach: vary the query or move on to 'answer'.",
					c.Name, counts[key],
				)
				params.Cancel(c.ID, reason)
				fmt.Printf("[loop-detector] cancelled %s call %s (count=%d)\n",
					c.Name, c.ID, counts[key])
			}
		}
		return nil
	}

	agent := looper.MustNewAgent(openai.NewProvider(key),
		"You are a research assistant. You have two tools: 'search' and 'answer'. "+
			"Investigate the user's question using search, then call answer with a final sentence. "+
			"If a tool call comes back saying you're looping, change your approach immediately — "+
			"either reformulate the query or call answer with what you have.",
		searchTool, answerTool,
	)
	agent.OnBeforeToolExecution(loopDetector)

	res, err := agent.Run(context.Background(),
		"What's the capital of Australia? Use the search tool first.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
}

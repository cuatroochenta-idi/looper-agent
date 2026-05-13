// Example: ToolChoice — force the model to call a tool.
//
// By default the model decides whether to call a tool (ToolChoiceAuto).
// Two other modes are useful in state-machine agents:
//
//   - ToolChoiceRequired: the model MUST call some tool on this turn.
//     Use on a planning turn where prose alone is not allowed.
//   - ToolChoiceSpecific(name): the model MUST call this specific tool.
//     Use to force a transition: "now run the publish step".
//
// This example wires a `track_progress` tool and uses
// ToolChoiceRequired so the model is forced to call it (instead of just
// describing its plan in prose) before producing any final answer.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/17_tool_choice/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type TrackIn struct {
	Step  string `json:"step" jsonschema:"description=What step are you on?,required"`
	Done  bool   `json:"done"  jsonschema:"description=Is this step finished?"`
}

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	track := tool.MustNewTool(TrackIn{},
		func(_ context.Context, in TrackIn) (string, error) {
			mark := "·"
			if in.Done {
				mark = "✓"
			}
			fmt.Printf("[tracker]   %s %s\n", mark, in.Step)
			return "tracked", nil
		},
		tool.ToolConfig{
			Name:        "track_progress",
			Description: "Record the current step of your reasoning before answering. Always call this once.",
		},
	)

	agent := looper.MustNewAgent(openai.NewProvider(key),
		"You are a precise assistant. Use track_progress to record what you're "+
			"doing before producing a final answer.",
		track,
		// Force the FIRST LLM turn to call a tool — prose alone is rejected.
		// Subsequent turns return to "auto" since LLMRequest carries the
		// same value on every turn but the validator-driven flip pattern
		// (see colleague feedback) can swap it dynamically if needed.
		looper.WithToolChoice(provider.ToolChoiceRequired()),
	)

	res, err := agent.Run(context.Background(), "What is the time complexity of quicksort?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
}

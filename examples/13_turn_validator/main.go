// Example: TurnValidator with corrective re-prompt.
//
// A TurnValidator inspects every completed turn (final text OR tool calls +
// results) and can reject the turn with a Hint. On rejection the framework
// injects the Hint as a system message and re-prompts the model, up to a
// configurable retry budget. The budget resets on every accepted turn so a
// validator can recover after a streak of rejections.
//
// Use case: enforce per-project invariants the model keeps slipping on —
// "always cite a source", "never reply with a one-word answer", "must call
// publish_pages before complete_prd", etc.
//
// This example asks for a definition; the validator rejects any reply
// shorter than 60 characters until the model produces a real explanation.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/13_turn_validator/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	p := openai.NewProvider(key)

	// Validator: reject final-text turns whose content is too short. Tool
	// call turns are accepted unconditionally (LastAssistantContent is
	// usually empty when the LLM only produced tool calls).
	validator := func(snap loop.TurnSnapshot) loop.Outcome {
		// Skip turns that produced tool calls — judge the final text only.
		if len(snap.ToolCalls) > 0 {
			return loop.Outcome{OK: true}
		}
		text := strings.TrimSpace(snap.LastAssistantContent)
		if len(text) < 60 {
			return loop.Outcome{
				OK:     false,
				Reason: fmt.Sprintf("reply too short (%d chars)", len(text)),
				Hint:   "Your previous reply was too short. Give a proper explanation in at least two sentences.",
			}
		}
		return loop.Outcome{OK: true}
	}

	agent := looper.MustNewAgent(p,
		"You are a curt assistant. Answer briefly.", // intentionally encourages short replies
		looper.WithTurnValidatorFunc(validator, 2),  // up to 2 corrective re-prompts
	)

	res, err := agent.Run(context.Background(), "What is a closure in programming?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
	if res.Status == "validation_exhausted" {
		fmt.Println("⚠ validator never accepted a turn — surfacing last attempt anyway.")
	}
}

// Example: full hook lifecycle — observe every stage of the agentic loop.
//
// This registers a hook for each HookType so you can see the order in which
// they fire, what state they receive, and how to mutate the history mid-run.
//
// Usage:
//
//	set -a && source .env.local && set +a
//	go run examples/05_hooks_lifecycle/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

type ClockInput struct {
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone like 'Europe/Madrid'"`
}

func main() {
	ctx := context.Background()

	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))

	clock := tool.MustNewTool(ClockInput{},
		func(_ context.Context, in ClockInput) (string, error) {
			return fmt.Sprintf("Pretend now() in %s is 2026-05-12 12:34:56", in.Timezone), nil
		},
		tool.ToolConfig{
			Name:        "get_clock",
			Description: "Returns the current time for a given IANA timezone.",
		},
	)

	agent := looper.MustNewAgent(p,
		"You are a helpful assistant. When asked about time, use the get_clock tool.",
		clock,
	)

	// Track every hook firing for inspection.
	agent.On("BeforeCall", func(_ context.Context, p *loop.CallParams) error {
		fmt.Printf("[BeforeCall] turn=%d  history.len=%d  prompt_len=%d\n",
			p.Turn, p.History.Len(), len(p.SystemPrompt))
		return nil
	})
	agent.On("AfterCall", func(_ context.Context, p *loop.CallParams) error {
		fmt.Printf("[AfterCall]  turn=%d  history.len=%d\n", p.Turn, p.History.Len())
		return nil
	})
	agent.On("BeforeFinalResponse", func(_ context.Context, p *loop.CallParams) error {
		fmt.Printf("[BeforeFinal] turn=%d — about to return final output\n", p.Turn)
		return nil
	})
	agent.On("AfterFinalResponse", func(_ context.Context, p *loop.CallParams) error {
		fmt.Printf("[AfterFinal]  turn=%d — final output delivered\n", p.Turn)
		return nil
	})
	agent.On("OnCancel", func(_ context.Context, p *loop.CallParams) error {
		fmt.Printf("[OnCancel]    turn=%d — loop cancelled\n", p.Turn)
		return nil
	})

	result, err := agent.Run(ctx, "What time is it in Madrid right now?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("Output: %s\n", result.Output)
	fmt.Printf("Cost:   $%.6f  Turns: %d  Status: %s\n",
		result.Cost.TotalUSD, result.Turns, result.Status)
}

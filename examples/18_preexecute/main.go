// Example: tool.WithPreExecute + RejectWithHint — declarative business
// validation that lives next to the tool, not buried inside the body.
//
// A PreExecute hook runs after the framework's JSON Schema validation but
// before the tool body. Return tool.RejectWithHint("...") to abort with a
// hint the LLM will see as a tool_result error — the model can self-correct
// (e.g. call a prerequisite tool first). Return any other error to fail the
// call outright; no retry.
//
// This example wires two tools (`publish_pages` and `complete_prd`) and a
// shared state tracker. `complete_prd` rejects until `publish_pages` has
// succeeded — exactly the "PRD done without publish" patho the colleague
// flagged.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/18_preexecute/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// stateTracker shares "has publish_pages run yet?" between the two tools.
// In a real app this is your domain DB / cache; the closure pattern here
// is the simplest way to demo the dependency.
type stateTracker struct {
	mu        sync.Mutex
	published bool
}

func (s *stateTracker) markPublished() {
	s.mu.Lock()
	s.published = true
	s.mu.Unlock()
}

func (s *stateTracker) isPublished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

type PublishIn struct {
	PageID string `json:"page_id" jsonschema:"description=Page to publish,required"`
}

type CompleteIn struct {
	PRDID string `json:"prd_id" jsonschema:"description=PRD identifier,required"`
}

func main() {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	state := &stateTracker{}

	publish := tool.MustNewTool(PublishIn{},
		func(_ context.Context, in PublishIn) (string, error) {
			state.markPublished()
			return fmt.Sprintf("Published page %s.", in.PageID), nil
		},
		tool.ToolConfig{
			Name:        "publish_pages",
			Description: "Publish a PRD's draft pages. Must run before complete_prd.",
		},
	)

	completePRD := tool.MustNewTool(CompleteIn{},
		func(_ context.Context, in CompleteIn) (string, error) {
			return fmt.Sprintf("PRD %s marked complete.", in.PRDID), nil
		},
		tool.ToolConfig{
			Name:        "complete_prd",
			Description: "Mark a PRD as complete. Only valid AFTER publish_pages has run.",
		},
		// Business rule wired declaratively — the model sees a clean
		// "publish_pages must run first" hint instead of a silent success
		// that breaks downstream state.
		tool.WithPreExecute(func(_ context.Context, in CompleteIn) error {
			if !state.isPublished() {
				return tool.RejectWithHint(
					"You must call publish_pages before complete_prd. " +
						"Call publish_pages first, then retry.",
				)
			}
			return nil
		}),
	)

	agent := looper.MustNewAgent(openai.NewProvider(key),
		"You are a PRD workflow assistant. You have two tools: publish_pages "+
			"and complete_prd. Follow tool hints carefully.",
		publish, completePRD,
	)

	// Deliberately ask for completion FIRST — the model will likely try
	// complete_prd, get rejected, and self-correct by calling publish_pages.
	res, err := agent.Run(context.Background(),
		"Please complete PRD-42. The page_id you can use for publishing is page-99.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("──── Output ────")
	fmt.Println(res.Output)
	fmt.Println("────────────────")
	fmt.Printf("turns: %d  status: %s  cost: $%.6f\n", res.Turns, res.Status, res.Cost.TotalUSD)
}

// Example: structured output — the agent returns a typed Go struct.
//
// WithStructuredOutput[T] generates the JSON Schema from T at agent
// construction, injects a framework-managed `final_response` tool whose
// arguments match the schema, and instructs the model to reply by
// calling it. The framework short-circuits the run as soon as the model
// invokes that tool: its `output` argument becomes res.Output, ready for
// Decode[T] to unmarshal into a typed Go value.
//
// Works on every provider — Anthropic too, where native response_format
// is unavailable. Strict-mode invariants from the schema generator
// (additionalProperties:false, required fields, enum / range constraints)
// flow through so the model gets a tight contract.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	go run examples/02_structured/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

// AnalysisResult is the structured output we expect from the agent.
type AnalysisResult struct {
	Sentiment string   `json:"sentiment" jsonschema:"description=Sentiment verdict,enum=Positive,enum=Negative,enum=Neutral,required"`
	Score     float64  `json:"score" jsonschema:"description=Confidence 0-1,minimum=0,maximum=1"`
	Keywords  []string `json:"keywords" jsonschema:"description=Key topics found"`
}

func main() {
	ctx := context.Background()

	p := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))

	agent := looper.MustNewAgent(p,
		"You are a sentiment analysis assistant. Be precise.",
		looper.WithStructuredOutput[AnalysisResult](),
	)

	res, err := agent.Run(ctx, "Analyze this: 'I absolutely love this product, it's amazing!'")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	var out AnalysisResult
	if err := looper.Decode(res, &out); err != nil {
		fmt.Fprintf(os.Stderr, "decode failed: %v\nraw output: %s\n", err, res.Output)
		os.Exit(1)
	}

	fmt.Printf("Sentiment: %s\n", out.Sentiment)
	fmt.Printf("Score:     %.2f\n", out.Score)
	fmt.Printf("Keywords:  %v\n", out.Keywords)
	fmt.Printf("Cost:      $%.6f  (%d turns)\n", res.Cost.TotalUSD, res.Turns)
}

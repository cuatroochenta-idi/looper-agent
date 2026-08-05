// Example: Claude Opus 4.8 on Amazon Bedrock — tool call + structured output.
//
// Bedrock authenticates with AWS SigV4 instead of an Anthropic API key, so
// the provider is built with an EMPTY key plus bedrock.WithConfig, which
// rewrites the request for the classic inference endpoint
// (https://bedrock-runtime.<region>.amazonaws.com) and signs it.
//
// The agent looks up a (fake) inventory record through a tool, then returns
// a typed Go struct via WithStructuredOutput. Both work on Bedrock exactly
// as they do on the first-party API — the wire shape is identical, only the
// transport differs.
//
// Model id: Bedrock prefixes the first-party id with "anthropic.", so
// Opus 4.8 is "anthropic.claude-opus-4-8". The provider strips that prefix
// when resolving model capabilities, so it correctly sends adaptive
// thinking and drops the sampling parameters Opus 4.7+ rejects.
//
// Usage:
//
//	export AWS_ACCESS_KEY_ID=AKIA...
//	export AWS_SECRET_ACCESS_KEY=...
//	export AWS_REGION=us-east-1              # optional, defaults to us-east-1
//	# export AWS_SESSION_TOKEN=...           # only for temporary/STS creds
//	go run examples/22_aws_bedrock_anthropic/main.go
//
// Requires Bedrock model access for Claude Opus 4.8 to be enabled in that
// region (AWS console → Bedrock → Model access) and an IAM policy allowing
// bedrock:InvokeModel.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// StockLookupIn is the tool's input schema.
type StockLookupIn struct {
	SKU string `json:"sku" jsonschema:"description=The product SKU to look up,required"`
}

// StockReport is the structured output we expect back from the agent.
type StockReport struct {
	SKU       string `json:"sku"       jsonschema:"description=The SKU that was looked up,required"`
	Product   string `json:"product"   jsonschema:"description=Human-readable product name,required"`
	Units     int    `json:"units"     jsonschema:"description=Units currently in stock,required"`
	Status    string `json:"status"    jsonschema:"description=Stock status,enum=InStock,enum=Low,enum=OutOfStock,required"`
	Reasoning string `json:"reasoning" jsonschema:"description=One sentence explaining the status call"`
}

// inventory stands in for whatever real system you'd query here.
var inventory = map[string]struct {
	Name  string
	Units int
}{
	"SKU-1021": {"Aeron Chair, Size B", 3},
	"SKU-4407": {"Standing Desk 160x80", 48},
	"SKU-9002": {"Monitor Arm, Dual", 0},
}

func main() {
	ctx := context.Background()

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	keyID := os.Getenv("AWS_ACCESS_KEY_ID")
	secret := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if keyID == "" || secret == "" {
		fmt.Fprintln(os.Stderr, "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
		os.Exit(1)
	}

	// Static credentials keep the example explicit about what Bedrock
	// needs. In production prefer awsconfig.LoadDefaultConfig(ctx) on its
	// own, which picks up instance roles, SSO, and the shared credentials
	// file — no keys in the environment at all.
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(keyID, secret, os.Getenv("AWS_SESSION_TOKEN")),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aws config: %v\n", err)
		os.Exit(1)
	}

	// Empty API key: Bedrock authenticates with SigV4, and an empty
	// x-api-key header would invalidate the signature.
	p := anthropic.NewProvider("",
		anthropic.WithRequestOptions(
			// WithoutEnvironmentDefaults is REQUIRED here. Without it the
			// SDK runs its own credential resolution first (ANTHROPIC_API_KEY,
			// then the ant profile) and aborts with "no Anthropic credentials
			// found" before the Bedrock middleware ever signs the request.
			option.WithoutEnvironmentDefaults(),
			bedrock.WithConfig(cfg),
		),
		anthropic.WithModel("anthropic.claude-opus-4-8"),
		// max_tokens covers thinking AND the visible reply. At effort
		// "high" Opus 4.8 can spend a 4k budget entirely on thinking and
		// get truncated before it emits the tool call — leave real
		// headroom. Anthropic's guidance is 64k+ at xhigh/max.
		anthropic.WithMaxTokens(16384),
		// Opus 4.8 takes adaptive thinking + effort; the provider picks
		// that shape automatically from the model id. "low" suits a
		// scoped lookup like this one and keeps the example fast.
		anthropic.WithEffort("low"),
		// Label the telemetry so Bedrock traffic is distinguishable from
		// calls to api.anthropic.com in cost reports.
		anthropic.WithProviderID("bedrock"),
	)

	stockTool := tool.MustNewTool(StockLookupIn{},
		func(_ context.Context, in StockLookupIn) (string, error) {
			fmt.Printf("[tool] looking up %s\n", in.SKU)
			item, ok := inventory[strings.ToUpper(in.SKU)]
			if !ok {
				return fmt.Sprintf("No inventory record found for %q.", in.SKU), nil
			}
			return fmt.Sprintf("%s — %d units on hand.", item.Name, item.Units), nil
		},
		tool.ToolConfig{
			Name:        "lookup_stock",
			Description: "Look up current inventory for a product SKU. Returns the product name and unit count.",
		},
	)

	agent := looper.MustNewAgent(p,
		"You are an inventory assistant. Always call lookup_stock before "+
			"answering — never guess stock levels. Treat 0 units as OutOfStock, "+
			"1-9 as Low, and 10 or more as InStock.",
		stockTool,
		looper.WithStructuredOutput[StockReport](),
	)

	res, err := agent.Run(ctx, "What's the stock situation for SKU-1021?")
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		os.Exit(1)
	}

	var out StockReport
	if err := looper.Decode(res, &out); err != nil {
		fmt.Fprintf(os.Stderr, "decode failed: %v\nraw output: %s\n", err, res.Output)
		os.Exit(1)
	}

	fmt.Printf("\nSKU:       %s\n", out.SKU)
	fmt.Printf("Product:   %s\n", out.Product)
	fmt.Printf("Units:     %d\n", out.Units)
	fmt.Printf("Status:    %s\n", out.Status)
	fmt.Printf("Why:       %s\n", out.Reasoning)
	fmt.Printf("\nCost:      $%.6f  (%d turns, %d in / %d out tokens)\n",
		res.Cost.TotalUSD, res.Turns, res.Cost.InputTokens, res.Cost.OutputTokens)
}

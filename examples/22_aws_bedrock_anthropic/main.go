// Example: Claude Opus 4.8 on Amazon Bedrock — tool call + structured output.
//
// The agent looks up a (fake) inventory record through a tool, then returns
// a typed Go struct. Both work on Bedrock exactly as on the first-party
// API — the wire shape is identical, only the transport differs.
//
// Three things are Bedrock-specific, all visible below:
//
//   - Auth is AWS SigV4, so the provider takes an EMPTY API key plus
//     bedrock.WithConfig. It also needs option.WithoutEnvironmentDefaults(),
//     or the SDK resolves its own credentials first and fails with "no
//     Anthropic credentials found" before the request is ever signed.
//   - Model ids are prefixed. Current Anthropic models are not invocable
//     on on-demand throughput with the bare id — they need a cross-region
//     inference profile, which adds a geo on top ("us.anthropic.…").
//     See inferenceProfile.
//   - The cost registry is keyed by provider, so leave the provider id
//     alone: relabelling with WithProviderID("bedrock") means no pricing
//     table and $0 on every call.
//
// Usage:
//
//	export AWS_ACCESS_KEY_ID=AKIA...
//	export AWS_SECRET_ACCESS_KEY=...
//	export AWS_REGION=us-east-1              # optional, defaults to us-east-1
//	# export AWS_SESSION_TOKEN=...           # only for temporary/STS creds
//	go run ./examples/22_aws_bedrock_anthropic
//
// Requires Bedrock model access for Claude Opus 4.8 in that region (AWS
// console → Bedrock → Model access) and an IAM policy allowing
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
	// needs. In production prefer awsconfig.LoadDefaultConfig(ctx) alone,
	// which picks up instance roles, SSO, and the shared credentials file.
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

	p := anthropic.NewProvider("",
		anthropic.WithRequestOptions(
			option.WithoutEnvironmentDefaults(),
			bedrock.WithConfig(cfg),
		),
		anthropic.WithModel(inferenceProfile(region, "anthropic.claude-opus-4-8")),
		// max_tokens covers thinking AND the visible reply, so a tight
		// budget at high effort can be spent entirely on thinking and get
		// truncated before the tool call. Anthropic suggests 64k+ at
		// xhigh/max; "low" effort suits a scoped lookup like this one.
		anthropic.WithMaxTokens(16384),
		anthropic.WithEffort("low"),
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
		fmt.Fprintf(os.Stderr, "decode failed: %v\nraw output: %q\n", err, res.Output)
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

// inferenceProfile turns a Bedrock model id into a cross-region inference
// profile id for the given region, by prepending the geography that routes
// it. Without this, Bedrock answers:
//
//	Invocation of model ID anthropic.claude-opus-4-8 with on-demand
//	throughput isn't supported. Retry your request with the ID or ARN of
//	an inference profile that contains this model.
func inferenceProfile(region, modelID string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		return "us-gov." + modelID
	case strings.HasPrefix(region, "us-"):
		return "us." + modelID
	case strings.HasPrefix(region, "eu-"):
		return "eu." + modelID
	case strings.HasPrefix(region, "ap-"):
		return "apac." + modelID
	default:
		// Unknown geography — let Bedrock report what it supports there.
		return modelID
	}
}

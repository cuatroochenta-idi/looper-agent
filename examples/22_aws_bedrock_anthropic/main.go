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
// Model id: Bedrock prefixes the first-party id with "anthropic.", and
// current Anthropic models additionally require a CROSS-REGION INFERENCE
// PROFILE rather than the bare id — invoking "anthropic.claude-opus-4-8"
// on on-demand throughput fails with "Retry your request with the ID or
// ARN of an inference profile that contains this model". The profile id
// adds a geo prefix, so in us-east-1 it is "us.anthropic.claude-opus-4-8"
// (see inferenceProfile below). The provider strips both prefixes when
// resolving model capabilities, so it still sends adaptive thinking and
// drops the sampling parameters Opus 4.7+ rejects.
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
		anthropic.WithModel(inferenceProfile(region, "anthropic.claude-opus-4-8")),
		// max_tokens covers thinking AND the visible reply. At effort
		// "high" Opus 4.8 can spend a 4k budget entirely on thinking and
		// get truncated before it emits the tool call — leave real
		// headroom. Anthropic's guidance is 64k+ at xhigh/max.
		anthropic.WithMaxTokens(16384),
		// Opus 4.8 takes adaptive thinking + effort; the provider picks
		// that shape automatically from the model id. "low" suits a
		// scoped lookup like this one and keeps the example fast.
		anthropic.WithEffort("low"),
		// Note: WithProviderID("bedrock") would relabel the telemetry, but
		// the cost registry is keyed by provider — a custom label has no
		// pricing table and every call reports $0. Relabel only if you
		// also register rates under that name.
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
		dumpHistory(res)
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

// dumpHistory prints the full conversation when the run produces nothing
// usable. An empty Output with status "completed" means the loop treated
// some turn as the final answer; the history is the only place that shows
// which turn that was and what the model actually sent — in particular
// whether it called final_response with usable arguments.
func dumpHistory(res *looper.RunResult) {
	fmt.Fprintf(os.Stderr, "\n--- history (%d turns, status=%s) ---\n", res.Turns, res.Status)
	for i, m := range res.History.Messages() {
		fmt.Fprintf(os.Stderr, "[%d] type=%s", i, m.Type)
		if m.Name != "" {
			fmt.Fprintf(os.Stderr, " name=%s", m.Name)
		}
		if m.Content != "" {
			fmt.Fprintf(os.Stderr, " content=%q", m.Content)
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(os.Stderr, "\n      tool_call name=%s args=%s", tc.Name, string(tc.Arguments))
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "--- end history ---\n")
}

// inferenceProfile turns a Bedrock model id into a cross-region inference
// profile id for the given region.
//
// Current Anthropic models are not invocable on Bedrock through on-demand
// throughput with the bare model id — the API answers:
//
//	Invocation of model ID anthropic.claude-opus-4-8 with on-demand
//	throughput isn't supported. Retry your request with the ID or ARN of
//	an inference profile that contains this model.
//
// The profile id is the model id with a geo prefix ("us.anthropic.…"),
// which routes across the regions in that geography. Provisioned
// throughput is the alternative; it needs a reserved capacity commitment.
func inferenceProfile(region, modelID string) string {
	var geo string
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		geo = "us-gov."
	case strings.HasPrefix(region, "us-"):
		geo = "us."
	case strings.HasPrefix(region, "eu-"):
		geo = "eu."
	case strings.HasPrefix(region, "ap-"):
		geo = "apac."
	default:
		// Unknown geography — fall back to the bare id and let Bedrock
		// report what it supports there.
		return modelID
	}
	return geo + modelID
}

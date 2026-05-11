package looper

import (
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// RunResult contains the complete outcome of an agent execution.
type RunResult struct {
	// Output is the final text output from the agent.
	Output string

	// History contains the full conversation history after the run.
	// Can be serialized and restored for later runs.
	History *message.History

	// Cost provides a detailed cost breakdown for the entire run.
	Cost CostBreakdown

	// Usage reports total token consumption.
	Usage Usage

	// Turns is the number of loop turns executed.
	Turns int

	// Status indicates how the run ended: "completed", "error", "cancelled", "paused".
	Status string
}

// CostBreakdown provides detailed cost information for a run.
type CostBreakdown struct {
	TotalUSD     float64
	InputUSD     float64
	OutputUSD    float64
	CachedUSD    float64
	SavingsUSD   float64
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

// Usage reports token consumption for a run.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

// ToolConfig is a convenience re-export for basic usage.
// For full configuration, use tool.ToolConfig directly.
type ToolConfig = tool.ToolConfig

// Package web implements the Looper Agent debug panel: a read-only viewer
// of agent runs persisted in-memory. The UI is built with a-h/templ
// components on the server side and reactive bindings via datastar on the
// client. Step events emitted by the framework loop are converted into
// TimelineStep records and rendered as a hierarchical trace.
package web

import (
	"context"
	"time"
)

// ─── Step / runner contract ───────────────────────────────────────────────────

// StepKind classifies a single event in an agent run.
type StepKind string

const (
	StepKindSystemPrompt StepKind = "system_prompt"
	StepKindLLMCall      StepKind = "llm_call"
	StepKindToolCall     StepKind = "tool_call"
	StepKindToolResult   StepKind = "tool_result"
	StepKindFinal        StepKind = "final_response"
	StepKindUserInput    StepKind = "user_input"
	StepKindError        StepKind = "error"
	// StepKindReasoning carries an extended-thinking delta. Rendered apart
	// from the model's visible text so the operator can fold it away. The
	// string must match loop.StepReasoningChunk on the wire.
	StepKindReasoning StepKind = "reasoning_chunk"
	// StepKindStreamingChunk is a delta of the model's visible text response
	// emitted during a streaming turn. The trace builder accumulates these
	// per-turn into TurnNode.AssistantText so the operator can read what the
	// model actually said — including the "thought" text emitted alongside a
	// tool call.
	StepKindStreamingChunk StepKind = "streaming_chunk"
	// StepKindLLMResponse fires after each LLM call returns and carries
	// the turn's provenance — provider, model, fallback flag, usage.
	// Distinct from llm_call (which is the pre-call marker / spinner).
	StepKindLLMResponse StepKind = "llm_response"
)

// StepEvent is the per-step record the web UI consumes from a runner.
type StepEvent struct {
	Kind         StepKind
	Turn         int
	Content      string
	ToolName     string
	ToolArgs     string
	ToolCallID   string
	Err          string
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

// RunSummary is the final aggregate returned once a run finishes.
type RunSummary struct {
	Output       string
	Status       string
	Turns        int
	TotalUSD     float64
	InputTokens  int
	OutputTokens int
	CachedTokens int
	Err          error
}

// RunFunc is the contract between the web UI and the framework.
type RunFunc func(ctx context.Context, input string) (<-chan StepEvent, <-chan RunSummary, error)

// ─── Stored run model ─────────────────────────────────────────────────────────

// RunStatus identifies the lifecycle stage of a run.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunError     RunStatus = "error"
)

// TimelineStep is a single step persisted on a run for the detail view.
type TimelineStep struct {
	Kind         StepKind
	Turn         int
	Content      string
	ToolName     string
	ToolArgs     string
	ToolCallID   string
	Err          string
	At           time.Time
	InputTokens  int
	OutputTokens int
	CachedTokens int

	// Provider / Model / Fallback are populated on usage-bearing steps
	// (StepKindLLMResponse, StepKindToolCall, StepKindFinal) so the
	// turn aggregator can stamp the (provider, model) bucket and flag
	// fallback calls in the trace UI. Empty on non-LLM steps.
	Provider string
	Model    string
	Fallback bool
}

// ProviderStat is the per-(Provider, Model) breakdown shown in the run
// header. Mirrors looper.ProviderStatsData but lives in the UI layer
// so the templ files don't import the framework's wire types directly.
type ProviderStat struct {
	Provider      string  `json:"provider"`
	Model         string  `json:"model"`
	Calls         int     `json:"calls"`
	FallbackCalls int     `json:"fallback_calls,omitempty"`
	InputTokens   int     `json:"input_tokens,omitempty"`
	OutputTokens  int     `json:"output_tokens,omitempty"`
	CachedTokens  int     `json:"cached_tokens,omitempty"`
	TotalUSD      float64 `json:"total_usd,omitempty"`
}

// RunRecord is the in-memory snapshot of an agent run.
type RunRecord struct {
	ID               string         `json:"id"`
	SessionID        string         `json:"session_id,omitempty"`
	ParentRunID      string         `json:"parent_run_id,omitempty"`
	ParentToolCallID string         `json:"parent_tool_call_id,omitempty"`
	Project          string         `json:"project,omitempty"`
	Input        string         `json:"input"`
	Output       string         `json:"output"`
	Status       RunStatus      `json:"status"`
	Turns        int            `json:"turns"`
	TotalUSD     float64        `json:"total_usd"`
	Tokens       int            `json:"tokens"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	CachedTokens int            `json:"cached_tokens"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      time.Time      `json:"ended_at,omitzero"`
	Steps        []TimelineStep `json:"steps,omitempty"`

	// Providers is the per-(Provider, Model) breakdown when the run
	// used a multiprovider chain. Empty when single-provider.
	Providers []ProviderStat `json:"providers,omitempty"`

	// FallbackCalls is the total number of LLM calls that hit the
	// failover path during this run.
	FallbackCalls int `json:"fallback_calls,omitempty"`
}

// Duration returns the wall-clock time the run has been or was running.
func (r *RunRecord) Duration() time.Duration {
	end := r.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(r.StartedAt)
}

// IsStuck returns true when the run is still "running" but no events have
// landed in idleThreshold. The sweeper auto-finalizes after a longer
// window; this lower threshold lets the chat bubble flag a probably-dead
// run before the sweep tick fires so operators don't stare at a fake
// "thinking…" spinner.
func (r *RunRecord) IsStuck(idleThreshold time.Duration) bool {
	if r.Status != RunRunning {
		return false
	}
	last := r.StartedAt
	if n := len(r.Steps); n > 0 {
		if t := r.Steps[n-1].At; t.After(last) {
			last = t
		}
	}
	return time.Since(last) > idleThreshold
}

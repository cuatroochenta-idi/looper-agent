package loop

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// TurnSnapshot is the read-only view of a single completed turn that a
// TurnValidator inspects. A turn is "complete" once the LLM has responded
// and any tool calls it requested have been executed and their results
// recorded in the history.
type TurnSnapshot struct {
	// Turn is the 0-indexed turn number within the run.
	Turn int

	// LastAssistantContent is the assistant's textual output for this turn.
	// Empty when the turn produced only tool calls.
	LastAssistantContent string

	// ToolCalls is the set of tool invocations the LLM requested this turn.
	// Empty when the LLM produced a final text response.
	ToolCalls []message.ToolCall

	// ToolResults are the results of executing ToolCalls in this turn,
	// preserving order. Empty on final-text turns.
	ToolResults []message.ToolResult

	// History is the conversation history including this turn. The
	// validator may inspect it but must not mutate — mutations bypass the
	// retry budget and break invariants other hooks rely on.
	History *message.History
}

// Outcome is the validator's verdict for a turn.
//
// When OK is true the loop proceeds normally — emits the final response or
// continues to the next LLM call. When OK is false the loop adds Hint as a
// system message and re-prompts the model, up to the configured retry budget.
//
// The retry budget is per consecutive failure streak: a single OK resets it.
//
// SkipBudget — use on tool-call turns where the validator wants to inject a
// corrective hint without penalising the budget. This is the idiomatic signal
// for "the model is still in the middle of a multi-step plan; I want to steer
// it, but the plan isn't failing yet". Without SkipBudget, a long but healthy
// execution chain (plan_tasks → update_task → add_tables → …) exhausts
// validatorMaxRetries and aborts with "validation_exhausted" even though every
// individual turn was working correctly.
//
// Semantics summary:
//
//	OK=true              → accept, reset budget counter.
//	OK=false, SkipBudget=false → inject Hint + decrement budget (current default).
//	OK=false, SkipBudget=true  → inject Hint but do NOT touch the budget counter.
type Outcome struct {
	// OK indicates the turn is acceptable. When true Hint/Reason are ignored.
	OK bool

	// Reason explains why the turn was rejected. Surfaced via telemetry and
	// the StepError that fires on validation_exhausted. Not shown to the LLM.
	Reason string

	// Hint is the corrective instruction shown to the LLM as a system message
	// on the next turn. Required when OK is false.
	Hint string

	// SkipBudget prevents the rejection from counting against the retry
	// budget. The Hint is still injected so the model receives the steering
	// signal. Only meaningful when OK is false; ignored when OK is true.
	//
	// Canonical use case: mid-batch tool progress where the validator wants
	// to guide without budgeting against the failure counter.
	SkipBudget bool
}

// TurnValidator inspects every completed turn and decides whether the loop
// should continue, re-prompt the model with a hint, or stop entirely.
//
// Implementations may run synchronous logic (regex checks, allowlists,
// state-tracker queries) or call out to a grader LLM. Errors are reported
// by returning Outcome{OK: false, Reason: "validator: ..."} — a validator
// that itself fails should not crash the loop.
//
// # Semantics by turn shape
//
// On text-only turns (snap.ToolCalls is empty):
//   - proceed=true  → record the final response and return.
//   - proceed=false → inject Hint, iterate (model will re-prompt).
//
// On tool-call turns (snap.ToolCalls is non-empty):
//   - proceed is IGNORED — the loop always continues because tools have
//     already executed and their side-effects cannot be rolled back.
//   - To signal "rejected without burning budget", set Outcome.SkipBudget.
//   - To stop the run cleanly from inside a tool body, set
//     message.ToolResult.Halt on the result the tool returns.
type TurnValidator interface {
	Validate(ctx context.Context, snap TurnSnapshot) Outcome
}

// TurnValidatorFunc adapts a plain function to the TurnValidator interface,
// so callers can register inline validators without defining a struct.
type TurnValidatorFunc func(ctx context.Context, snap TurnSnapshot) Outcome

// Validate makes TurnValidatorFunc satisfy the TurnValidator interface.
func (f TurnValidatorFunc) Validate(ctx context.Context, snap TurnSnapshot) Outcome {
	return f(ctx, snap)
}

// WithLoopTurnValidator attaches a TurnValidator to the loop with the given
// retry budget. The budget counts consecutive rejections — a single OK
// resets it. A budget of zero allows zero retries (rejection is terminal).
func WithLoopTurnValidator(v TurnValidator, maxRetries int) LoopOption {
	return func(l *AgentLoop) {
		l.validator = v
		l.validatorMaxRetries = maxRetries
	}
}

// validateTurn runs the configured TurnValidator and returns three booleans:
//
//   - proceed: true when the loop should continue past this turn (either no
//     validator is set, or the validator accepted the turn). false when the
//     hint has been added and the loop should iterate again.
//   - abort: true when the validator rejected and the retry budget is
//     exhausted — the caller must stop emitting steps and return.
//   - outcome: the Outcome returned by the validator (or a synthetic OK one).
//
// failures is a pointer so the caller's counter can be mutated in place,
// keeping the per-run state with the Iterator rather than the AgentLoop
// (which is shared across runs).
//
// # Semantics by turn shape
//
// On text-only turns: proceed=true → record final + return;
// proceed=false → inject Hint + iterate.
//
// On tool-call turns: proceed is ignored — the loop always continues because
// tools already executed. To reject without burning budget, use
// Outcome.SkipBudget. To stop the run from a tool body, set
// message.ToolResult.Halt on the result the tool returns.
func (l *AgentLoop) validateTurn(
	ctx context.Context,
	snap TurnSnapshot,
	failures *int,
) (proceed bool, abort bool, outcome Outcome) {
	if l.validator == nil {
		return true, false, Outcome{OK: true}
	}
	out := l.validator.Validate(ctx, snap)
	if out.OK {
		*failures = 0
		return true, false, out
	}
	// SkipBudget: inject the hint but don't touch the failure counter.
	// The run continues; the budget is preserved for genuine failures.
	if out.SkipBudget {
		if out.Hint != "" {
			snap.History.AddSystemMessage(out.Hint)
		}
		return false, false, out
	}
	if *failures < l.validatorMaxRetries {
		if out.Hint != "" {
			snap.History.AddSystemMessage(out.Hint)
		}
		*failures++
		return false, false, out
	}
	// Retry budget exhausted — signal the caller to stop.
	return false, true, out
}

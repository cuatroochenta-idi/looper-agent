// Package loop implements the agentic loop engine that powers
// the Looper Agent framework. It manages the iterative LLM → tool
// execution → result feedback cycle with hooks, memory management,
// and concurrency control.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
	"github.com/cuatroochenta-idi/looper-agent/tool"

	"golang.org/x/sync/errgroup"
)

// StepType identifies the type of step in the agentic loop.
type StepType string

const (
	StepSystemPrompt StepType = "system_prompt"
	StepLLMCall      StepType = "llm_call"
	// StepLLMResponse fires after the LLM call returns (or the streaming
	// final chunk arrives) and carries the turn's provenance —
	// ProviderID, ModelID, Fallback, Usage. Trace consumers use it to
	// attribute the turn to the (provider, model) bucket without
	// scanning every later step for the same data. Emitted once per
	// turn, in addition to (not in place of) the existing StepLLMCall
	// marker that fires BEFORE the call.
	StepLLMResponse    StepType = "llm_response"
	StepStreamingChunk StepType = "streaming_chunk"
	// StepReasoningChunk carries an extended-thinking / reasoning delta
	// from the model on its own channel, so consumers can render it apart
	// from regular text (collapsed, faint, behind a toggle, etc.).
	StepReasoningChunk StepType = "reasoning_chunk"
	StepToolCall       StepType = "tool_call"
	StepToolResult     StepType = "tool_result"
	StepFinalResponse  StepType = "final_response"
	StepError          StepType = "error"
)

// Step represents a single event in the agentic loop.
//
// For StepToolResult, IsError mirrors message.ToolResult.IsError so that
// downstream consumers (UI streams, audit logs, retry counters) don't have
// to parse Content to decide whether the call failed. The framework sets it
// to true on tool-function errors AND on framework-level rejections
// (schema validation, unknown tool, cancellation). It's false for any
// successful execution, even when the tool's payload uses a JSON "ok": false
// convention — that distinction is the tool's responsibility, not the loop's.
//
// Halt is set on StepToolResult when the tool requested a clean termination
// of the run (e.g. request_user_decision, end_of_conversation). The loop
// stops after emitting this step and sets RunResult.Status to "halted_by_tool".
type Step struct {
	Type       StepType
	Content    string
	ToolName   string
	ToolArgs   string
	ToolCallID string // matches StepToolCall ↔ StepToolResult pairs
	Turn       int
	Error      error
	// IsError is true when the tool execution resulted in an error.
	IsError bool
	// Halt is true when the tool requested a clean termination of the run.
	Halt bool
	// Usage is populated on the final chunk of an LLM call (one per turn).
	// Nil on tool / system / intermediate streaming chunk steps.
	Usage *provider.Usage

	// ProviderID / ModelID identify which provider+model actually answered
	// the LLM call this step belongs to. Populated on usage-carrying
	// steps (StepStreamingChunk's final / StepToolCall / StepFinalResponse)
	// so trace consumers can attribute each turn to the right cost-table
	// entry. Empty on non-LLM steps and on legacy providers that don't
	// set the provenance on LLMResponse / StreamChunk.
	ProviderID string
	ModelID    string

	// Fallback is true when the LLM call backing this step was answered
	// by a non-primary inner of a FailoverProvider (i.e. the chain
	// switched away from the primary). Always false when the primary
	// answered or when no failover is wired.
	Fallback bool

	// APIKeySuffix mirrors provider.StreamChunk.APIKeySuffix /
	// provider.LLMResponse.APIKeySuffix — the "****xxxx" surface of the
	// API key that actually served the call. Stamped on every
	// StepStreamingChunk and on StepLLMResponse so the trace UI can show
	// per-fragment attribution when a KeyRotationProvider or
	// FailoverProvider chain mixes keys across one run. Empty for
	// keyless providers (LM Studio, Ollama) and on non-LLM steps.
	APIKeySuffix string
}

// RunResult contains the outcome of an agent run.
type RunResult struct {
	Output  string
	History *message.History
	Cost    CostBreakdown
	Usage   Usage
	Turns   int
	Status  string

	// Providers reports the per-(Provider, Model) breakdown. One entry
	// per distinct (provider, model) that answered any LLM call in the
	// run, in first-seen order. Single-provider runs emit one entry;
	// multiprovider chains (FailoverProvider with several inners, or
	// chains that mix models) emit several. Cost on each entry uses
	// that (provider, model)'s rate, so the totals reconcile with Cost
	// even when the run was served by several providers.
	Providers []ProviderStats

	// FallbackCalls is the total count of LLM calls in the run that
	// were answered by a non-primary inner of a FailoverProvider (or
	// any provider that set LLMResponse.Fallback / StreamChunk.Fallback).
	// Zero when the chain ran on the primary the entire time. Cheap
	// "did this run hit the fallback path?" signal for telemetry.
	FallbackCalls int
}

// CostBreakdown reports cost information for a run.
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

// Usage reports token usage.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

// AgentLoop manages the iterative LLM → tool execution cycle.
type AgentLoop struct {
	mu                  sync.Mutex
	provider            provider.LLMProvider
	systemPrompt        func(context.Context) string
	tools               []*tool.Tool
	hooks               *HookManager
	memoryMgr           memory.MemoryManager
	pauseMgr            *pause.PauseManager
	costModel           *telemetry.CostModel
	maxTurns            int
	maxRetries          int
	model               string
	temperature         float64
	structuredOutput    json.RawMessage // schema for final_response tool
	reasoning           *provider.ReasoningConfig
	validator           TurnValidator
	validatorMaxRetries int
	dynamicTools        DynamicToolsFunc
	toolChoice          provider.ToolChoice
	usageLimits         UsageLimits

	// Structured-output validation: when a compiled schema is wired AND
	// outputMaxRetries > 0, every candidate output is validated against
	// the schema (and the optional custom validator) before being
	// declared final. Rejections add a system-message hint and re-prompt
	// the model, up to outputMaxRetries. Exhausting the budget terminates
	// the run with Status="output_validation_exhausted", last payload
	// preserved in Output.
	structuredOutputCompiled outputValidatorFunc
	outputMaxRetries         int
	outputCustomValidator    outputValidatorFunc
}

// outputValidatorFunc returns nil when the raw JSON output is acceptable
// and a typed error otherwise. Both the compiled-schema check and the
// user-supplied business validator share this shape so the loop's
// validation pipeline can chain them.
type outputValidatorFunc func(raw []byte) error

// LoopOption configures an AgentLoop.
type LoopOption func(*AgentLoop)

// WithLoopMaxTurns sets the maximum turns before the loop aborts.
func WithLoopMaxTurns(n int) LoopOption {
	return func(l *AgentLoop) { l.maxTurns = n }
}

// WithLoopMaxRetries sets the maximum consecutive tool retries.
func WithLoopMaxRetries(n int) LoopOption {
	return func(l *AgentLoop) { l.maxRetries = n }
}

// WithLoopMemory sets the memory manager.
func WithLoopMemory(mm memory.MemoryManager) LoopOption {
	return func(l *AgentLoop) { l.memoryMgr = mm }
}

// WithLoopPause sets the pause manager.
func WithLoopPause(pm *pause.PauseManager) LoopOption {
	return func(l *AgentLoop) { l.pauseMgr = pm }
}

// WithLoopModel overrides the provider's default model.
func WithLoopModel(model string) LoopOption {
	return func(l *AgentLoop) { l.model = model }
}

// WithLoopTemperature sets the LLM temperature.
func WithLoopTemperature(t float64) LoopOption {
	return func(l *AgentLoop) { l.temperature = t }
}

// WithLoopStructuredOutput enables structured output by injecting a
// final_response tool with the given JSON schema. The system prompt
// is augmented with instructions to use this tool.
func WithLoopStructuredOutput(schema json.RawMessage) LoopOption {
	return func(l *AgentLoop) { l.structuredOutput = schema }
}

// WithLoopCostModel sets the price table used to translate token usage into
// USD on every run result.
func WithLoopCostModel(cm *telemetry.CostModel) LoopOption {
	return func(l *AgentLoop) { l.costModel = cm }
}

// WithLoopReasoning sets the default reasoning configuration for every LLM
// call this loop makes. Nil disables reasoning. Per-provider mapping: see
// provider.ReasoningConfig.
func WithLoopReasoning(rc *provider.ReasoningConfig) LoopOption {
	return func(l *AgentLoop) { l.reasoning = rc }
}

// WithLoopToolChoice sets the default ToolChoice for every LLM call this
// loop makes. The zero value (provider.ToolChoice{}) means "auto" — same
// as not setting it at all. Per-provider mapping: see provider.ToolChoice.
func WithLoopToolChoice(c provider.ToolChoice) LoopOption {
	return func(l *AgentLoop) { l.toolChoice = c }
}

// WithLoopOutputValidation wires the structured-output validation
// pipeline: a compiled JSON-Schema check (built by the agent package
// from T) plus an optional custom business-rule validator. maxRetries
// > 0 enables the re-prompt loop; zero leaves the current legacy
// "first JSON wins" behaviour intact.
func WithLoopOutputValidation(
	schemaValidator outputValidatorFunc,
	maxRetries int,
	customValidator outputValidatorFunc,
) LoopOption {
	return func(l *AgentLoop) {
		l.structuredOutputCompiled = schemaValidator
		l.outputMaxRetries = maxRetries
		l.outputCustomValidator = customValidator
	}
}

// validateStructuredOutput runs every configured validator on raw and
// returns nil + ok=true when the output passes. On failure, returns
// false with a hint string ready to inject as a system message.
//
// Order matters: the schema check runs first (cheap, catches the bulk
// of model mistakes), then the optional custom validator (business
// rules that only make sense once the shape is sound).
func (l *AgentLoop) validateStructuredOutput(raw string) (ok bool, hint string) {
	if l.structuredOutputCompiled == nil && l.outputCustomValidator == nil {
		return true, ""
	}
	bytes := []byte(raw)
	if l.structuredOutputCompiled != nil {
		if err := l.structuredOutputCompiled(bytes); err != nil {
			return false, fmt.Sprintf(
				"Your previous reply did not match the required schema: %v. "+
					"Apply the required changes and reply again with a single "+
					"JSON object that satisfies the schema — do not include "+
					"prose, markdown fences, or commentary.",
				err,
			)
		}
	}
	if l.outputCustomValidator != nil {
		if err := l.outputCustomValidator(bytes); err != nil {
			return false, fmt.Sprintf(
				"Your previous reply was rejected: %v. Apply the required "+
					"changes and reply again with a corrected response.",
				err,
			)
		}
	}
	return true, ""
}

// validateStructuredOrAbort is the streaming-path wrapper around
// validateStructuredOutput. It mirrors the validator-budget protocol
// used by TurnValidator so the Iterator code at each commit site reads
// the same way: ok=commit, !ok && !abort = re-prompt, !ok && abort = stop.
func (it *Iterator) validateStructuredOrAbort(
	out string,
	history *message.History,
	retries *int,
) (ok, abort bool) {
	if it.loop.structuredOutput == nil {
		return true, false
	}
	pass, hint := it.loop.validateStructuredOutput(out)
	if pass {
		return true, false
	}
	if *retries < it.loop.outputMaxRetries {
		history.AddSystemMessage(hint)
		*retries++
		return false, false
	}
	return false, true
}

// gateStructuredClose runs the turn validator against a structured-output
// close — i.e. the model ended the run by emitting the framework-injected
// final_response tool. Without this gate the structured-output short-circuit
// commits the close directly and the TurnValidator never sees it, so a
// consumer-defined "you may not finish yet" rule is silently bypassed.
//
// On rejection it answers the otherwise-dangling final_response tool call with
// the corrective hint as an error result. That serves two purposes at once:
// it keeps the history provider-valid (an assistant message carrying
// tool_calls MUST be followed by matching tool results or OpenAI/Anthropic
// reject the next request with a 400), and it delivers the feedback in-band so
// the model sees that its close was refused and why — no separate system
// message, hence no ordering hazard between the assistant call and its result.
//
// Contract mirrors validateTurn: (proceed) accept the close, (abort) retry
// budget spent — stop the run, otherwise re-prompt. A nil validator accepts
// unconditionally, so validator-less consumers are unaffected.
func (l *AgentLoop) gateStructuredClose(
	ctx context.Context,
	toolCalls []message.ToolCall,
	final string,
	history *message.History,
	turn int,
	failures *int,
) (proceed, abort bool, out Outcome) {
	if l.validator == nil {
		return true, false, Outcome{OK: true}
	}
	out = l.validator.Validate(ctx, TurnSnapshot{
		Turn:                 turn,
		LastAssistantContent: final,
		ToolCalls:            toolCalls,
		History:              history,
	})
	if out.OK {
		*failures = 0
		return true, false, out
	}
	// Refuse the close in-band: every dangling tool call from this turn gets
	// an error result carrying the reason/hint. final_response is the only
	// expected call here, but answering all of them keeps history valid if a
	// provider streamed extra calls alongside it.
	rejection := closeRejectionResult(out)
	for _, tc := range toolCalls {
		history.AddToolResult(tc.ID, tc.Name, rejection, true)
	}
	if out.SkipBudget {
		return false, false, out
	}
	if *failures < l.validatorMaxRetries {
		*failures++
		return false, false, out
	}
	return false, true, out
}

// closeRejectionResult renders a rejected close as the JSON body of the
// final_response tool's error result. It is the model-facing explanation for
// why the run was not allowed to finish.
func closeRejectionResult(out Outcome) string {
	b, _ := json.Marshal(map[string]any{
		"ok":     false,
		"reason": out.Reason,
		"hint":   out.Hint,
	})
	return string(b)
}

// SetHookManager installs a pre-built hook manager (typically the one held by
// the Agent so external callers can register hooks via agent.On). Replaces
// the internal one created in NewAgentLoop.
func (l *AgentLoop) SetHookManager(h *HookManager) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h != nil {
		l.hooks = h
	}
}

// NewAgentLoop creates a new agentic loop engine.
func NewAgentLoop(p provider.LLMProvider, systemPrompt func(context.Context) string, tools []*tool.Tool, opts ...LoopOption) *AgentLoop {
	l := &AgentLoop{
		provider:     p,
		systemPrompt: systemPrompt,
		tools:        tools,
		hooks:        NewHookManager(),
		maxTurns:     10,
		maxRetries:   3,
		temperature:  0,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Run executes the full agentic loop and returns the final result.
func (l *AgentLoop) Run(ctx context.Context, input string, opts ...RunOption) (*RunResult, error) {
	cfg := l.resolveRunConfig(opts)

	history := cfg.history
	if history == nil {
		history = message.NewHistory()
	}
	// Skip the append when input is empty so callers can deliver the user
	// turn pre-populated in History (e.g. a multi-modal Parts message via
	// WithHistory). Always appending would inject a phantom empty user
	// message after the real one.
	if input != "" {
		history.AddUserMessage(input)
	}

	sysPrompt := l.resolveSystemPrompt(ctx)

	// Inject metadata into context
	for k, v := range cfg.metadata {
		ctx = context.WithValue(ctx, contextKey(k), v)
	}

	var (
		totalInputTokens         int
		totalOutputTokens        int
		totalCachedTokens        int
		status                   = "completed"
		consecutiveValidatorFail int
		outputRetriesUsed        int
		lastAttemptedOutput      string
		lastStructuredCandidate  string
		// stats tracks per-(provider, model) usage so RunResult.Cost is
		// correct when a FailoverProvider or any multi-provider chain
		// answers calls in a single run. Populated alongside the legacy
		// total* counters for backward compatibility.
		stats = newRunStats()
	)
	// finalizeBreakdown builds the (cost, providers, fallbackCalls)
	// triple that every return site below needs. Closure capture keeps
	// each RunResult literal short.
	finalizeBreakdown := func() (CostBreakdown, []ProviderStats, int) {
		return l.finalizeRun(stats)
	}

	for turn := 0; turn < l.maxTurns; turn++ {
		// Trigger BeforeCall hooks
		if err := l.hooks.Trigger(ctx, HookBeforeCall, &CallParams{
			History:      history,
			Turn:         turn,
			MaxTurns:     l.maxTurns,
			SystemPrompt: sysPrompt,
		}); err != nil {
			return nil, fmt.Errorf("BeforeCall hook: %w", err)
		}

		// Memory management
		if l.memoryMgr != nil {
			if err := l.memoryMgr.Manage(ctx, history); err != nil {
				log.Printf("loop: memory manager error: %v", err)
			}
		}

		// Build LLM request with tools (inject final_response if structured output)
		allTools := l.buildToolList(ctx, history)
		req := provider.LLMRequest{
			SystemPrompt: sysPrompt,
			Messages:     history.Messages(),
			Tools:        allTools,
			Temperature:  l.temperature,
			ToolChoice:   l.toolChoice,
		}
		if l.model != "" {
			req.Model = l.model
		}
		// Native structured output: only when the agent has no tools of its
		// own (see useNativeResponseFormat). With tools present, structured
		// output rides the final_response tool appended in buildToolList so
		// the model can still emit ordinary tool calls.
		if l.useNativeResponseFormat() {
			req.ResponseSchema = l.structuredOutput
		}

		// Call LLM
		llmResp, err := l.provider.Chat(ctx, req)
		if err != nil {
			status = "error"
			return nil, fmt.Errorf("llm call turn %d: %w", turn, err)
		}

		totalInputTokens += llmResp.Usage.InputTokens
		totalOutputTokens += llmResp.Usage.OutputTokens
		totalCachedTokens += llmResp.Usage.CachedTokens
		stats.add(llmResp, providerLabel(l.provider), l.model)

		// Add assistant message
		history.AddAssistantMessage(llmResp.Content, llmResp.ToolCalls)
		lastAttemptedOutput = llmResp.Content

		// Usage / cost limits — evaluated after every LLM call so the
		// FIRST cap to trip wins and no further requests are issued.
		breakdown, _, _ := finalizeBreakdown()
		if exceeded, _ := l.usageLimits.exceeds(turn+1, totalInputTokens+totalOutputTokens, breakdown.TotalUSD); exceeded {
			cost, providers, fc := finalizeBreakdown()
			return &RunResult{
				Output:        lastAttemptedOutput,
				History:       history,
				Cost:          cost,
				Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
				Turns:         turn + 1,
				Status:        "usage_exceeded",
				Providers:     providers,
				FallbackCalls: fc,
			}, nil
		}

		// Structured-output short-circuit: if the model called the
		// implicit final_response tool, its `output` argument IS the
		// run's final answer. We extract it instead of looping on the
		// tool's pass-through return value. When output-validation is
		// configured we validate the candidate against the compiled
		// schema (and any custom validator) and re-prompt on failure
		// up to outputMaxRetries.
		if l.structuredOutput != nil {
			if out, ok := extractFinalResponseOutput(llmResp.ToolCalls); ok {
				// Gate the close through the turn validator first (parity
				// with the streaming Iterator path): a premature
				// final_response is refused in-band and re-prompted, or the
				// run aborts once the retry budget is spent.
				_, abort, vout := l.gateStructuredClose(ctx, llmResp.ToolCalls, llmResp.Content, history, turn, &consecutiveValidatorFail)
				if abort {
					cost, providers, fc := finalizeBreakdown()
					return &RunResult{
						Output:        out,
						History:       history,
						Cost:          cost,
						Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
						Turns:         turn + 1,
						Status:        "validation_exhausted",
						Providers:     providers,
						FallbackCalls: fc,
					}, nil
				}
				if !vout.OK {
					// Rejection result added to history; re-prompt.
					continue
				}
				lastStructuredCandidate = out
				ok, hint := l.validateStructuredOutput(out)
				if !ok {
					if outputRetriesUsed < l.outputMaxRetries {
						history.AddSystemMessage(hint)
						outputRetriesUsed++
						continue
					}
					cost, providers, fc := finalizeBreakdown()
					return &RunResult{
						Output:        lastStructuredCandidate,
						History:       history,
						Cost:          cost,
						Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
						Turns:         turn + 1,
						Status:        "output_validation_exhausted",
						Providers:     providers,
						FallbackCalls: fc,
					}, nil
				}
				cost, providers, fc := finalizeBreakdown()
				return &RunResult{
					Output:        out,
					History:       history,
					Cost:          cost,
					Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
					Turns:         turn + 1,
					Status:        status,
					Providers:     providers,
					FallbackCalls: fc,
				}, nil
			}
		}

		// Execute tool calls (if any) and capture their results so the
		// validator snapshot can inspect both the calls and their outcomes.
		// BeforeToolExecution hooks can cancel / replace calls before the
		// tool function runs — cancelled ones produce synthetic error
		// results that already live in history when applyHooks returns.
		var toolResults []message.ToolResult
		if len(llmResp.ToolCalls) > 0 {
			approved, cancelled, hookErr := l.applyBeforeToolExecutionHooks(
				ctx, history, llmResp.ToolCalls, nil)
			if hookErr != nil {
				log.Printf("loop: BeforeToolExecution hook error: %v", hookErr)
			}
			toolResults = append(toolResults, cancelled...)
			executed := l.executeToolCallsInternal(ctx, history, approved)
			nameByID := make(map[string]string, len(approved))
			for _, c := range approved {
				nameByID[c.ID] = c.Name
			}
			for _, r := range executed {
				history.AddToolResult(r.ToolCallID, nameByID[r.ToolCallID], r.Content, r.IsError)
			}
			toolResults = append(toolResults, executed...)

			// Halt detection: if any tool result signals Halt, stop the run
			// cleanly after recording all results. All tool results are
			// already in history; the LLM is not called again.
			//
			// pickHaltFinalText lets a halting tool override Output with
			// the text it registered via tool.SetFinalResponse — needed
			// when the model emitted no streaming content and the
			// canonical close lives inside the tool call args.
			if anyHalted(toolResults) {
				cost, providers, fc := finalizeBreakdown()
				return &RunResult{
					Output:        pickHaltFinalText(lastAttemptedOutput, toolResults),
					History:       history,
					Cost:          cost,
					Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
					Turns:         turn + 1,
					Status:        "halted_by_tool",
					Providers:     providers,
					FallbackCalls: fc,
				}, nil
			}
		}

		// Trigger AfterCall hooks once per turn — fires for both tool-call
		// and final-text paths so consumers see a consistent lifecycle.
		l.hooks.Trigger(ctx, HookAfterCall, &CallParams{
			History:      history,
			Turn:         turn,
			MaxTurns:     l.maxTurns,
			SystemPrompt: sysPrompt,
		})

		// Run the turn validator (if any). On rejection we add the hint as
		// a system message and let the loop iterate again — without
		// returning the assistant's final text, so it doesn't bleed through
		// as the run output. SkipBudget rejections steer without consuming
		// the budget counter.
		proceed, abort, _ := l.validateTurn(ctx, TurnSnapshot{
			Turn:                 turn,
			LastAssistantContent: llmResp.Content,
			ToolCalls:            llmResp.ToolCalls,
			ToolResults:          toolResults,
			History:              history,
		}, &consecutiveValidatorFail)
		if abort {
			// Retries exhausted — surface the last attempted output and
			// stop so observers can still see what the model produced.
			cost, providers, fc := finalizeBreakdown()
			return &RunResult{
				Output:        lastAttemptedOutput,
				History:       history,
				Cost:          cost,
				Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
				Turns:         turn + 1,
				Status:        "validation_exhausted",
				Providers:     providers,
				FallbackCalls: fc,
			}, nil
		}

		// If this turn had no tool calls, it's the final response — return
		// only after the validator has had a chance to accept it (proceed
		// returned true, meaning validator accepted or no validator is set).
		// On tool-call turns proceed is advisory and we always continue.
		if len(llmResp.ToolCalls) == 0 {
			if !proceed {
				continue
			}
			cost, providers, fc := finalizeBreakdown()
			return &RunResult{
				Output:        llmResp.Content,
				History:       history,
				Cost:          cost,
				Usage:         Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
				Turns:         turn + 1,
				Status:        status,
				Providers:     providers,
				FallbackCalls: fc,
			}, nil
		}
	}

	status = "max_turns_exceeded"
	return nil, fmt.Errorf("max turns (%d) exceeded", l.maxTurns)
}

// executeToolCalls executes tool calls respecting parallel/sequential configuration.
// Tools marked Parallel=true run concurrently. Tools marked Parallel=false run
// sequentially in order, waiting for any parallel tools to complete first.
func (l *AgentLoop) executeToolCalls(ctx context.Context, history *message.History, calls []message.ToolCall) error {
	if len(calls) == 0 {
		return nil
	}

	// Build a map of tool name -> tool for fast lookup
	toolMap := make(map[string]*tool.Tool)
	for _, t := range l.tools {
		toolMap[t.Name()] = t
	}

	// Separate parallel and sequential tool calls
	type indexedCall struct {
		index int
		call  message.ToolCall
		tt    *tool.Tool
	}

	var parallelCalls []indexedCall
	var sequentialCalls []indexedCall

	for i, tc := range calls {
		tt, ok := toolMap[tc.Name]
		if !ok {
			// Unknown tool — report as error feedback to the LLM
			history.AddToolResult(tc.ID, tc.Name,
				fmt.Sprintf("Error: unknown tool %q. Available tools: %v", tc.Name, toolNames(toolMap)),
				true,
			)
			continue
		}
		if tt.Config().Parallel {
			parallelCalls = append(parallelCalls, indexedCall{i, tc, tt})
		} else {
			sequentialCalls = append(sequentialCalls, indexedCall{i, tc, tt})
		}
	}

	// Result buffer preserves original order
	results := make([]message.ToolResult, len(calls))

	// Execute parallel tools first
	if len(parallelCalls) > 0 {
		g, gctx := errgroup.WithContext(ctx)
		for _, pc := range parallelCalls {
			pc := pc
			g.Go(func() error {
				result := l.executeSingleTool(gctx, pc.tt, pc.call)
				results[pc.index] = result
				return nil // errgroup continues even on errors (feedback, not crash)
			})
		}
		_ = g.Wait() // errors are captured per-tool as feedback
	}

	// Execute sequential tools in order
	for _, sc := range sequentialCalls {
		result := l.executeSingleTool(ctx, sc.tt, sc.call)
		results[sc.index] = result
	}

	// Add all results to history in original order. Look up each call's name
	// by ID — Google's API requires function_response.name, and Anthropic /
	// OpenAI ignore it harmlessly.
	nameByID := make(map[string]string, len(calls))
	for _, c := range calls {
		nameByID[c.ID] = c.Name
	}
	for _, r := range results {
		history.AddToolResult(r.ToolCallID, nameByID[r.ToolCallID], r.Content, r.IsError)
	}

	return nil
}

// executeSingleTool runs a single tool and returns its result.
// Errors are converted to tool results with IsError=true so the LLM
// receives them as feedback for self-correction.
//
// Pause points are NOT consulted here. The streaming entry point
// (executeToolCallsStreaming) already gates each call through the
// PauseManager up-front and filters cancelled tools out of the slice
// it hands us. Checking again would deadlock — the TUI only emits one
// approval request per tool-call step.
func (l *AgentLoop) executeSingleTool(ctx context.Context, tt *tool.Tool, tc message.ToolCall) message.ToolResult {
	// Stamp the tool's call_id on ctx so any sub-agent spawned inside the
	// tool body can record this exact tool call as its origin. The panel
	// uses it to render the spawned run nested under the right tool node.
	ctx = ContextWithToolCallID(ctx, tc.ID)

	// Attach a halt signal so the tool body can call tool.SetHalt(ctx)
	// to request a clean termination of the run after this turn.
	ctx, isHalted := tool.WithHaltSignal(ctx)

	// Attach a final-response signal so the tool body can call
	// tool.SetFinalResponse(ctx, text) to register the canonical
	// user-facing wrap-up — surfaced on StepFinalResponse.Content when
	// the run halts. Needed for providers (Gemini thinking-mode) that
	// emit the answer inside a tool call's arguments instead of as
	// streaming chunks; without this, StepFinalResponse.Content would
	// carry the empty streamed string and consumers render blank.
	ctx, finalResponseText := tool.WithFinalResponse(ctx)

	// Execute the tool
	output, err := tt.Execute(ctx, tc.Arguments)
	if err != nil {
		return message.ToolResult{
			ToolCallID: tc.ID,
			Content:    toolErrorContent(tc.Name, err),
			IsError:    true,
		}
	}

	return message.ToolResult{
		ToolCallID:    tc.ID,
		Content:       output,
		IsError:       false,
		Halt:          isHalted(),
		FinalResponse: finalResponseText(),
	}
}

// toolErrorContent formats a tool execution error as feedback for the
// model. Schema-level validation failures get an explicit "fix and retry"
// instruction so the model self-corrects on the next turn instead of
// looping on the same malformed payload; non-validation errors fall back
// to the legacy phrasing because they don't necessarily mean the model
// should redrive the call (network, timeout, business rejection, etc.).
func toolErrorContent(name string, err error) string {
	var vErr *tool.ValidationError
	if errors.As(err, &vErr) {
		return fmt.Sprintf(
			"Tool %q call rejected: arguments did not match the input schema. "+
				"%s. Apply the required changes and call the tool again with "+
				"corrected arguments that satisfy the schema.",
			name, vErr.Message,
		)
	}
	return fmt.Sprintf("Tool %q error: %v", name, err)
}

// resolveSystemPrompt evaluates the system prompt function and appends
// structured-output instructions when needed. The wording differs by path:
//
//   - Native response_format (tool-less agents on OpenAI / Gemini): the
//     provider enforces the schema server-side, so we just nudge the model
//     toward returning plain JSON without any framing.
//   - Tool-injection path (Anthropic, or any agent that has tools): the
//     model must invoke the framework-injected final_response tool —
//     instruct it explicitly so it doesn't reply with prose.
func (l *AgentLoop) resolveSystemPrompt(ctx context.Context) string {
	if l.systemPrompt == nil {
		return ""
	}
	prompt := l.systemPrompt(ctx)

	if l.structuredOutput != nil {
		schemaStr := string(l.structuredOutput)
		if l.useNativeResponseFormat() {
			prompt += fmt.Sprintf(
				"\n\nReply with a single JSON object — no markdown, no prose, "+
					"no leading commentary — matching this JSON Schema:\n%s",
				schemaStr,
			)
		} else {
			prompt += fmt.Sprintf(
				"\n\nYou MUST use the final_response tool to return your answer. "+
					"Do not respond with plain text. Always call final_response "+
					"with a valid object matching this JSON Schema:\n%s",
				schemaStr,
			)
		}
	}

	return prompt
}

// resolveRunConfig merges run options with defaults.
func (l *AgentLoop) resolveRunConfig(opts []RunOption) *runConfig {
	cfg := &runConfig{
		maxTurns:   l.maxTurns,
		maxRetries: l.maxRetries,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// calculateCost converts accumulated token counts into a full USD breakdown
// via the configured cost model. If no model is set (legacy callers), only
// the token counts are reported.
//
// This is the legacy single-bucket path: it bills the entire token total
// against the loop's static (provider, model) and is only correct in the
// single-provider case. Multi-provider chains must use finalizeRun
// instead — it iterates the runStats accumulator and bills each
// (provider, model) entry at its own rate before summing.
func (l *AgentLoop) calculateCost(u provider.Usage, totalInput, totalOutput, totalCached int) CostBreakdown {
	tokens := CostBreakdown{
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		CachedTokens: totalCached,
	}
	if l.costModel == nil {
		// No matrix configured, but an API-reported cost is still valid.
		tokens.TotalUSD = u.Cost
		return tokens
	}
	providerName := providerLabel(l.provider)
	modelName := l.model
	if modelName == "" {
		if m, ok := l.provider.(interface{ Model() string }); ok {
			modelName = m.Model()
		}
	}
	br := l.costModel.Calculate(providerName, modelName, telemetry.Usage{
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		CachedTokens: totalCached,
		Cost:         u.Cost,
	})
	return CostBreakdown{
		TotalUSD:     br.TotalUSD,
		InputUSD:     br.InputUSD,
		OutputUSD:    br.OutputUSD,
		CachedUSD:    br.CachedUSD,
		SavingsUSD:   br.SavingsUSD,
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		CachedTokens: totalCached,
	}
}

// finalizeRun summarises the per-call accumulator into the public shape:
// the aggregated CostBreakdown across every (provider, model) entry, the
// per-entry ProviderStats slice in first-seen order, and the count of
// LLM calls that hit a FailoverProvider fallback target.
//
// fallbackProvider / fallbackModel are used by stats.snapshot when an
// entry doesn't carry provenance (legacy provider that doesn't set
// LLMResponse.ProviderID). Pass providerLabel(l.provider) and l.model so
// single-provider deployments still get a properly-labelled bucket.
func (l *AgentLoop) finalizeRun(stats *runStats) (CostBreakdown, []ProviderStats, int) {
	if stats == nil {
		return CostBreakdown{}, nil, 0
	}
	providers := stats.snapshot(func(p, m string, u provider.Usage) CostBreakdown {
		return providerCostFor(l.costModel, p, m, u)
	})
	var total CostBreakdown
	for _, ps := range providers {
		total.InputUSD += ps.Cost.InputUSD
		total.OutputUSD += ps.Cost.OutputUSD
		total.CachedUSD += ps.Cost.CachedUSD
		total.SavingsUSD += ps.Cost.SavingsUSD
		total.TotalUSD += ps.Cost.TotalUSD
		total.InputTokens += ps.Usage.InputTokens
		total.OutputTokens += ps.Usage.OutputTokens
		total.CachedTokens += ps.Usage.CachedTokens
	}
	return total, providers, stats.fallbackCount()
}

// providerLabel classifies the LLMProvider for cost-table lookups. Matches
// the keys used by telemetry.defaultCosts().
func providerLabel(p provider.LLMProvider) string {
	switch fmt.Sprintf("%T", p) {
	case "*openai.Provider":
		return "openai"
	case "*anthropic.Provider":
		return "anthropic"
	case "*google.Provider":
		return "google"
	}
	return ""
}

// toolNames returns a list of available tool names.
func toolNames(toolMap map[string]*tool.Tool) []string {
	names := make([]string, 0, len(toolMap))
	for name := range toolMap {
		names = append(names, name)
	}
	return names
}

// anyHalted reports whether any tool result in the slice has Halt=true.
// Used to detect when a tool body called tool.SetHalt(ctx), signalling
// that the run should stop cleanly without another LLM call.
func anyHalted(results []message.ToolResult) bool {
	for _, r := range results {
		if r.Halt {
			return true
		}
	}
	return false
}

// pickHaltFinalText picks the canonical final-response text for a turn
// that halted via a tool. Precedence:
//
//  1. The first tool result whose body called tool.SetFinalResponse
//     wins — for the canonical "tool whose argument IS the wrap-up"
//     pattern (e.g. a final_response tool on Gemini thinking-mode,
//     which emits zero streaming chunks and puts the entire visible
//     answer inside the tool call args).
//  2. Otherwise the streamed assistant text — the legacy behaviour
//     when no tool opted in.
//
// Keeping precedence explicit (first non-empty wins, same as Halt
// semantics) so multiple halting tools in one turn behave predictably.
func pickHaltFinalText(streamed string, results []message.ToolResult) string {
	for _, r := range results {
		if r.Halt && r.FinalResponse != "" {
			return r.FinalResponse
		}
	}
	return streamed
}

// HookManager returns the loop's hook manager.
func (l *AgentLoop) HookManager() *HookManager { return l.hooks }

// buildToolList returns the tool list for the next LLM call. When a
// DynamicToolsFunc is registered it is consulted with the current history
// — its return value replaces the static tools. The structured-output
// final_response tool is appended unless we use native response_format (see
// useNativeResponseFormat) — i.e. it is appended whenever the agent has tools
// of its own, so structured output never has to be bound via response_format
// alongside a tool list.
//
// A nil ctx or history is tolerated to keep callers that don't yet thread
// them through working (the dynamic function should also be defensive).
func (l *AgentLoop) buildToolList(ctx context.Context, history *message.History) []*tool.Tool {
	var base []*tool.Tool
	if l.dynamicTools != nil {
		base = l.dynamicTools(ctx, history)
	}
	if base == nil {
		base = l.tools
	}

	tools := make([]*tool.Tool, len(base))
	copy(tools, base)

	if l.structuredOutput != nil && !l.useNativeResponseFormat() {
		tools = append(tools, l.createFinalResponseTool())
	}

	return tools
}

// useNativeResponseFormat reports whether structured output should be
// enforced via the provider's native response_format (true) or via the
// injected final_response tool (false).
//
// Native response_format constrains EVERY completion to the output schema.
// That is correct for a pure extraction agent (no tools of its own), but on
// a tool-using agent it prevents the model from ever emitting a tool call:
// strict OpenAI-compatible servers (vLLM/SGLang and the like) honour
// response_format rigidly and fabricate a schema-shaped answer instead of
// calling the tool. OpenAI proper happens to let tool calls take precedence,
// but relying on that is a portability trap. So native is used ONLY when the
// agent exposes no tools; otherwise structured output rides the
// final_response tool, which composes cleanly with ordinary tool calls (the
// approach frameworks like pydantic-ai take).
func (l *AgentLoop) useNativeResponseFormat() bool {
	if l.structuredOutput == nil || !provider.SupportsNativeResponseFormat(l.provider) {
		return false
	}
	return len(l.tools) == 0 && l.dynamicTools == nil
}

// finalResponseInput is the internal schema for the final_response tool.
type finalResponseInput struct {
	Output json.RawMessage `json:"output" jsonschema:"description=The final response object"`
}

// extractFinalResponseOutput scans a tool-call slice for the implicit
// final_response call and returns its `output` argument as a JSON string.
// Used by the loop to short-circuit structured-output turns: the LLM
// "calling" final_response is the framework's signal that the model has
// produced its typed answer, so we return that JSON directly rather than
// running the tool and feeding the pass-through back to the model.
func extractFinalResponseOutput(calls []message.ToolCall) (string, bool) {
	for _, tc := range calls {
		if tc.Name != "final_response" {
			continue
		}
		var args struct {
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(tc.Arguments, &args); err == nil && len(args.Output) > 0 {
			return string(args.Output), true
		}
	}
	return "", false
}

// createFinalResponseTool builds the final_response tool. The input schema
// is fixed and known to compile, so MustNewTool is appropriate here — a
// failure would mean the framework itself is broken, not the user's code.
func (l *AgentLoop) createFinalResponseTool() *tool.Tool {
	return tool.MustNewTool(finalResponseInput{},
		func(ctx context.Context, input finalResponseInput) (string, error) {
			return string(input.Output), nil
		},
		tool.ToolConfig{
			Name:        "final_response",
			Description: "Call this tool with your final answer. You MUST use this tool instead of plain text.",
			Parallel:    false,
		},
	)
}

// contextKey is a typed key for metadata injection into context.
type contextKey string

// Iterate returns an Iterator for manual step-by-step control.
func (l *AgentLoop) Iterate(ctx context.Context, input string, opts ...RunOption) *Iterator {
	it := &Iterator{
		steps: make(chan Step, 64),
		done:  make(chan struct{}),
		loop:  l,
		input: input,
		opts:  opts,
		stats: newRunStats(),
	}

	go it.run(ctx)
	return it
}

// Iterator provides manual control over the agentic loop via a channel.
// Each step in the loop is emitted as a Step on the channel.
// The channel closes when the loop completes or an error occurs.
type Iterator struct {
	steps chan Step
	done  chan struct{}
	once  sync.Once
	loop  *AgentLoop
	input string
	opts  []RunOption

	// Result fields populated by run(). Safe to read after the steps channel
	// is closed (channel close synchronizes the writer goroutine).
	resMu        sync.RWMutex
	output       string
	status       string
	turns        int
	inputTokens  int
	outputTokens int
	cachedTokens int
	apiCost      float64
	history      *message.History

	// stats is the per-(provider, model) accumulator. Populated alongside
	// inputTokens / outputTokens / cachedTokens on every LLM response or
	// final stream chunk. Result() reads it to fill RunResult.Providers
	// and RunResult.FallbackCalls. Goroutine-safe via runStats.mu.
	stats *runStats

	// proxy points to a wrapped underlying iterator when this one is a
	// transparent middleware (see WrapIterator). Result() delegates to it.
	proxy *Iterator
}

// Next returns a channel that emits steps as the loop progresses.
func (it *Iterator) Next() <-chan Step {
	return it.steps
}

// Close stops the iterator.
func (it *Iterator) Close() {
	it.once.Do(func() {
		close(it.done)
	})
}

// WrapIterator returns a new Iterator that proxies the inner one. Every step
// is fanned out to `onStep` before being forwarded to the caller. Once the
// inner channel closes, `onDone` runs, then the outer channel closes too.
//
// The wrapper's Result() delegates to the inner iterator, so callers see the
// same final breakdown regardless of which level they query.
func WrapIterator(inner *Iterator, onStep func(Step), onDone func()) *Iterator {
	if inner == nil {
		return nil
	}
	out := &Iterator{
		steps: make(chan Step, cap(inner.steps)),
		done:  make(chan struct{}),
		loop:  inner.loop,
		input: inner.input,
		opts:  inner.opts,
		// Share the inner's result state so out.Result() == inner.Result().
		// Pointer share is fine: writers and readers don't race because the
		// inner finishes before we close out.
		proxy: inner,
	}
	go func() {
		defer close(out.steps)
		for s := range inner.Next() {
			if onStep != nil {
				onStep(s)
			}
			select {
			case out.steps <- s:
			case <-out.done:
				return
			}
		}
		if onDone != nil {
			onDone()
		}
	}()
	return out
}

// recordUsage adds a single LLM response's usage to the running totals.
// Kept for backward compatibility; new code paths use recordResponse /
// recordChunk so the per-provider stats accumulator stays in sync.
func (it *Iterator) recordUsage(u provider.Usage) {
	it.resMu.Lock()
	defer it.resMu.Unlock()
	it.inputTokens += u.InputTokens
	it.outputTokens += u.OutputTokens
	it.cachedTokens += u.CachedTokens
	it.apiCost += u.Cost
}

// recordResponse adds a non-streaming LLM response to both the legacy
// totals and the per-(provider, model) accumulator. Empty ProviderID /
// ModelID fall back to the loop's static labels so single-provider
// deployments still see a properly-labelled bucket.
func (it *Iterator) recordResponse(resp *provider.LLMResponse) {
	if resp == nil {
		return
	}
	it.recordUsage(resp.Usage)
	if it.stats != nil {
		it.stats.add(resp, providerLabel(it.loop.provider), it.loop.model)
	}
}

// recordChunk adds the FINAL chunk of a streaming response to both the
// legacy totals and the per-(provider, model) accumulator. Non-final
// chunks (or chunks without Usage) are ignored — only the wire-final
// chunk carries the usage and provenance signals.
func (it *Iterator) recordChunk(c provider.StreamChunk) {
	if !c.IsFinal || c.Usage == nil {
		return
	}
	it.recordUsage(*c.Usage)
	if it.stats != nil {
		it.stats.addChunk(c, providerLabel(it.loop.provider), it.loop.model)
	}
}

// recordFinal stores the final output and turn count.
func (it *Iterator) recordFinal(output string, turn int, status string) {
	it.resMu.Lock()
	defer it.resMu.Unlock()
	it.output = output
	it.turns = turn + 1
	it.status = status
}

// recordError marks the run as errored at the given turn.
func (it *Iterator) recordError(turn int) {
	it.resMu.Lock()
	defer it.resMu.Unlock()
	it.turns = turn + 1
	it.status = "error"
}

// recordCancelled marks the run as cancelled at the given turn. Distinct from
// "error" so observers can tell the difference between a real failure and a
// user-driven abort.
func (it *Iterator) recordCancelled(turn int) {
	it.resMu.Lock()
	defer it.resMu.Unlock()
	it.turns = turn + 1
	it.status = "cancelled"
}

// fireCancel dispatches the OnCancel hook. Best-effort: any error from the
// hook is logged via the steps channel as a StepError but doesn't block exit.
func (it *Iterator) fireCancel(ctx context.Context, history *message.History, turn int, sysPrompt string) {
	it.recordCancelled(turn)
	if it.loop.hooks == nil || !it.loop.hooks.HasHooks(HookOnCancel) {
		return
	}
	_ = it.loop.hooks.Trigger(ctx, HookOnCancel, &CallParams{
		History:      history,
		Turn:         turn,
		MaxTurns:     it.loop.maxTurns,
		SystemPrompt: sysPrompt,
	})
}

// Result returns the final outcome of the iteration. Call only after the
// channel returned by Next() has been drained (closed). USD figures are
// resolved from the loop's cost model when present; otherwise the breakdown
// carries only the token counts.
//
// Wrapper iterators (created by WrapIterator) transparently delegate to the
// inner iterator so callers always see the real, fully-populated result.
func (it *Iterator) Result() RunResult {
	if it.proxy != nil {
		return it.proxy.Result()
	}
	it.resMu.RLock()
	defer it.resMu.RUnlock()
	status := it.status
	if status == "" {
		status = "completed"
	}
	// Multi-provider runs need per-(provider, model) cost attribution.
	// finalizeRun delegates to runStats which iterates each bucket; the
	// legacy single-bucket calculateCost path stays only as a fallback
	// when no stats accumulator was wired (older callers / tests).
	var cost CostBreakdown
	var providers []ProviderStats
	var fallbackCalls int
	if it.stats != nil {
		cost, providers, fallbackCalls = it.loop.finalizeRun(it.stats)
	} else {
		cost = it.loop.calculateCost(provider.Usage{Cost: it.apiCost}, it.inputTokens, it.outputTokens, it.cachedTokens)
	}
	return RunResult{
		Output:  it.output,
		History: it.history,
		Cost:    cost,
		Usage: Usage{
			InputTokens:  it.inputTokens,
			OutputTokens: it.outputTokens,
			CachedTokens: it.cachedTokens,
		},
		Turns:         it.turns,
		Status:        status,
		Providers:     providers,
		FallbackCalls: fallbackCalls,
	}
}

// run executes the loop and emits steps.
func (it *Iterator) run(ctx context.Context) {
	defer close(it.steps)

	cfg := it.loop.resolveRunConfig(it.opts)
	history := cfg.history
	if history == nil {
		history = message.NewHistory()
	}
	// Same rule as AgentLoop.Run: callers can deliver the user turn via
	// WithHistory (e.g. multi-modal Parts) and pass an empty input. Don't
	// inject a phantom empty user message in that case.
	if it.input != "" {
		history.AddUserMessage(it.input)
	}
	it.resMu.Lock()
	it.history = history
	it.resMu.Unlock()

	sysPrompt := it.loop.resolveSystemPrompt(ctx)

	it.steps <- Step{
		Type:    StepSystemPrompt,
		Content: sysPrompt,
	}

	// validatorFails counts consecutive rejections from the TurnValidator
	// across turns in this run. It is reset on every accepting turn so a
	// validator can recover after a streak of rejections.
	validatorFails := 0

	// outputRetriesUsed tracks how many structured-output candidates have
	// been rejected so far. Independent of validatorFails — they cover
	// different stages of the loop.
	outputRetriesUsed := 0

	for turn := 0; turn < it.loop.maxTurns; turn++ {
		select {
		case <-it.done:
			it.fireCancel(ctx, history, turn, sysPrompt)
			return
		case <-ctx.Done():
			it.fireCancel(ctx, history, turn, sysPrompt)
			it.steps <- Step{Type: StepError, Error: ctx.Err()}
			return
		default:
		}

		// BeforeCall hooks
		if err := it.loop.hooks.Trigger(ctx, HookBeforeCall, &CallParams{
			History:      history,
			Turn:         turn,
			MaxTurns:     it.loop.maxTurns,
			SystemPrompt: sysPrompt,
		}); err != nil {
			it.steps <- Step{Type: StepError, Error: err, Turn: turn}
			return
		}

		// Memory
		if it.loop.memoryMgr != nil {
			it.loop.memoryMgr.Manage(ctx, history)
		}

		// LLM Call
		it.steps <- Step{Type: StepLLMCall, Turn: turn}

		req := provider.LLMRequest{
			SystemPrompt: sysPrompt,
			Messages:     history.Messages(),
			Tools:        it.loop.buildToolList(ctx, history),
			Temperature:  it.loop.temperature,
			Reasoning:    it.loop.reasoning,
			ToolChoice:   it.loop.toolChoice,
		}
		if it.loop.useNativeResponseFormat() {
			req.ResponseSchema = it.loop.structuredOutput
		}
		if it.loop.model != "" {
			req.Model = it.loop.model
		}

		// Try streaming first
		if stream, err := it.loop.provider.ChatStream(ctx, req); err == nil {
			var fullContent string
			for chunk := range stream {
				if chunk.Error != nil {
					it.steps <- Step{Type: StepError, Error: chunk.Error, Turn: turn}
					return
				}
				// Provider contract: deltas while streaming, then a final chunk
				// whose Content carries the cumulative text. Only accumulate
				// deltas — re-adding the final's Content would double it.
				if !chunk.IsFinal && chunk.Content != "" {
					fullContent += chunk.Content
					it.steps <- Step{
						Type:         StepStreamingChunk,
						Content:      chunk.Content,
						Turn:         turn,
						ProviderID:   chunk.ProviderID,
						ModelID:      chunk.ModelID,
						Fallback:     chunk.Fallback,
						APIKeySuffix: chunk.APIKeySuffix,
					}
				}
				// Reasoning deltas travel on a separate channel so the
				// UI can render them differently (collapsed / faint /
				// behind a toggle). They are NOT folded into fullContent.
				if !chunk.IsFinal && chunk.Reasoning != "" {
					it.steps <- Step{
						Type:         StepReasoningChunk,
						Content:      chunk.Reasoning,
						Turn:         turn,
						ProviderID:   chunk.ProviderID,
						ModelID:      chunk.ModelID,
						Fallback:     chunk.Fallback,
						APIKeySuffix: chunk.APIKeySuffix,
					}
				}
				if chunk.IsFinal {
					it.recordChunk(chunk)
					// Resolve the assistant text BEFORE emitting
					// StepLLMResponse so we can carry it on the step.
					// Persistence layers (tracer, JSON store) strip
					// individual StepStreamingChunk events as noise; the
					// turn's full text survives via this single event.
					final := chunk.Content
					if final == "" {
						final = fullContent
					}
					// Per-turn provenance signal for trace consumers.
					// Emitted before the per-tool / per-final steps so
					// the web UI can stamp the turn's (provider, model,
					// fallback, key) before rendering any nested children.
					it.steps <- Step{
						Type:         StepLLMResponse,
						Turn:         turn,
						Content:      final,
						Usage:        chunk.Usage,
						ProviderID:   chunk.ProviderID,
						ModelID:      chunk.ModelID,
						Fallback:     chunk.Fallback,
						APIKeySuffix: chunk.APIKeySuffix,
					}
					if it.tripUsageLimitIfExceeded(final, turn, chunk.Usage) {
						return
					}
					if len(chunk.ToolCalls) > 0 {
						history.AddAssistantMessage(final, chunk.ToolCalls)
						for _, tc := range chunk.ToolCalls {
							argsJSON, _ := json.Marshal(tc.Arguments)
							it.steps <- Step{
								Type:       StepToolCall,
								ToolName:   tc.Name,
								ToolArgs:   string(argsJSON),
								ToolCallID: tc.ID,
								Turn:       turn,
								Usage:      chunk.Usage,
							}
						}
						// Structured-output short-circuit: final_response
						// is the framework-injected tool whose `output`
						// argument is THE run's answer. Emit it as a
						// StepFinalResponse and stop the loop instead of
						// executing the tool and looping again.
						if it.loop.structuredOutput != nil {
							if out, ok := extractFinalResponseOutput(chunk.ToolCalls); ok {
								// Gate the close through the turn validator first:
								// a premature final_response is refused in-band and
								// re-prompted rather than committed as the answer.
								proceed, abort, vout := it.loop.gateStructuredClose(ctx, chunk.ToolCalls, final, history, turn, &validatorFails)
								if abort {
									it.recordFinal(out, turn, "validation_exhausted")
									it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", vout.Reason), Turn: turn}
									return
								}
								if !proceed {
									// Rejection result already in history; break the
									// inner chunk loop so the outer for re-prompts.
									break
								}
								okOut, abortOut := it.validateStructuredOrAbort(out, history, &outputRetriesUsed)
								if abortOut {
									it.recordFinal(out, turn, "output_validation_exhausted")
									it.steps <- Step{Type: StepFinalResponse, Content: out, Turn: turn, Usage: chunk.Usage}
									return
								}
								if !okOut {
									// Hint already in history; break inner chunk
									// loop so the outer for can re-prompt.
									break
								}
								it.recordFinal(out, turn, "completed")
								it.steps <- Step{Type: StepFinalResponse, Content: out, Turn: turn, Usage: chunk.Usage}
								return
							}
						}
						// Execute tools and validate the turn so the model gets
						// corrective feedback when it called the wrong tools.
						results := it.loop.executeToolCallsStreaming(ctx, history, chunk.ToolCalls, it.steps, turn)
						// Halt detection: if any tool called tool.SetHalt(ctx),
						// stop the run cleanly. All results are already in
						// history; no further LLM call is issued.
						//
						// pickHaltFinalText lets a halting tool override
						// the streamed content via tool.SetFinalResponse —
						// needed for providers that emit zero streaming
						// chunks (Gemini thinking-mode) and put the visible
						// answer inside the tool call args.
						if anyHalted(results) {
							finalText := pickHaltFinalText(final, results)
							it.recordFinal(finalText, turn, "halted_by_tool")
							it.steps <- Step{Type: StepFinalResponse, Content: finalText, Turn: turn, Usage: chunk.Usage}
							return
						}
						_, abort, out := it.loop.validateTurn(ctx, TurnSnapshot{
							Turn:                 turn,
							LastAssistantContent: final,
							ToolCalls:            chunk.ToolCalls,
							ToolResults:          results,
							History:              history,
						}, &validatorFails)
						if abort {
							it.recordFinal(final, turn, "validation_exhausted")
							it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", out.Reason), Turn: turn}
							return
						}
					} else {
						// Final-text candidate — let the validator inspect it
						// before we commit. On rejection with budget remaining
						// we skip recordFinal and let the outer loop iterate.
						proceed, abort, out := it.loop.validateTurn(ctx, TurnSnapshot{
							Turn:                 turn,
							LastAssistantContent: final,
							History:              history,
						}, &validatorFails)
						if abort {
							it.recordFinal(final, turn, "validation_exhausted")
							it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", out.Reason), Turn: turn}
							return
						}
						if !proceed {
							// Hint already added; next turn re-prompts.
							break
						}
						// Native structured-output path: `final` IS the JSON
						// when the provider answered via response_format.
						// Run the structured-output gate too so schema-
						// incompatible payloads trigger the same retry loop.
						if it.loop.structuredOutput != nil {
							okOut, abortOut := it.validateStructuredOrAbort(final, history, &outputRetriesUsed)
							if abortOut {
								it.recordFinal(final, turn, "output_validation_exhausted")
								it.steps <- Step{Type: StepFinalResponse, Content: final, Turn: turn, Usage: chunk.Usage}
								return
							}
							if !okOut {
								break
							}
						}
						it.recordFinal(final, turn, "completed")
						it.steps <- Step{Type: StepFinalResponse, Content: final, Turn: turn, Usage: chunk.Usage}
						return
					}
					break
				}
			}
		} else {
			// Fallback to non-streaming
			llmResp, err := it.loop.provider.Chat(ctx, req)
			if err != nil {
				it.recordError(turn)
				it.steps <- Step{Type: StepError, Error: err, Turn: turn}
				return
			}

			it.recordResponse(llmResp)
			usagePtr := llmResp.Usage
			// Per-turn provenance for the non-streaming fallback path.
			// Same shape as the streaming branch's emission so trace
			// consumers see a consistent StepLLMResponse for every turn.
			it.steps <- Step{
				Type:       StepLLMResponse,
				Turn:       turn,
				Usage:      &usagePtr,
				ProviderID: llmResp.ProviderID,
				ModelID:    llmResp.ModelID,
				Fallback:   llmResp.Fallback,
			}

			if it.tripUsageLimitIfExceeded(llmResp.Content, turn, &usagePtr) {
				return
			}

			if llmResp.Content != "" {
				it.steps <- Step{Type: StepStreamingChunk, Content: llmResp.Content, Turn: turn}
			}

			history.AddAssistantMessage(llmResp.Content, llmResp.ToolCalls)

			if len(llmResp.ToolCalls) > 0 {
				for _, tc := range llmResp.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Arguments)
					it.steps <- Step{
						Type:       StepToolCall,
						ToolName:   tc.Name,
						ToolArgs:   string(argsJSON),
						ToolCallID: tc.ID,
						Turn:       turn,
						Usage:      &usagePtr,
					}
				}
				// Structured-output short-circuit (non-streaming fallback path).
				if it.loop.structuredOutput != nil {
					if out, ok := extractFinalResponseOutput(llmResp.ToolCalls); ok {
						// Gate the close through the turn validator first (see
						// the streaming path); refuse a premature final_response
						// in-band and re-prompt instead of committing it.
						proceed, abort, vout := it.loop.gateStructuredClose(ctx, llmResp.ToolCalls, llmResp.Content, history, turn, &validatorFails)
						if abort {
							it.recordFinal(out, turn, "validation_exhausted")
							it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", vout.Reason), Turn: turn}
							return
						}
						if !proceed {
							// Rejection result added; outer loop re-prompts.
							continue
						}
						okOut, abortOut := it.validateStructuredOrAbort(out, history, &outputRetriesUsed)
						if abortOut {
							it.recordFinal(out, turn, "output_validation_exhausted")
							it.steps <- Step{Type: StepFinalResponse, Content: out, Turn: turn, Usage: &usagePtr}
							return
						}
						if !okOut {
							// Hint added; outer loop iterates for re-prompt.
							continue
						}
						it.recordFinal(out, turn, "completed")
						it.steps <- Step{Type: StepFinalResponse, Content: out, Turn: turn, Usage: &usagePtr}
						return
					}
				}
				results := it.loop.executeToolCallsStreaming(ctx, history, llmResp.ToolCalls, it.steps, turn)
				// Halt detection (non-streaming fallback path).
				if anyHalted(results) {
					finalText := pickHaltFinalText(llmResp.Content, results)
					it.recordFinal(finalText, turn, "halted_by_tool")
					it.steps <- Step{Type: StepFinalResponse, Content: finalText, Turn: turn, Usage: &usagePtr}
					return
				}
				_, abort, out := it.loop.validateTurn(ctx, TurnSnapshot{
					Turn:                 turn,
					LastAssistantContent: llmResp.Content,
					ToolCalls:            llmResp.ToolCalls,
					ToolResults:          results,
					History:              history,
				}, &validatorFails)
				if abort {
					it.recordFinal(llmResp.Content, turn, "validation_exhausted")
					it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", out.Reason), Turn: turn}
					return
				}
			} else {
				proceed, abort, out := it.loop.validateTurn(ctx, TurnSnapshot{
					Turn:                 turn,
					LastAssistantContent: llmResp.Content,
					History:              history,
				}, &validatorFails)
				if abort {
					it.recordFinal(llmResp.Content, turn, "validation_exhausted")
					it.steps <- Step{Type: StepError, Error: fmt.Errorf("validation_exhausted: %s", out.Reason), Turn: turn}
					return
				}
				if !proceed {
					// Hint added; outer loop iterates without committing the
					// final response.
					continue
				}
				// Native structured-output path on the non-streaming fallback.
				if it.loop.structuredOutput != nil {
					okOut, abortOut := it.validateStructuredOrAbort(llmResp.Content, history, &outputRetriesUsed)
					if abortOut {
						it.recordFinal(llmResp.Content, turn, "output_validation_exhausted")
						it.steps <- Step{Type: StepFinalResponse, Content: llmResp.Content, Turn: turn, Usage: &usagePtr}
						return
					}
					if !okOut {
						continue
					}
				}
				it.recordFinal(llmResp.Content, turn, "completed")
				it.steps <- Step{Type: StepFinalResponse, Content: llmResp.Content, Turn: turn, Usage: &usagePtr}
				return
			}
		}

		// AfterCall hooks
		it.loop.hooks.Trigger(ctx, HookAfterCall, &CallParams{
			History:      history,
			Turn:         turn,
			MaxTurns:     it.loop.maxTurns,
			SystemPrompt: sysPrompt,
		})
	}
}

// executeToolCallsStreaming executes tools and emits steps. Before running
// each call, the loop consults its pause manager: if a pause point is
// configured for that tool, the loop blocks until an external actor calls
// Resume (or the timeout elapses). A "cancel" response short-circuits the
// tool with an error result so the LLM sees it but the process continues.
//
// Each Step on the channel carries the tool NAME (not the call_id) so SSE
// consumers can render "tool=add_tables" without a parallel lookup table.
// IsError and Halt are propagated from message.ToolResult so consumers know
// the outcome without parsing Content.
//
// Returns the collected tool results so the caller can hand them to a
// TurnValidator without re-reading history.
func (l *AgentLoop) executeToolCallsStreaming(ctx context.Context, history *message.History, calls []message.ToolCall, steps chan<- Step, turn int) []message.ToolResult {
	nameByID := make(map[string]string, len(calls))
	for _, c := range calls {
		nameByID[c.ID] = c.Name
	}

	// Apply pause points up-front. Each blocked tool may be replaced by a
	// synthetic "cancelled" result so executeToolCallsInternal can skip it.
	approved := calls
	if l.pauseMgr != nil {
		approved = approved[:0]
		for _, c := range calls {
			cfg, ok := l.pauseMgr.HasPausePoint(c.Name)
			if !ok {
				approved = append(approved, c)
				continue
			}
			resp, err := l.pauseMgr.Pause(ctx, pause.PauseRequest{
				RequestID: c.ID, // unique per tool call → no cross-talk between concurrent runs
				Type:      cfg.Type,
				ToolName:  c.Name,
				Message:   fmt.Sprintf("Approve %s? args=%s", c.Name, c.Arguments),
				Timeout:   cfg.Timeout,
			})
			if err != nil || resp == nil || resp.Action == "cancel" {
				history.AddToolResult(c.ID, c.Name,
					fmt.Sprintf("Cancelled by pause point %q", c.Name), true)
				steps <- Step{
					Type:       StepToolResult,
					Content:    "(cancelled by pause point)",
					ToolName:   c.Name,
					ToolCallID: c.ID,
					IsError:    true,
					Turn:       turn,
				}
				continue
			}
			approved = append(approved, c)
		}
	}

	// BeforeToolExecution hooks may Cancel or Replace any approved call.
	// Cancellations are converted into synthetic error tool_results in
	// history by the helper; we mirror them on the steps channel so the
	// UI sees the cancellation as a regular tool_result step.
	approved, hookCancelled, hookErr := l.applyBeforeToolExecutionHooks(ctx, history, approved, nameByID)
	if hookErr != nil {
		steps <- Step{Type: StepError, Error: fmt.Errorf("BeforeToolExecution hook: %w", hookErr), Turn: turn}
	}
	for _, r := range hookCancelled {
		steps <- Step{
			Type:       StepToolResult,
			Content:    r.Content,
			ToolName:   nameByID[r.ToolCallID],
			ToolCallID: r.ToolCallID,
			IsError:    r.IsError,
			Turn:       turn,
		}
	}

	executed := l.executeToolCallsInternal(ctx, history, approved)
	for _, r := range executed {
		steps <- Step{
			Type:       StepToolResult,
			Content:    r.Content,
			ToolName:   nameByID[r.ToolCallID],
			ToolCallID: r.ToolCallID,
			IsError:    r.IsError,
			Halt:       r.Halt,
			Turn:       turn,
		}
		history.AddToolResult(r.ToolCallID, nameByID[r.ToolCallID], r.Content, r.IsError)
	}
	// Return both hook-cancelled and freshly executed results so the
	// validator snapshot sees every call's outcome.
	out := make([]message.ToolResult, 0, len(hookCancelled)+len(executed))
	out = append(out, hookCancelled...)
	out = append(out, executed...)
	return out
}

// executeToolCallsInternal mirrors executeToolCalls but returns raw results.
func (l *AgentLoop) executeToolCallsInternal(ctx context.Context, history *message.History, calls []message.ToolCall) []message.ToolResult {
	toolMap := make(map[string]*tool.Tool)
	for _, t := range l.tools {
		toolMap[t.Name()] = t
	}

	type indexedCall struct {
		index int
		call  message.ToolCall
		tt    *tool.Tool
	}

	var parallelCalls []indexedCall
	var sequentialCalls []indexedCall

	results := make([]message.ToolResult, len(calls))

	for i, tc := range calls {
		tt, ok := toolMap[tc.Name]
		if !ok {
			results[i] = message.ToolResult{
				ToolCallID: tc.ID,
				Content:    fmt.Sprintf("Error: unknown tool %q", tc.Name),
				IsError:    true,
			}
			continue
		}
		if tt.Config().Parallel {
			parallelCalls = append(parallelCalls, indexedCall{i, tc, tt})
		} else {
			sequentialCalls = append(sequentialCalls, indexedCall{i, tc, tt})
		}
	}

	// Execute parallel
	if len(parallelCalls) > 0 {
		g, gctx := errgroup.WithContext(ctx)
		for _, pc := range parallelCalls {
			pc := pc
			g.Go(func() error {
				results[pc.index] = l.executeSingleTool(gctx, pc.tt, pc.call)
				return nil
			})
		}
		_ = g.Wait()
	}

	// Execute sequential
	for _, sc := range sequentialCalls {
		results[sc.index] = l.executeSingleTool(ctx, sc.tt, sc.call)
	}

	return results
}

// RunOption configures a single run of the agentic loop.
type RunOption func(*runConfig)

type runConfig struct {
	history    *message.History
	maxTurns   int
	maxRetries int
	metadata   map[string]any
}

// WithHistory injects an existing history into the run.
func WithHistory(h *message.History) RunOption {
	return func(rc *runConfig) {
		rc.history = h
	}
}

// WithRunMaxTurns overrides maxTurns for this run.
func WithRunMaxTurns(n int) RunOption {
	return func(rc *runConfig) {
		rc.maxTurns = n
	}
}

// WithRunMetadata injects metadata into the run context.
func WithRunMetadata(m map[string]any) RunOption {
	return func(rc *runConfig) {
		rc.metadata = m
	}
}

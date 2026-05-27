// Package looper is the main entry point for the Looper Agent framework.
//
// Looper Agent is a minimalist, extensible LLM agent framework for Go.
// It follows a functional-first approach: tools, hooks, and providers
// are defined as functions with configuration structs. Interfaces are
// exposed as escape hatches for advanced use cases.
//
// Basic usage:
//
//	provider := openai.NewProvider(os.Getenv("OPENAI_API_KEY"))
//	agent := looper.NewAgent(provider, "You are a helpful assistant",
//	    looper.NewTool(SearchInput{}, searchFn, looper.ToolConfig{
//	        Name: "web_search", Description: "Search the web",
//	    }),
//	)
//	result, err := agent.Run(ctx, "What is Go?")
//	fmt.Println(result.Output, result.Cost.TotalUSD)
package looper

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/skill"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
	"github.com/cuatroochenta-idi/looper-agent/tool"
	"github.com/cuatroochenta-idi/looper-agent/toolkit"

	"github.com/google/uuid"
)

// Agent is the main orchestrator for LLM agent execution. It combines
// a provider, system prompt, tools, skills, and configuration into a
// runnable agent with observability, cost tracking, and hook support.
type Agent struct {
	provider            provider.LLMProvider
	systemPrompt        func(context.Context) string
	tools               []*tool.Tool
	skills              []skill.Skill
	loops               *loop.AgentLoop
	hooks               *loop.HookManager
	memoryMgr           memory.MemoryManager
	telemetry           *telemetry.CostTracker
	pauseMgr            *pause.PauseManager
	costModel           *telemetry.CostModel
	maxTurns            int
	maxRetries          int
	model               string
	temperature         float64
	reasoning           *provider.ReasoningConfig
	validator              loop.TurnValidator
	validatorMaxRetries    int
	dynamicTools           loop.DynamicToolsFunc
	toolChoice             provider.ToolChoice
	structuredOutputSchema json.RawMessage
	usageLimits            loop.UsageLimits
	outputMaxRetries       int
	outputCustomValidator  func(raw []byte) error
}

// NewAgent creates a new agent with the given provider, system prompt, and
// optional components.
//
// The systemPrompt can be a string (static) or a func(ctx context.Context) string
// (dynamic). Components can be:
//
//   - *tool.Tool       — a single registered tool
//   - skill.Skill      — group of tools + prompt fragment
//   - toolkit.Toolkit  — group of tools with shared state, no prompt fragment
//   - AgentOption      — typed configuration (WithAgentMemory, WithAgentMaxTurns…)
//
// AgentOptions are applied AFTER the agent is built, so they can rebuild the
// internal loop with the configured memory manager, pause manager, cost
// pricing overrides, model name, and so on.
//
// Returns an error if any argument has an unsupported type. Use MustNewAgent
// when building agents declaratively in tests or examples.
func NewAgent(p provider.LLMProvider, systemPrompt any, components ...any) (*Agent, error) {
	var spFn func(context.Context) string
	switch sp := systemPrompt.(type) {
	case string:
		s := sp
		spFn = func(_ context.Context) string { return s }
	case func(context.Context) string:
		spFn = sp
	default:
		return nil, fmt.Errorf("looper: systemPrompt must be string or func(context.Context) string, got %T", systemPrompt)
	}

	reg := tool.NewToolRegistry()
	var (
		skills []skill.Skill
		opts   []AgentOption
	)

	for _, comp := range components {
		switch c := comp.(type) {
		case *tool.Tool:
			reg.Add(c)
		case skill.Skill:
			skills = append(skills, c)
			c.RegisterTools(reg)
		case toolkit.Toolkit:
			c.RegisterTools(reg)
		case AgentOption:
			opts = append(opts, c)
		default:
			return nil, fmt.Errorf("looper: unsupported component type %T", comp)
		}
	}

	tools := reg.Tools()

	a := &Agent{
		provider:     p,
		systemPrompt: spFn,
		tools:        tools,
		skills:       skills,
		hooks:        loop.NewHookManager(),
		costModel:    telemetry.NewCostModel(),
		maxTurns:     10,
		maxRetries:   3,
		temperature:  0.7,
	}

	// Apply user-supplied options (memory, pause, model, temperature…) so they
	// flow into the loop we're about to build.
	for _, opt := range opts {
		opt(a)
	}

	// Build the system prompt — skill fragments are concatenated lazily so a
	// dynamic prompt func() keeps working.
	fullPrompt := func(ctx context.Context) string {
		prompt := spFn(ctx)
		for _, s := range skills {
			prompt += "\n" + s.PromptFragment()
		}
		return prompt
	}

	loopOpts := []loop.LoopOption{
		loop.WithLoopMaxTurns(a.maxTurns),
		loop.WithLoopMaxRetries(a.maxRetries),
		loop.WithLoopTemperature(a.temperature),
		loop.WithLoopCostModel(a.costModel),
	}
	if a.memoryMgr != nil {
		loopOpts = append(loopOpts, loop.WithLoopMemory(a.memoryMgr))
	}
	if a.pauseMgr != nil {
		loopOpts = append(loopOpts, loop.WithLoopPause(a.pauseMgr))
	}
	if a.model != "" {
		loopOpts = append(loopOpts, loop.WithLoopModel(a.model))
	}
	if a.reasoning != nil {
		loopOpts = append(loopOpts, loop.WithLoopReasoning(a.reasoning))
	}
	if a.validator != nil {
		loopOpts = append(loopOpts, loop.WithLoopTurnValidator(a.validator, a.validatorMaxRetries))
	}
	if a.dynamicTools != nil {
		loopOpts = append(loopOpts, loop.WithLoopDynamicTools(a.dynamicTools))
	}
	if a.toolChoice.Kind != provider.ToolChoiceKindAuto || a.toolChoice.Name != "" {
		loopOpts = append(loopOpts, loop.WithLoopToolChoice(a.toolChoice))
	}
	applyStructuredOutputOption(a, &loopOpts)
	if a.usageLimits != (loop.UsageLimits{}) {
		loopOpts = append(loopOpts, loop.WithLoopUsageLimits(a.usageLimits))
	}

	a.loops = loop.NewAgentLoop(p, fullPrompt, tools, loopOpts...)
	a.loops.SetHookManager(a.hooks)

	return a, nil
}

// MustNewAgent wraps NewAgent and panics on error. Use in declarative agent
// construction (tests, examples) where unsupported component types are a
// programmer error caught at startup, not a runtime condition.
func MustNewAgent(p provider.LLMProvider, systemPrompt any, components ...any) *Agent {
	a, err := NewAgent(p, systemPrompt, components...)
	if err != nil {
		panic(err)
	}
	return a
}

// Run executes the full agentic loop and returns the result.
//
// Run is a convenience wrapper around Iterate: it drains the step channel,
// adds an OpenTelemetry span around the whole call (when a TracerProvider is
// configured via WithTelemetry), and converts the loop result to the public
// RunResult shape. All step-level tracing to LOOPER_TRACE_ENDPOINT happens
// inside Iterate, so both code paths emit identical events.
func (a *Agent) Run(ctx context.Context, input string, opts ...RunOption) (*RunResult, error) {
	// Resolve a runID up-front so OTel span + the trace writer share it.
	cfg := &runConfig{maxTurns: a.maxTurns}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.runID == "" {
		cfg.runID = newRunID()
	}

	if a.telemetry != nil {
		newCtx, span := a.telemetry.StartAgentRun(ctx, "looper-agent", cfg.runID)
		ctx = newCtx
		defer span.End()
	}

	// Always force the run id into the opts so Iterate uses ours.
	opts = append(opts, WithRunID(cfg.runID))

	iter := a.Iterate(ctx, input, opts...)
	for range iter.Next() { //nolint:revive // we only need side effects
	}
	res := iter.Result()

	return &RunResult{
		Output:        res.Output,
		History:       res.History,
		Cost:          costBreakdownFromLoop(res.Cost),
		Usage:         usageFromLoop(res.Usage),
		Turns:         res.Turns,
		Status:        res.Status,
		Providers:     res.Providers,
		FallbackCalls: res.FallbackCalls,
	}, nil
}

// providerModel returns the model name from the configured provider for trace
// metadata. Returns the empty string if the provider doesn't expose one.
func (a *Agent) providerModel() string {
	if m, ok := a.provider.(interface{ Model() string }); ok {
		return m.Model()
	}
	return ""
}

// providerName classifies the provider via its concrete type. Used purely for
// trace labelling.
func (a *Agent) providerName() string {
	switch fmt.Sprintf("%T", a.provider) {
	case "*openai.Provider":
		return "openai"
	case "*anthropic.Provider":
		return "anthropic"
	case "*google.Provider":
		return "google"
	}
	return ""
}

// Iterate returns an iterator for manual step-by-step control.
//
// When LOOPER_TRACE_ENDPOINT is set, the iterator is transparently wrapped
// so that a run_start event fires before the first step, each loop.Step is
// forwarded to the trace endpoint, and run_end fires once the underlying
// iterator finishes. The returned *loop.Iterator behaves identically to the
// non-traced one — the tap is a transparent middleware on the steps channel.
//
// A caller-supplied run id (via WithRunID) is honoured so Run and Iterate
// agree on the same identifier when Run delegates here.
func (a *Agent) Iterate(ctx context.Context, input string, opts ...RunOption) *loop.Iterator {
	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	runID := cfg.runID
	if runID == "" {
		runID = newRunID()
	}

	tw := newTraceWriterFromEnv(ctx, cfg.sessionID)

	// Stamp our runID on ctx so that any tool function executed by this run
	// can spawn a sub-agent and have that sub-agent record us as its parent.
	// Done unconditionally (even when tracing is off) so dynamic prompts /
	// other consumers can still see the hierarchy if they want.
	ctx = contextWithRunID(ctx, runID)

	if tw == nil {
		return a.loops.Iterate(ctx, input, runOptsToLoop(opts)...)
	}

	tw.send(TraceRunStart, runID, RunStartData{
		Input:        input,
		SystemPrompt: a.systemPrompt(ctx),
		Model:        a.providerModel(),
		Provider:     a.providerName(),
		StartedAt:    time.Now().Format(time.RFC3339Nano),
	})

	inner := a.loops.Iterate(ctx, input, runOptsToLoop(opts)...)
	return loop.WrapIterator(inner, func(s loop.Step) {
		// Streaming deltas are intentionally NOT forwarded to the trace
		// endpoint: a single turn produces dozens-to-hundreds of chunks,
		// which becomes pure noise in Grafana / third-party sinks and
		// bloats the JSON store on disk. The accumulated assistant text
		// is preserved on StepLLMResponse.Content (one event per turn),
		// so consumers can still render the model's response without
		// every individual delta. Local live views that want the
		// streaming effect read directly from the iterator — they never
		// hit this path.
		if s.Type == loop.StepStreamingChunk {
			return
		}
		tw.send(TraceStep, runID, stepDataFrom(s))
	}, func() {
		res := inner.Result()
		tw.send(TraceRunEnd, runID, RunEndData{
			Output:        res.Output,
			Status:        res.Status,
			Turns:         res.Turns,
			TotalUSD:      res.Cost.TotalUSD,
			InputTokens:   res.Usage.InputTokens,
			OutputTokens:  res.Usage.OutputTokens,
			CachedTokens:  res.Usage.CachedTokens,
			EndedAt:       time.Now().Format(time.RFC3339Nano),
			Providers:     providersFromLoop(res.Providers),
			FallbackCalls: res.FallbackCalls,
		})
		tw.close()
	})
}

// runOptsToLoop converts the agent-level RunOptions into the loop's run
// options. Used by Run / Iterate to pass history, metadata, and max-turns
// through.
func runOptsToLoop(opts []RunOption) []loop.RunOption {
	if len(opts) == 0 {
		return nil
	}
	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	out := make([]loop.RunOption, 0, 3)
	if cfg.history != nil {
		out = append(out, loop.WithHistory(cfg.history))
	}
	if cfg.maxTurns > 0 {
		out = append(out, loop.WithRunMaxTurns(cfg.maxTurns))
	}
	if cfg.metadata != nil {
		out = append(out, loop.WithRunMetadata(cfg.metadata))
	}
	return out
}

// On registers a hook at a specific point in the agentic loop.
// Hook types: "BeforeCall", "AfterCall", "OnCancel", "BeforeFinalResponse", "AfterFinalResponse".
func (a *Agent) On(hookType string, h loop.Hook) {
	a.loops.HookManager().On(loop.HookType(hookType), h)
}

// OnBeforeToolExecution registers a hook that fires before tool calls
// execute (after pause-point gating). The hook may call params.Cancel
// to suppress a tool with a feedback reason, or params.Replace to
// substitute a tool call before execution. Hooks compose in
// registration order; each sees the cumulative mutations.
//
// Use cases: loop-detection guards (cancel the 12th list_pages call in a
// row), tool-rate limiters, business validation that needs to inspect
// arguments, declarative side-by-side audit logging.
func (a *Agent) OnBeforeToolExecution(h loop.ToolCallHook) {
	a.loops.HookManager().OnBeforeToolExecution(h)
}

// WithCustomModelCost registers pricing for a custom model.
func (a *Agent) WithCustomModelCost(model string, config telemetry.CostConfig) {
	a.costModel.WithCustomCost(model, config)
}

// HookManager returns the agent's hook manager for advanced hook configuration.
func (a *Agent) HookManager() *loop.HookManager {
	return a.loops.HookManager()
}

// newRunID generates a unique run identifier.
func newRunID() string {
	return uuid.New().String()
}

// costBreakdownFromLoop converts loop.CostBreakdown to the public type.
func costBreakdownFromLoop(cb loop.CostBreakdown) CostBreakdown {
	return CostBreakdown{
		TotalUSD:     cb.TotalUSD,
		InputUSD:     cb.InputUSD,
		OutputUSD:    cb.OutputUSD,
		CachedUSD:    cb.CachedUSD,
		SavingsUSD:   cb.SavingsUSD,
		InputTokens:  cb.InputTokens,
		OutputTokens: cb.OutputTokens,
		CachedTokens: cb.CachedTokens,
	}
}

// usageFromLoop converts loop.Usage to the public type.
func usageFromLoop(u loop.Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.CachedTokens,
	}
}

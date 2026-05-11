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
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/loop"
	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
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
	provider     provider.LLMProvider
	systemPrompt func(context.Context) string
	tools        []*tool.Tool
	skills       []skill.Skill
	loops        *loop.AgentLoop
	hooks        *loop.HookManager
	memoryMgr    memory.MemoryManager
	telemetry    *telemetry.CostTracker
	pauseMgr     *pause.PauseManager
	costModel    *telemetry.CostModel
	maxTurns     int
	maxRetries   int
	model        string
	temperature  float64
}

// NewAgent creates a new agent with the given provider, system prompt, and
// optional components (tools, skills, toolkits).
//
// The systemPrompt can be a string (static) or a func(ctx context.Context) string
// (dynamic). Components can be *tool.Tool, skill.Skill, or toolkit.Toolkit.
func NewAgent(p provider.LLMProvider, systemPrompt any, components ...any) *Agent {
	var spFn func(context.Context) string
	switch sp := systemPrompt.(type) {
	case string:
		s := sp
		spFn = func(_ context.Context) string { return s }
	case func(context.Context) string:
		spFn = sp
	default:
		panic(fmt.Sprintf("systemPrompt must be string or func(context.Context) string, got %T", systemPrompt))
	}

	reg := tool.NewToolRegistry()
	var skills []skill.Skill

	for _, comp := range components {
		switch c := comp.(type) {
		case *tool.Tool:
			reg.Add(c)
		case skill.Skill:
			skills = append(skills, c)
			c.RegisterTools(reg)
		case toolkit.Toolkit:
			c.RegisterTools(reg)
		default:
			panic(fmt.Sprintf("unsupported component type: %T", comp))
		}
	}

	tools := reg.Tools()

	loopOpts := []loop.LoopOption{
		loop.WithLoopMaxTurns(10),
		loop.WithLoopMaxRetries(3),
		loop.WithLoopTemperature(0.7),
	}

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

	// Build system prompt that includes skill fragments
	fullPrompt := func(ctx context.Context) string {
		prompt := spFn(ctx)
		for _, s := range skills {
			prompt += "\n" + s.PromptFragment()
		}
		return prompt
	}

	a.loops = loop.NewAgentLoop(p, fullPrompt, tools, loopOpts...)
	if a.hooks != nil {
		// Merge external hooks into the loop's hook manager
		for _, ht := range []loop.HookType{
			loop.HookBeforeCall,
			loop.HookAfterCall,
			loop.HookOnCancel,
			loop.HookBeforeFinalResponse,
			loop.HookAfterFinalResponse,
		} {
			if a.hooks.HasHooks(ht) {
				// Hooks are triggered via the agent's On method
				// which delegates to a.loops.HookManager()
			}
		}
	}

	return a
}

// Run executes the full agentic loop and returns the result.
func (a *Agent) Run(ctx context.Context, input string, opts ...RunOption) (*RunResult, error) {
	// Resolve options
	cfg := &runConfig{
		maxTurns: a.maxTurns,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Restore history if provided
	var history *message.History
	if cfg.history != nil {
		history = cfg.history
	} else {
		history = message.NewHistory()
	}

	// Inject metadata into context if provided
	if cfg.metadata != nil {
		for k, v := range cfg.metadata {
			ctx = context.WithValue(ctx, metadataKey(k), v)
		}
	}

	// Start OTel trace if configured
	if a.telemetry != nil {
		runID := cfg.runID
		if runID == "" {
			runID = newRunID()
		}
		newCtx, span := a.telemetry.StartAgentRun(ctx, "looper-agent", runID)
		ctx = newCtx
		defer span.End()
	}

	// Execute the loop
	result, err := a.loops.Run(ctx, input,
		loop.WithHistory(history),
		loop.WithRunMaxTurns(cfg.maxTurns),
		loop.WithRunMetadata(cfg.metadata),
	)
	if err != nil {
		return nil, err
	}

	return &RunResult{
		Output:  result.Output,
		History: result.History,
		Cost:    costBreakdownFromLoop(result.Cost),
		Usage:   usageFromLoop(result.Usage),
		Turns:   result.Turns,
		Status:  result.Status,
	}, nil
}

// Iterate returns an iterator for manual step-by-step control.
func (a *Agent) Iterate(ctx context.Context, input string, opts ...RunOption) *loop.Iterator {
	return a.loops.Iterate(ctx, input)
}

// On registers a hook at a specific point in the agentic loop.
// Hook types: "BeforeCall", "AfterCall", "OnCancel", "BeforeFinalResponse", "AfterFinalResponse".
func (a *Agent) On(hookType string, h loop.Hook) {
	a.loops.HookManager().On(loop.HookType(hookType), h)
}

// WithCustomModelCost registers pricing for a custom model.
func (a *Agent) WithCustomModelCost(model string, config telemetry.CostConfig) {
	a.costModel.WithCustomCost(model, config)
}

// HookManager returns the agent's hook manager for advanced hook configuration.
func (a *Agent) HookManager() *loop.HookManager {
	return a.loops.HookManager()
}

// metadataKey is a typed context key for injected metadata.
type metadataKey string

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

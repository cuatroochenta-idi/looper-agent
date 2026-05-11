// Package loop implements the agentic loop engine that powers
// the Looper Agent framework. It manages the iterative LLM → tool
// execution → result feedback cycle with hooks, memory management,
// and concurrency control.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/memory"
	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/pause"
	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/tool"

	"golang.org/x/sync/errgroup"
)

// StepType identifies the type of step in the agentic loop.
type StepType string

const (
	StepSystemPrompt  StepType = "system_prompt"
	StepLLMCall       StepType = "llm_call"
	StepStreamingChunk StepType = "streaming_chunk"
	StepToolCall      StepType = "tool_call"
	StepToolResult    StepType = "tool_result"
	StepFinalResponse StepType = "final_response"
	StepError         StepType = "error"
)

// Step represents a single event in the agentic loop.
type Step struct {
	Type      StepType
	Content   string
	ToolName  string
	ToolArgs  string
	Turn      int
	Error     error
}

// RunResult contains the outcome of an agent run.
type RunResult struct {
	Output  string
	History *message.History
	Cost    CostBreakdown
	Usage   Usage
	Turns   int
	Status  string
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
	mu              sync.Mutex
	provider        provider.LLMProvider
	systemPrompt    func(context.Context) string
	tools           []*tool.Tool
	hooks           *HookManager
	memoryMgr       memory.MemoryManager
	pauseMgr        *pause.PauseManager
	maxTurns        int
	maxRetries      int
	model           string
	temperature     float64
	structuredOutput json.RawMessage // schema for final_response tool
}

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

// NewAgentLoop creates a new agentic loop engine.
func NewAgentLoop(p provider.LLMProvider, systemPrompt func(context.Context) string, tools []*tool.Tool, opts ...LoopOption) *AgentLoop {
	l := &AgentLoop{
		provider:     p,
		systemPrompt: systemPrompt,
		tools:        tools,
		hooks:        NewHookManager(),
		maxTurns:     10,
		maxRetries:   3,
		temperature:  0.7,
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
	history.AddUserMessage(input)

	sysPrompt := l.resolveSystemPrompt(ctx)

	// Inject metadata into context
	for k, v := range cfg.metadata {
		ctx = context.WithValue(ctx, contextKey(k), v)
	}

	var (
		totalInputTokens  int
		totalOutputTokens int
		totalCachedTokens int
		status            = "completed"
	)

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
		allTools := l.buildToolList()
		req := provider.LLMRequest{
			SystemPrompt: sysPrompt,
			Messages:     history.Messages(),
			Tools:        allTools,
			Temperature:  l.temperature,
		}
		if l.model != "" {
			req.Model = l.model
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

		// Add assistant message
		history.AddAssistantMessage(llmResp.Content, llmResp.ToolCalls)

		// Check for tool calls
		if len(llmResp.ToolCalls) > 0 {
			// Execute tools
			if err := l.executeToolCalls(ctx, history, llmResp.ToolCalls); err != nil {
				status = "error"
				return nil, fmt.Errorf("tool execution turn %d: %w", turn, err)
			}
		} else if llmResp.IsFinal {
			// Final response
			return &RunResult{
				Output:  llmResp.Content,
				History: history,
				Cost:    l.calculateCost(llmResp.Usage, totalInputTokens, totalOutputTokens, totalCachedTokens),
				Usage:   Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
				Turns:   turn + 1,
				Status:  status,
			}, nil
		} else {
			// LLM returned content without final flag — treat as final
			return &RunResult{
				Output:  llmResp.Content,
				History: history,
				Cost:    l.calculateCost(llmResp.Usage, totalInputTokens, totalOutputTokens, totalCachedTokens),
				Usage:   Usage{totalInputTokens, totalOutputTokens, totalCachedTokens},
				Turns:   turn + 1,
				Status:  status,
			}, nil
		}

		// Trigger AfterCall hooks
		if err := l.hooks.Trigger(ctx, HookAfterCall, &CallParams{
			History:      history,
			Turn:         turn,
			MaxTurns:     l.maxTurns,
			SystemPrompt: sysPrompt,
		}); err != nil {
			log.Printf("loop: AfterCall hook warning: %v", err)
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

	// Add all results to history in original order
	for _, r := range results {
		history.AddToolResult(r.ToolCallID, "", r.Content, r.IsError)
	}

	return nil
}

// executeSingleTool runs a single tool and returns its result.
// Errors are converted to tool results with IsError=true so the LLM
// receives them as feedback for self-correction.
func (l *AgentLoop) executeSingleTool(ctx context.Context, tt *tool.Tool, tc message.ToolCall) message.ToolResult {
	// Pause point check
	if l.pauseMgr != nil {
		if cfg, ok := l.pauseMgr.HasPausePoint(tc.Name); ok {
			resp, err := l.pauseMgr.Pause(ctx, pause.PauseRequest{
				Type:     cfg.Type,
				ToolName: tc.Name,
				Message:  fmt.Sprintf("About to execute tool %q", tc.Name),
				Timeout:  cfg.Timeout,
			})
			if err != nil || (resp != nil && resp.Action == "cancel") {
				return message.ToolResult{
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Tool %q execution cancelled", tc.Name),
					IsError:    true,
				}
			}
			// If action is "data", inject the data into context or modify args
			if resp != nil && resp.Action == "data" && resp.Data != nil {
				if dataStr, ok := resp.Data.(string); ok {
					// Inject external data as override argument
					tc.Arguments = json.RawMessage(dataStr)
				}
			}
		}
	}

	// Execute the tool
	output, err := tt.Execute(ctx, tc.Arguments)
	if err != nil {
		return message.ToolResult{
			ToolCallID: tc.ID,
			Content:    fmt.Sprintf("Tool %q error: %v", tc.Name, err),
			IsError:    true,
		}
	}

	return message.ToolResult{
		ToolCallID: tc.ID,
		Content:    output,
		IsError:    false,
	}
}

// resolveSystemPrompt evaluates the system prompt function and appends
// structured output instructions if configured.
func (l *AgentLoop) resolveSystemPrompt(ctx context.Context) string {
	if l.systemPrompt == nil {
		return ""
	}
	prompt := l.systemPrompt(ctx)

	if l.structuredOutput != nil {
		schemaStr := string(l.structuredOutput)
		prompt += fmt.Sprintf(
			"\n\nYou MUST use the final_response tool to return your answer. "+
			"Do not respond with plain text. Always call final_response with a "+
			"valid object matching this JSON Schema:\n%s", schemaStr,
		)
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

// calculateCost computes the cost breakdown from accumulated usage.
func (l *AgentLoop) calculateCost(usage provider.Usage, totalInput, totalOutput, totalCached int) CostBreakdown {
	// Simple cost calculation — full cost tracking happens in telemetry.CostModel
	return CostBreakdown{
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		CachedTokens: totalCached,
	}
}

// toolNames returns a list of available tool names.
func toolNames(toolMap map[string]*tool.Tool) []string {
	names := make([]string, 0, len(toolMap))
	for name := range toolMap {
		names = append(names, name)
	}
	return names
}

// HookManager returns the loop's hook manager.
func (l *AgentLoop) HookManager() *HookManager { return l.hooks }

// buildToolList returns the full tool list including final_response if
// structured output is configured.
func (l *AgentLoop) buildToolList() []*tool.Tool {
	tools := make([]*tool.Tool, len(l.tools))
	copy(tools, l.tools)

	if l.structuredOutput != nil {
		// Create the implicit final_response tool
		finalTool := l.createFinalResponseTool()
		tools = append(tools, finalTool)
	}

	return tools
}

// finalResponseInput is the internal schema for the final_response tool.
type finalResponseInput struct {
	Output json.RawMessage `json:"output" jsonschema:"description=The final response object"`
}

// createFinalResponseTool builds the final_response tool.
func (l *AgentLoop) createFinalResponseTool() *tool.Tool {
	return tool.NewTool(finalResponseInput{},
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

// run executes the loop and emits steps.
func (it *Iterator) run(ctx context.Context) {
	defer close(it.steps)

	cfg := it.loop.resolveRunConfig(it.opts)
	history := cfg.history
	if history == nil {
		history = message.NewHistory()
	}
	history.AddUserMessage(it.input)

	sysPrompt := it.loop.resolveSystemPrompt(ctx)

	it.steps <- Step{
		Type:    StepSystemPrompt,
		Content: sysPrompt,
	}

	for turn := 0; turn < it.loop.maxTurns; turn++ {
		select {
		case <-it.done:
			return
		case <-ctx.Done():
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
			Tools:        it.loop.buildToolList(),
			Temperature:  it.loop.temperature,
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
				if chunk.Content != "" {
					fullContent += chunk.Content
					it.steps <- Step{Type: StepStreamingChunk, Content: chunk.Content, Turn: turn}
				}
				if chunk.IsFinal {
					if len(chunk.ToolCalls) > 0 {
						history.AddAssistantMessage(fullContent, chunk.ToolCalls)
						for _, tc := range chunk.ToolCalls {
							argsJSON, _ := json.Marshal(tc.Arguments)
							it.steps <- Step{
								Type:     StepToolCall,
								ToolName: tc.Name,
								ToolArgs: string(argsJSON),
								Turn:     turn,
							}
						}
						// Execute tools
						it.loop.executeToolCallsStreaming(ctx, history, chunk.ToolCalls, it.steps, turn)
					} else {
						it.steps <- Step{Type: StepFinalResponse, Content: fullContent, Turn: turn}
						return
					}
					break
				}
			}
		} else {
			// Fallback to non-streaming
			llmResp, err := it.loop.provider.Chat(ctx, req)
			if err != nil {
				it.steps <- Step{Type: StepError, Error: err, Turn: turn}
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
						Type:     StepToolCall,
						ToolName: tc.Name,
						ToolArgs: string(argsJSON),
						Turn:     turn,
					}
				}
				it.loop.executeToolCallsStreaming(ctx, history, llmResp.ToolCalls, it.steps, turn)
			} else {
				it.steps <- Step{Type: StepFinalResponse, Content: llmResp.Content, Turn: turn}
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

// executeToolCallsStreaming executes tools and emits steps.
func (l *AgentLoop) executeToolCallsStreaming(ctx context.Context, history *message.History, calls []message.ToolCall, steps chan<- Step, turn int) {
	results := l.executeToolCallsInternal(ctx, history, calls)
	for _, r := range results {
		steps <- Step{
			Type:     StepToolResult,
			Content:  r.Content,
			ToolName: r.ToolCallID,
			Turn:     turn,
		}
		history.AddToolResult(r.ToolCallID, "", r.Content, r.IsError)
	}
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

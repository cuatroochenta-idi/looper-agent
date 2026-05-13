package loop

import (
	"context"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// ToolExecutionParams is the payload passed to every BeforeToolExecution
// hook. Hooks may mutate the planned tool calls in two ways:
//
//   - Cancel(callID, reason) suppresses execution of one tool. The framework
//     emits a synthetic tool_result with IsError=true and the reason text,
//     so the LLM still sees the call as resolved and can self-correct.
//   - Replace(callID, newCall) swaps the call for another one. The callID
//     should typically be preserved so the LLM's bookkeeping stays
//     consistent — only Name/Arguments change.
//
// Multiple hooks compose sequentially: each one sees the mutations made by
// the previous hooks (cancellations + replacements) via the accessor
// methods. This makes it natural to layer a guard hook + a logger hook.
type ToolExecutionParams struct {
	// Calls is the original set of tool calls the LLM asked to execute,
	// preserved verbatim across all hooks for diagnostic / replay needs.
	Calls []message.ToolCall

	mu            sync.Mutex
	cancellations map[string]string
	replacements  map[string]message.ToolCall
}

// Cancel marks a tool call as cancelled. The loop will skip its execution
// and emit a synthetic tool_result carrying reason so the LLM sees the
// outcome and can adapt its plan.
func (p *ToolExecutionParams) Cancel(callID, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancellations == nil {
		p.cancellations = make(map[string]string)
	}
	p.cancellations[callID] = reason
}

// Replace substitutes the tool call with the supplied newCall. The new call
// inherits the original's place in the execution order. Preserve the
// original ID in newCall.ID unless you have a specific reason not to.
func (p *ToolExecutionParams) Replace(callID string, newCall message.ToolCall) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replacements == nil {
		p.replacements = make(map[string]message.ToolCall)
	}
	p.replacements[callID] = newCall
}

// Cancellations returns a snapshot of the current cancellation map keyed by
// call ID. Later hooks use this to observe earlier hooks' decisions.
func (p *ToolExecutionParams) Cancellations() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.cancellations))
	for k, v := range p.cancellations {
		out[k] = v
	}
	return out
}

// Replacements returns a snapshot of pending replacement calls keyed by ID.
func (p *ToolExecutionParams) Replacements() map[string]message.ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]message.ToolCall, len(p.replacements))
	for k, v := range p.replacements {
		out[k] = v
	}
	return out
}

// ToolCallHook is the function signature for BeforeToolExecution hooks. The
// hook may mutate params via Cancel/Replace. Returning an error aborts the
// loop with that error — use sparingly; for "don't run this tool" use
// Cancel instead so the model sees the rejection as feedback.
type ToolCallHook func(ctx context.Context, params *ToolExecutionParams) error

// OnBeforeToolExecution registers a hook that fires once per turn that has
// tool calls, just before they execute and after any pause-point gating.
// Hooks compose in registration order; each sees the cumulative mutations
// from earlier hooks.
func (hm *HookManager) OnBeforeToolExecution(h ToolCallHook) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.toolCallHooks = append(hm.toolCallHooks, h)
}

// triggerBeforeToolExecution runs all registered BeforeToolExecution hooks
// against the same params object. Returning an error from any hook aborts
// the chain and surfaces the error to the caller.
func (hm *HookManager) triggerBeforeToolExecution(ctx context.Context, p *ToolExecutionParams) error {
	hm.mu.RLock()
	hooks := make([]ToolCallHook, len(hm.toolCallHooks))
	copy(hooks, hm.toolCallHooks)
	hm.mu.RUnlock()
	for _, h := range hooks {
		if err := h(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// hasBeforeToolExecutionHooks returns true when at least one hook is
// registered, so the loop can short-circuit the mutation plumbing when
// nothing is attached.
func (hm *HookManager) hasBeforeToolExecutionHooks() bool {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return len(hm.toolCallHooks) > 0
}

// applyBeforeToolExecutionHooks fires every registered BeforeToolExecution
// hook, then translates the resulting cancellations / replacements into a
// new approved slice plus a list of synthetic error results that already
// went into history. The streaming path additionally emits step events for
// each cancelled call — that work lives in executeToolCallsStreaming, this
// helper only handles the history + slice mutation that both paths share.
//
// hookErr is returned separately so callers can surface it as a step or a
// run error without losing the partial mutations applied by earlier hooks.
func (l *AgentLoop) applyBeforeToolExecutionHooks(
	ctx context.Context,
	history *message.History,
	approved []message.ToolCall,
	nameByID map[string]string,
) (mutated []message.ToolCall, cancelled []message.ToolResult, hookErr error) {
	if l.hooks == nil || !l.hooks.hasBeforeToolExecutionHooks() {
		return approved, nil, nil
	}
	params := &ToolExecutionParams{
		Calls: append([]message.ToolCall(nil), approved...),
	}
	hookErr = l.hooks.triggerBeforeToolExecution(ctx, params)
	cancels := params.Cancellations()
	replaces := params.Replacements()
	if len(cancels) == 0 && len(replaces) == 0 {
		return approved, nil, hookErr
	}
	mutated = make([]message.ToolCall, 0, len(approved))
	for _, c := range approved {
		if reason, ok := cancels[c.ID]; ok {
			content := "Cancelled by hook: " + reason
			history.AddToolResult(c.ID, c.Name, content, true)
			cancelled = append(cancelled, message.ToolResult{
				ToolCallID: c.ID,
				Content:    content,
				IsError:    true,
			})
			continue
		}
		if newCall, ok := replaces[c.ID]; ok {
			if nameByID != nil {
				nameByID[newCall.ID] = newCall.Name
			}
			mutated = append(mutated, newCall)
			continue
		}
		mutated = append(mutated, c)
	}
	return mutated, cancelled, hookErr
}

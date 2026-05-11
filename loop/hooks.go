// Package loop implements the agentic loop engine that powers
// the Looper Agent framework. It manages the iterative LLM → tool
// execution → result feedback cycle with hooks, memory management,
// and concurrency control.
package loop

import (
	"context"
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/message"
)

// HookType identifies when a hook executes in the agentic loop.
type HookType string

const (
	// HookBeforeCall runs before each LLM call in the loop.
	HookBeforeCall HookType = "BeforeCall"

	// HookAfterCall runs after each LLM call (and tool execution).
	HookAfterCall HookType = "AfterCall"

	// HookOnCancel runs when the loop is cancelled.
	HookOnCancel HookType = "OnCancel"

	// HookBeforeFinalResponse runs before returning the final output.
	HookBeforeFinalResponse HookType = "BeforeFinalResponse"

	// HookAfterFinalResponse runs after the final output is produced.
	HookAfterFinalResponse HookType = "AfterFinalResponse"
)

// CallParams contains the context available to hooks during execution.
type CallParams struct {
	// History is the conversation history (mutable by hooks).
	History *message.History

	// Turn is the current turn number (0-indexed).
	Turn int

	// MaxTurns is the configured maximum turns.
	MaxTurns int

	// SystemPrompt is the resolved system prompt for this run.
	SystemPrompt string

	// RunID is the unique identifier for this execution.
	RunID string
}

// Hook is a function that executes at a specific point in the agentic loop.
// Returning an error aborts the loop.
type Hook func(ctx context.Context, params *CallParams) error

// HookManager stores and triggers hooks in registration order.
type HookManager struct {
	mu    sync.RWMutex
	hooks map[HookType][]Hook
}

// NewHookManager creates an empty hook manager.
func NewHookManager() *HookManager {
	return &HookManager{
		hooks: make(map[HookType][]Hook),
	}
}

// On registers a hook for the given hook type.
// Multiple hooks per type execute in registration order.
func (hm *HookManager) On(hookType HookType, h Hook) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.hooks[hookType] = append(hm.hooks[hookType], h)
}

// Trigger executes all hooks registered for the given type.
// If any hook returns an error, execution stops and the error is returned.
func (hm *HookManager) Trigger(ctx context.Context, hookType HookType, params *CallParams) error {
	hm.mu.RLock()
	hooks := make([]Hook, len(hm.hooks[hookType]))
	copy(hooks, hm.hooks[hookType])
	hm.mu.RUnlock()

	for _, hook := range hooks {
		if err := hook(ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// HasHooks returns true if at least one hook is registered for the type.
func (hm *HookManager) HasHooks(hookType HookType) bool {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return len(hm.hooks[hookType]) > 0
}

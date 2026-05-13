package loop

import (
	"context"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/tool"
)

// DynamicToolsFunc returns the list of tools to expose to the model on the
// next LLM call. It is invoked on every turn with the current conversation
// history, so callers can implement allowlists keyed on conversation state
// (e.g. "in discovery phase, hide publish_pages").
//
// The function must return slice owned by the caller — the loop reads it,
// the framework does not mutate it. Returning nil falls back to the
// agent's static tool list.
type DynamicToolsFunc func(ctx context.Context, history *message.History) []*tool.Tool

// WithLoopDynamicTools registers a function that produces the tool list per
// turn. When set, the loop calls fn(ctx, history) before each LLM call and
// uses the returned slice instead of the static tools passed to
// NewAgentLoop. The structured-output final_response tool, when configured,
// is still appended to whatever the function returns.
func WithLoopDynamicTools(fn DynamicToolsFunc) LoopOption {
	return func(l *AgentLoop) { l.dynamicTools = fn }
}

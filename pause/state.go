package pause

import "github.com/cuatroochenta-idi/looper-agent/message"

// SerializedState is a complete snapshot of the agent's state at a pause point.
// It can be stored in any backend (DB, cache, filesystem) and restored
// in another process or machine to resume execution.
type SerializedState struct {
	// ID uniquely identifies this run.
	ID string `json:"id"`

	// History contains all conversation messages up to the pause point.
	History []message.Message `json:"history"`

	// CurrentTurn is the turn number when the pause occurred.
	CurrentTurn int `json:"current_turn"`

	// MaxTurns is the configured maximum turns for this run.
	MaxTurns int `json:"max_turns"`

	// PendingTools are tool names waiting for external input.
	PendingTools []string `json:"pending_tools,omitempty"`

	// Context contains serialized context values injected at runtime.
	Context map[string]any `json:"context,omitempty"`

	// PausePoints preserves the configured pause points.
	PausePoints map[string]PausePointConfig `json:"pause_points,omitempty"`
}

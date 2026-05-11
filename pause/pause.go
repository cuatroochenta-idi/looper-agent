// Package pause implements the Pause & Resume system (man-in-the-middle).
// It allows interrupting the agentic loop at any point, interacting with an
// external actor, and resuming exactly where it left off.
//
// The full state (history, pending tools, context) is serialized for storage
// in DB, cache, or filesystem. Resume can happen in another process or machine.
package pause

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PausePointType identifies the type of pause point.
type PausePointType string

const (
	// PauseToolConfirm pauses before executing a tool, waiting for OK/Cancel.
	PauseToolConfirm PausePointType = "tool_confirm"

	// PauseToolInput pauses before executing a tool, asking for missing data.
	PauseToolInput PausePointType = "tool_input"

	// PauseFinalResp pauses before returning the final response.
	PauseFinalResp PausePointType = "final_response"

	// PauseManual pauses at a user-defined point.
	PauseManual PausePointType = "manual"
)

// PauseRequest is sent to the external actor when a pause point is hit.
type PauseRequest struct {
	// Type identifies what kind of interaction is needed.
	Type PausePointType

	// ToolName is the name of the tool being paused (if applicable).
	ToolName string

	// Message is a human-readable description of what's happening.
	Message string

	// Timeout is the maximum time to wait for a response.
	Timeout time.Duration
}

// PauseResponse is the external actor's answer to a pause request.
type PauseResponse struct {
	// Action is the actor's decision: "ok", "cancel", or "data".
	Action string

	// Data contains additional data when Action is "data".
	Data any
}

// PauseManager manages pause points and state serialization.
type PauseManager struct {
	mu          sync.RWMutex
	pausePoints map[string]PausePointConfig // keyed by tool name
	respCh      chan *PauseResponse
	cancelCh    chan struct{}
}

// PausePointConfig configures a single pause point.
type PausePointConfig struct {
	Type          PausePointType
	Timeout       time.Duration
	DefaultAction string // "ok", "cancel"
}

// NewPauseManager creates a new pause manager.
func NewPauseManager() *PauseManager {
	return &PauseManager{
		pausePoints: make(map[string]PausePointConfig),
		respCh:      make(chan *PauseResponse, 1),
		cancelCh:    make(chan struct{}),
	}
}

// SetPausePoint configures a pause point for a tool.
// Use an empty toolName for final_response pause points.
func (pm *PauseManager) SetPausePoint(toolName string, ppt PausePointType, timeout time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.pausePoints[toolName] = PausePointConfig{
		Type:    ppt,
		Timeout: timeout,
	}
}

// HasPausePoint checks if a tool has a configured pause point.
func (pm *PauseManager) HasPausePoint(toolName string) (PausePointConfig, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	cfg, ok := pm.pausePoints[toolName]
	return cfg, ok
}

// Pause interrupts execution and waits for an external response.
func (pm *PauseManager) Pause(ctx context.Context, req PauseRequest) (*PauseResponse, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case resp := <-pm.respCh:
		return resp, nil
	case <-ctx.Done():
		return &PauseResponse{Action: "cancel"}, fmt.Errorf("pause timed out")
	case <-pm.cancelCh:
		return &PauseResponse{Action: "cancel"}, nil
	}
}

// Resume sends a response to a waiting Pause call.
func (pm *PauseManager) Resume(resp *PauseResponse) error {
	select {
	case pm.respCh <- resp:
		return nil
	default:
		return fmt.Errorf("no pending pause to resume")
	}
}

// Serialize captures the current pause and run state for persistence.
func (pm *PauseManager) Serialize() SerializedState {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return SerializedState{
		PausePoints: pm.pausePoints,
	}
}

// Restore restores the pause manager state from a serialized snapshot.
func (pm *PauseManager) Restore(state SerializedState) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.pausePoints = state.PausePoints
	return nil
}

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
	// RequestID uniquely identifies this pause across concurrent runs on
	// the same PauseManager. The framework sets it to the originating tool
	// call ID. Callers may leave it empty for single-session use; Resume
	// without a RequestID then falls back to "any pending pause".
	RequestID string

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
	// RequestID identifies which pending Pause this response targets.
	// Required when multiple runs may pause concurrently on the same
	// manager; ignored when there is only one pending pause.
	RequestID string

	// Action is the actor's decision: "ok", "cancel", or "data".
	Action string

	// Data contains additional data when Action is "data".
	Data any
}

// PauseManager manages pause points and routes Resume calls back to the
// goroutine that issued each Pause. Safe for concurrent use: every Pause
// allocates its own response channel keyed by RequestID, so two runs
// pausing simultaneously can't steal each other's Resume.
//
// Callers without a RequestID get a legacy fallback channel — preserved
// so existing single-session examples don't break.
type PauseManager struct {
	mu          sync.RWMutex
	pausePoints map[string]PausePointConfig // keyed by tool name
	pending     map[string]chan *PauseResponse
	legacyCh    chan *PauseResponse
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
		pending:     make(map[string]chan *PauseResponse),
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

// Pause interrupts execution and waits for an external response. Each
// Pause allocates a private channel keyed by req.RequestID so concurrent
// pauses on the same manager can't cross-contaminate. If RequestID is
// empty the call uses a single shared legacy channel for backward compat
// with single-session callers.
func (pm *PauseManager) Pause(ctx context.Context, req PauseRequest) (*PauseResponse, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := make(chan *PauseResponse, 1)
	pm.mu.Lock()
	if req.RequestID != "" {
		pm.pending[req.RequestID] = ch
	} else {
		pm.legacyCh = ch
	}
	pm.mu.Unlock()

	defer func() {
		pm.mu.Lock()
		if req.RequestID != "" {
			delete(pm.pending, req.RequestID)
		} else if pm.legacyCh == ch {
			pm.legacyCh = nil
		}
		pm.mu.Unlock()
	}()

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return &PauseResponse{Action: "cancel"}, fmt.Errorf("pause timed out")
	case <-pm.cancelCh:
		return &PauseResponse{Action: "cancel"}, nil
	}
}

// Resume sends a response to the waiting Pause call. When resp.RequestID
// matches a pending pause the response is routed there; otherwise (legacy
// callers) it goes to the single most recent pause that did not set a
// RequestID. Returns an error when no matching pause is waiting.
func (pm *PauseManager) Resume(resp *PauseResponse) error {
	if resp == nil {
		return fmt.Errorf("resume: nil response")
	}
	pm.mu.RLock()
	ch, ok := pm.pending[resp.RequestID]
	if !ok {
		ch = pm.legacyCh
	}
	pm.mu.RUnlock()
	if ch == nil {
		return fmt.Errorf("no pending pause for request %q", resp.RequestID)
	}
	select {
	case ch <- resp:
		return nil
	default:
		return fmt.Errorf("pause channel full for request %q", resp.RequestID)
	}
}

// PendingCount returns the number of Pause calls currently waiting for a
// Resume. Used by tests + dashboards to observe how many sessions are
// blocked on user approval.
func (pm *PauseManager) PendingCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	n := len(pm.pending)
	if pm.legacyCh != nil {
		n++
	}
	return n
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

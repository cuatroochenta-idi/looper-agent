package web

import (
	"encoding/json"
	"net/http"
	"time"
)

// TraceEvent mirrors the envelope emitted by the framework's traceWriter.
// Kept in this package so the web server stays decoupled from the root
// `looper` package (no import cycle).
type TraceEvent struct {
	Type             string          `json:"type"`
	RunID            string          `json:"run_id"`
	ParentRunID      string          `json:"parent_run_id,omitempty"`
	ParentToolCallID string          `json:"parent_tool_call_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	Ts               time.Time       `json:"ts"`
	Project          string          `json:"project,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`
}

// runStartPayload mirrors looper.RunStartData.
type runStartPayload struct {
	Input        string `json:"input"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	StartedAt    string `json:"started_at"`
}

// stepPayload mirrors looper.StepData.
type stepPayload struct {
	Kind         string `json:"kind"`
	Turn         int    `json:"turn"`
	Content      string `json:"content,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolArgs     string `json:"tool_args,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	Err          string `json:"err,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	CachedTokens int    `json:"cached_tokens,omitempty"`
}

// runEndPayload mirrors looper.RunEndData.
type runEndPayload struct {
	Output       string  `json:"output,omitempty"`
	Status       string  `json:"status"`
	Turns        int     `json:"turns"`
	TotalUSD     float64 `json:"total_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CachedTokens int     `json:"cached_tokens,omitempty"`
	EndedAt      string  `json:"ended_at"`
	Err          string  `json:"err,omitempty"`
}

// apiIngest accepts a single TraceEvent per POST. The agent runtime sends
// these as it executes, so the panel reflects progress live.
func (s *Server) apiIngest(w http.ResponseWriter, r *http.Request) {
	var ev TraceEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad event: "+err.Error(), http.StatusBadRequest)
		return
	}
	if ev.RunID == "" {
		http.Error(w, "run_id required", http.StatusBadRequest)
		return
	}

	// Notify scope: TopicRun(id) is hit on every event so the detail pane
	// keeps streaming, but TopicSidebar only fires on structural changes
	// (new run, finished run) — that way the user's card selection survives
	// the 30+ step events of a single agent run. TopicChats fires on every
	// event so the chat thread renders streaming tokens live.
	topics := []Topic{TopicRun(ev.RunID), TopicChats}

	switch ev.Type {
	case "run_start":
		var d runStartPayload
		_ = json.Unmarshal(ev.Data, &d)
		started, _ := time.Parse(time.RFC3339Nano, d.StartedAt)
		if started.IsZero() {
			started = ev.Ts
		}
		// Seed step list with the system prompt + the user input so the
		// trace view can render them as the first nodes. Without this the
		// timeline shows "Awaiting first step…" and operators have no way
		// to see what the agent was actually asked.
		initialSteps := make([]TimelineStep, 0, 2)
		if d.SystemPrompt != "" {
			initialSteps = append(initialSteps, TimelineStep{
				Kind: StepKindSystemPrompt, Content: d.SystemPrompt, At: started,
			})
		}
		if d.Input != "" {
			initialSteps = append(initialSteps, TimelineStep{
				Kind: StepKindUserInput, Content: d.Input, At: started,
			})
		}
		// Idempotent: if a run with this ID already exists, refresh the
		// header fields. We also reset the timeline so a caller that reuses
		// a runID (e.g. a chat keyed by app+time-bucket) gets a clean
		// per-turn trace instead of an ever-growing one. Old steps are
		// dropped here; the previous run is preserved on disk via the
		// snapshot written at run_end.
		if existing := s.store.Find(ev.RunID); existing != nil {
			s.store.Update(ev.RunID, func(r *RunRecord) {
				r.Input = d.Input
				r.Status = RunRunning
				r.StartedAt = started
				r.EndedAt = time.Time{}
				r.Output = ""
				r.Turns = 0
				r.TotalUSD = 0
				r.Tokens = 0
				r.InputTokens = 0
				r.OutputTokens = 0
				r.CachedTokens = 0
				r.Steps = initialSteps
				if ev.SessionID != "" {
					r.SessionID = ev.SessionID
				}
				if ev.ParentRunID != "" {
					r.ParentRunID = ev.ParentRunID
				}
				if ev.Project != "" {
					r.Project = ev.Project
				}
			})
		} else {
			s.store.Add(&RunRecord{
				ID:               ev.RunID,
				SessionID:        ev.SessionID,
				ParentRunID:      ev.ParentRunID,
				ParentToolCallID: ev.ParentToolCallID,
				Project:          ev.Project,
				Input:            d.Input,
				Status:           RunRunning,
				StartedAt:        started,
				Steps:            initialSteps,
			})
		}
		topics = append(topics, TopicSidebar)

	case "step":
		var d stepPayload
		_ = json.Unmarshal(ev.Data, &d)
		s.store.AppendStep(ev.RunID, TimelineStep{
			Kind:         StepKind(d.Kind),
			Turn:         d.Turn,
			Content:      d.Content,
			ToolName:     d.ToolName,
			ToolArgs:     d.ToolArgs,
			ToolCallID:   d.ToolCallID,
			Err:          d.Err,
			At:           ev.Ts,
			InputTokens:  d.InputTokens,
			OutputTokens: d.OutputTokens,
			CachedTokens: d.CachedTokens,
		})

	case "run_end":
		var d runEndPayload
		_ = json.Unmarshal(ev.Data, &d)
		ended, _ := time.Parse(time.RFC3339Nano, d.EndedAt)
		if ended.IsZero() {
			ended = ev.Ts
		}
		status := RunStatus(d.Status)
		if status == "" {
			if d.Err != "" {
				status = RunError
			} else {
				status = RunCompleted
			}
		}
		s.store.Update(ev.RunID, func(r *RunRecord) {
			r.Status = status
			r.Output = d.Output
			r.Turns = d.Turns
			r.TotalUSD = d.TotalUSD
			r.InputTokens = d.InputTokens
			r.OutputTokens = d.OutputTokens
			r.CachedTokens = d.CachedTokens
			r.Tokens = d.InputTokens + d.OutputTokens
			r.EndedAt = ended
		})
		// Snapshot to disk now that the run is final.
		if run := s.store.Find(ev.RunID); run != nil {
			_ = writeRunFile(s.storeDir, run)
		}
		topics = append(topics, TopicSidebar)

	default:
		http.Error(w, "unknown event type: "+ev.Type, http.StatusBadRequest)
		return
	}

	s.hub.Publish(topics...)
	w.WriteHeader(http.StatusNoContent)
}

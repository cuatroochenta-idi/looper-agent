package web

import (
	"encoding/json"
	"fmt"
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
	Kind             string `json:"kind"`
	Turn             int    `json:"turn"`
	Content          string `json:"content,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolArgs         string `json:"tool_args,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Err              string `json:"err,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CachedTokens     int    `json:"cached_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Fallback         bool   `json:"fallback,omitempty"`
	APIKeySuffix     string `json:"api_key_suffix,omitempty"`
}

// providerStatsPayload mirrors looper.ProviderStatsData.
type providerStatsPayload struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	FallbackCalls    int     `json:"fallback_calls,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	TotalUSD         float64 `json:"total_usd,omitempty"`
	InputUSD         float64 `json:"input_usd,omitempty"`
	OutputUSD        float64 `json:"output_usd,omitempty"`
	CachedUSD        float64 `json:"cached_usd,omitempty"`
	CacheWriteUSD    float64 `json:"cache_write_usd,omitempty"`
	SavingsUSD       float64 `json:"savings_usd,omitempty"`
	Estimated        bool    `json:"estimated,omitempty"`
}

// runEndPayload mirrors looper.RunEndData.
type runEndPayload struct {
	Output           string                 `json:"output,omitempty"`
	Status           string                 `json:"status"`
	Turns            int                    `json:"turns"`
	TotalUSD         float64                `json:"total_usd,omitempty"`
	CostEstimated    bool                   `json:"cost_estimated,omitempty"`
	InputTokens      int                    `json:"input_tokens,omitempty"`
	OutputTokens     int                    `json:"output_tokens,omitempty"`
	CachedTokens     int                    `json:"cached_tokens,omitempty"`
	CacheWriteTokens int                    `json:"cache_write_tokens,omitempty"`
	EndedAt          string                 `json:"ended_at"`
	Err              string                 `json:"err,omitempty"`
	Providers        []providerStatsPayload `json:"providers,omitempty"`
	FallbackCalls    int                    `json:"fallback_calls,omitempty"`
}

// apiIngest accepts a single TraceEvent per POST. The agent runtime sends
// these as it executes, so the panel reflects progress live.
func (s *Server) apiIngest(w http.ResponseWriter, r *http.Request) {
	var ev TraceEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad event: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.IngestEvent(ev); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// IngestEvent applies one trace event to the store and fans out the derived
// SSE events — the transport-agnostic core behind POST /ingest. In-process
// hosts (embedded panels wired through a looper.TraceSink) call it directly,
// skipping the HTTP hop. A non-nil error means the event was malformed and
// was not applied.
func (s *Server) IngestEvent(ev TraceEvent) error {
	if ev.RunID == "" {
		return fmt.Errorf("run_id required")
	}

	// appended, when non-nil, is the step persisted by a "step" event — emitted
	// to run:<id> subscribers via step_appended after liveness propagation.
	var appended *TimelineStep
	// structural is set by run_start / run_end: those flip the run list + chat
	// list (a row appears or finalizes), so they fire the coarse changed events.
	structural := false
	// persistWorthy gates the write-through snapshot at the end: chunk-only
	// events don't change the persistable (denoised) shape, so writing them
	// would be redundant I/O on the shared store.
	persistWorthy := true

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
				r.LastSeenAt = started
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
				LastSeenAt:       started,
				Steps:            initialSteps,
			})
		}
		structural = true

	case "step":
		var d stepPayload
		_ = json.Unmarshal(ev.Data, &d)
		// Defensive filter: the framework's tracer already drops
		// streaming_chunk events before they hit the wire, but older
		// agent binaries (pre-v0.3.5) still emit them. ACK with 204
		// (no-op success) so the agent doesn't retry, but never
		// persist the noise.
		if d.Kind == string(StepKindStreamingChunk) {
			return nil // no-op success so old agents don't retry
		}
		step := TimelineStep{
			Kind:             StepKind(d.Kind),
			Turn:             d.Turn,
			Content:          d.Content,
			ToolName:         d.ToolName,
			ToolArgs:         d.ToolArgs,
			ToolCallID:       d.ToolCallID,
			Err:              d.Err,
			At:               ev.Ts,
			InputTokens:      d.InputTokens,
			OutputTokens:     d.OutputTokens,
			CachedTokens:     d.CachedTokens,
			CacheWriteTokens: d.CacheWriteTokens,
			Provider:         d.Provider,
			Model:            d.Model,
			Fallback:         d.Fallback,
			APIKeySuffix:     d.APIKeySuffix,
		}
		if step.Kind == StepKindReasoning {
			persistWorthy = false
		}
		s.store.AppendStep(ev.RunID, step)
		appended = &step

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
		providers := make([]ProviderStat, 0, len(d.Providers))
		for _, p := range d.Providers {
			providers = append(providers, ProviderStat{
				Provider:         p.Provider,
				Model:            p.Model,
				Calls:            p.Calls,
				FallbackCalls:    p.FallbackCalls,
				InputTokens:      p.InputTokens,
				OutputTokens:     p.OutputTokens,
				CachedTokens:     p.CachedTokens,
				CacheWriteTokens: p.CacheWriteTokens,
				TotalUSD:         p.TotalUSD,
				Estimated:        p.Estimated,
			})
		}
		s.store.Update(ev.RunID, func(r *RunRecord) {
			r.Status = status
			r.Output = d.Output
			r.Turns = d.Turns
			r.TotalUSD = d.TotalUSD
			r.InputTokens = d.InputTokens
			r.OutputTokens = d.OutputTokens
			r.CachedTokens = d.CachedTokens
			r.CacheWriteTokens = d.CacheWriteTokens
			r.CostEstimated = d.CostEstimated
			r.Tokens = d.InputTokens + d.OutputTokens
			r.EndedAt = ended
			r.Providers = providers
			r.FallbackCalls = d.FallbackCalls
			// Memory hygiene: strip any streaming/reasoning chunk steps from the
			// live record now the run is final (no-op for wire-denoised runs).
			r.Steps = stripChunkSteps(r.Steps)
		})
		structural = true

	default:
		return fmt.Errorf("unknown event type: %s", ev.Type)
	}

	// Propagate liveness up the run tree. Every event refreshes LastSeenAt on
	// the emitting run AND all its ancestors, so the stuck-run sweeper treats a
	// busy sub-agent as keeping its whole parent chain alive — a long-running
	// child no longer makes the main run look stuck/failed. Ancestors' subtree
	// cost/last_seen changed too, so each gets a run_updated. Cycle-guarded.
	liveAt := ev.Ts
	if liveAt.IsZero() {
		liveAt = time.Now()
	}
	var ancestors []string
	seen := map[string]bool{}
	for cur := ev.RunID; cur != ""; {
		if seen[cur] {
			break
		}
		seen[cur] = true
		parent := ""
		found := false
		s.store.Update(cur, func(r *RunRecord) {
			found = true
			if liveAt.After(r.LastSeenAt) {
				r.LastSeenAt = liveAt
			}
			parent = r.ParentRunID
		})
		if !found {
			break
		}
		if cur != ev.RunID {
			ancestors = append(ancestors, cur)
		}
		cur = parent
	}

	// Write-through: snapshot the event's run AND its ancestors (their
	// LastSeenAt just changed) so other replicas hydrating from the shared
	// store see live runs with fresh liveness — without this, a sibling pod's
	// sweeper would finalize a parent as stuck while its sub-agent works.
	if s.persist != nil && persistWorthy {
		if r := s.store.Find(ev.RunID); r != nil {
			_ = s.persist.SaveRun(r)
		}
		for _, id := range ancestors {
			if r := s.store.Find(id); r != nil {
				_ = s.persist.SaveRun(r)
			}
		}
	}

	// Fan out typed events. Every event is safe to drop — subscribers refetch a
	// REST snapshot on a gap.
	if appended != nil {
		s.publishStepAppended(ev.RunID, *appended)
	}
	s.publishRunUpdated(ev.RunID, TopicRun(ev.RunID), TopicRuns, TopicSummary)
	for _, id := range ancestors {
		s.publishRunUpdated(id, TopicRun(id), TopicSummary)
	}
	s.publishChatsChanged()
	if structural {
		s.publishRunsChanged()
	}

	return nil
}

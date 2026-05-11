package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	runs := s.store.All()
	data := map[string]any{
		"Runs":  runs,
		"Stats": computeStats(runs),
	}
	if isHX(r) {
		s.renderHX(w, "dashboard_content", data)
	} else {
		s.renderFull(w, "Looper Agent - Dashboard", "dashboard_content", data)
	}
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.store.All()
	data := map[string]any{"Runs": runs}
	if isHX(r) {
		s.renderHX(w, "runs_content", data)
	} else {
		s.renderFull(w, "Looper Agent - Runs", "runs_content", data)
	}
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.findRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Run": run}
	if isHX(r) {
		s.renderHX(w, "run_detail_content", data)
	} else {
		s.renderFull(w, fmt.Sprintf("Run %s", id[:8]), "run_detail_content", data)
	}
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	if isHX(r) {
		s.renderHX(w, "live_content", data)
	} else {
		s.renderFull(w, "Looper Agent - Live", "live_content", data)
	}
}

func (s *Server) handleLiveRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.findRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Run": run}
	if isHX(r) {
		s.renderHX(w, "live_run_content", data)
	} else {
		s.renderFull(w, fmt.Sprintf("Live Run %s", id[:8]), "live_run_content", data)
	}
}

func (s *Server) handleAPIRun(w http.ResponseWriter, r *http.Request) {
	input := r.FormValue("input")
	if input == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}

	id := uuid.New().String()
	run := &RunRecord{
		ID:        id,
		Input:     input,
		Status:    "running",
		StartedAt: time.Now(),
	}
	s.store.Add(run)

	s.sse.Send(id, SSEEvent{
		Event: "status",
		Data:  fmt.Sprintf(`{"id":"%s","status":"started","input":"%s"}`, id, input),
	})

	w.Header().Set("HX-Redirect", "/live/"+id)
	fmt.Fprintf(w, `<div style="padding:12px;background:var(--accent-light);border-radius:var(--radius-sm);color:var(--accent);font-weight:500;font-size:13px;">Run %s started &rarr;</div>`, id[:8])
}

func (s *Server) handleAPIRuns(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.store.All())
}

func (s *Server) handleAPICosts(w http.ResponseWriter, r *http.Request) {
	runs := s.store.All()
	var totalUSD float64
	var totalTokens int
	for _, r := range runs {
		totalUSD += r.TotalUSD
		totalTokens += r.Tokens
	}
	avg := 0.0
	if len(runs) > 0 {
		avg = totalUSD / float64(len(runs))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total_runs":      len(runs),
		"total_cost_usd":  totalUSD,
		"total_tokens":    totalTokens,
		"avg_cost_per_run": avg,
	})
}

func (s *Server) findRun(id string) *RunRecord {
	for _, r := range s.store.All() {
		if r.ID == id {
			return r
		}
	}
	return nil
}

type dashboardStats struct {
	TotalRuns   int
	TotalCost   float64
	TotalTokens int
	AvgTurns    float64
}

func computeStats(runs []*RunRecord) dashboardStats {
	st := dashboardStats{TotalRuns: len(runs)}
	var totalTurns int
	for _, r := range runs {
		st.TotalCost += r.TotalUSD
		st.TotalTokens += r.Tokens
		totalTurns += r.Turns
	}
	if len(runs) > 0 {
		st.AvgTurns = float64(totalTurns) / float64(len(runs))
	}
	return st
}

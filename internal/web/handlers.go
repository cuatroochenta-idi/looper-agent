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
		"Title": "Looper Agent - Dashboard",
		"Runs":  runs,
		"Stats": computeStats(runs),
	}
	s.templates.ExecuteTemplate(w, "dashboard.html", data)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.store.All()
	if r.Header.Get("HX-Request") == "true" {
		s.templates.ExecuteTemplate(w, "runs_list.html", map[string]any{"Runs": runs})
		return
	}
	s.templates.ExecuteTemplate(w, "runs.html", map[string]any{
		"Title": "Looper Agent - Runs",
		"Runs":  runs,
	})
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.findRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	s.templates.ExecuteTemplate(w, "run_detail.html", map[string]any{
		"Title": fmt.Sprintf("Run %s", id),
		"Run":   run,
	})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	s.templates.ExecuteTemplate(w, "live.html", map[string]any{
		"Title": "Looper Agent - Live",
	})
}

func (s *Server) handleLiveRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.findRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	s.templates.ExecuteTemplate(w, "live_run.html", map[string]any{
		"Title": fmt.Sprintf("Live Run %s", id[:8]),
		"Run":   run,
	})
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

	// Kick off SSE streaming
	s.sse.Send(id, SSEEvent{
		Event: "status",
		Data:  fmt.Sprintf(`{"id":"%s","status":"started","input":"%s"}`, id, input),
	})

	// Redirect to live view
	w.Header().Set("HX-Redirect", "/live/"+id)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div class="text-green-400">Run %s started</div>`, id[:8])
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total_runs":      len(runs),
		"total_cost_usd":  totalUSD,
		"total_tokens":    totalTokens,
		"avg_cost_per_run": func() float64 {
			if len(runs) == 0 {
				return 0
			}
			return totalUSD / float64(len(runs))
		}(),
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
	TotalRuns  int
	TotalCost  float64
	TotalTokens int
	AvgTurns   float64
}

func computeStats(runs []*RunRecord) dashboardStats {
	s := dashboardStats{TotalRuns: len(runs)}
	var totalTurns int
	for _, r := range runs {
		s.TotalCost += r.TotalUSD
		s.TotalTokens += r.Tokens
		totalTurns += r.Turns
	}
	if len(runs) > 0 {
		s.AvgTurns = float64(totalTurns) / float64(len(runs))
	}
	return s
}

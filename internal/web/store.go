package web

import (
	"sync"
	"time"
)

// Store is the thread-safe in-memory database of runs.
type Store struct {
	mu   sync.RWMutex
	runs []*RunRecord
}

// NewStore creates an empty run store.
func NewStore() *Store { return &Store{runs: make([]*RunRecord, 0, 64)} }

// Add appends a new run. The store keeps the only live pointer; readers get
// clones, so all mutations must go through Update / AppendStep.
func (s *Store) Add(r *RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, r)
}

// All returns a snapshot clone of every run. Cloning under the read lock is
// what makes SSE renders race-free against the ingest path: previously the
// shared pointers let renders read Steps while AppendStep grew it (torn
// slice-header reads under load — the panel froze or drew garbage).
func (s *Store) All() []*RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RunRecord, len(s.runs))
	for i, r := range s.runs {
		out[i] = r.Clone()
	}
	return out
}

// Find returns a snapshot clone of the run with the given ID, or nil.
func (s *Store) Find(id string) *RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runs {
		if r.ID == id {
			return r.Clone()
		}
	}
	return nil
}

// Update applies a mutation function to the run with the given ID, under lock.
func (s *Store) Update(id string, fn func(*RunRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID == id {
			fn(r)
			return
		}
	}
}

// AppendStep adds a step to a run's timeline, under lock.
func (s *Store) AppendStep(id string, step TimelineStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID == id {
			r.Steps = append(r.Steps, step)
			return
		}
	}
}

// Counts returns counters for the sidebar filter pills.
func (s *Store) Counts() (all, running, completed, errored, unknown int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all = len(s.runs)
	for _, r := range s.runs {
		switch r.Status {
		case RunRunning:
			running++
		case RunCompleted:
			completed++
		case RunError:
			errored++
		case RunUnknown:
			unknown++
		}
	}
	return
}

// SweepStuckRuns marks every run that has been "running" for longer than
// maxIdle without any new step as "unknown". This catches runs whose host
// process died (or whose run_end event was lost) so the UI doesn't show
// "thinking…" forever. The outcome is genuinely unknown — not a failure —
// so it gets its own neutral status rather than being lumped in with errors.
// Returns the IDs that were finalized.
func (s *Store) SweepStuckRuns(maxIdle time.Duration, now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Index children so a run can be kept alive while any of its descendants
	// is still running — liveness is scoped to the whole run tree, not the
	// individual run. A long-running sub-agent must not let its parent (the
	// main run) look stuck.
	childIdx := map[string][]*RunRecord{}
	for _, r := range s.runs {
		if r.ParentRunID != "" {
			childIdx[r.ParentRunID] = append(childIdx[r.ParentRunID], r)
		}
	}

	var finalized []string
	for _, r := range s.runs {
		if r.Status != RunRunning {
			continue
		}
		// A run with a still-running descendant is not stuck — its sub-agent
		// is doing real work. Skipped only when maxIdle > 0; the startup purge
		// (maxIdle == 0) finalizes every orphaned "running" run regardless.
		if maxIdle > 0 && hasRunningDescendant(r.ID, childIdx, map[string]bool{}) {
			continue
		}
		// LastSeenAt reflects the most recent event anywhere in this run's
		// subtree (propagated on ingest). Fall back to StartedAt / last step
		// for runs persisted before LastSeenAt existed.
		last := r.LastSeenAt
		if last.IsZero() {
			last = r.StartedAt
			if n := len(r.Steps); n > 0 {
				if t := r.Steps[n-1].At; t.After(last) {
					last = t
				}
			}
		}
		if now.Sub(last) < maxIdle {
			continue
		}
		r.Status = RunUnknown
		r.EndedAt = now
		r.Steps = append(r.Steps, TimelineStep{
			Kind: StepKindError,
			Err:  "no events received for " + maxIdle.String() + " — marked unknown (process likely died or run_end lost)",
			At:   now,
		})
		finalized = append(finalized, r.ID)
	}
	return finalized
}

// hasRunningDescendant reports whether any transitive child of id is still in
// the running state. Cycle-guarded via visited.
func hasRunningDescendant(id string, childIdx map[string][]*RunRecord, visited map[string]bool) bool {
	for _, c := range childIdx[id] {
		if visited[c.ID] {
			continue
		}
		visited[c.ID] = true
		if c.Status == RunRunning {
			return true
		}
		if hasRunningDescendant(c.ID, childIdx, visited) {
			return true
		}
	}
	return false
}

package web

import "sync"

// Store is the thread-safe in-memory database of runs.
type Store struct {
	mu   sync.RWMutex
	runs []*RunRecord
}

// NewStore creates an empty run store.
func NewStore() *Store { return &Store{runs: make([]*RunRecord, 0, 64)} }

// Add appends a new run. The pointer is shared, so subsequent mutations
// via Update / AppendStep are visible to readers immediately.
func (s *Store) Add(r *RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, r)
}

// All returns a snapshot copy of every run.
func (s *Store) All() []*RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RunRecord, len(s.runs))
	copy(out, s.runs)
	return out
}

// Find returns the run with the given ID, or nil.
func (s *Store) Find(id string) *RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runs {
		if r.ID == id {
			return r
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
func (s *Store) Counts() (all, running, completed, errored int) {
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
		}
	}
	return
}

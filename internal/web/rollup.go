package web

import "fmt"

// ─── Cost rollup ──────────────────────────────────────────────────────────────
//
// A run only tracks its own tokens/USD — every sub-agent it spawns runs on a
// fresh accumulator, so the parent's TotalUSD under-reports the true cost of an
// agentic task. CostRollup re-aggregates a run's own figures with the recursive
// total contributed by all of its descendant runs, purely at display time. The
// stored RunRecord is never mutated, so historical totals and the dashboard's
// sum-of-all-runs stat stay correct.

// CostRollup is a run's own cost/tokens plus the recursive sub-agent total.
type CostRollup struct {
	SelfUSD    float64
	SubUSD     float64
	SelfTokens int
	SubTokens  int
	SubCount   int // number of descendant runs (transitive)
	SubRunning int // descendant runs still in the "running" state (transitive)

	// Estimated is true when ANY run in the subtree (self included) carries
	// a table-estimated cost — a subtree total is only as precise as its
	// least precise contributor.
	Estimated bool
}

// TotalUSD is own + sub-agent USD.
func (c CostRollup) TotalUSD() float64 { return c.SelfUSD + c.SubUSD }

// TotalTokens is own + sub-agent tokens.
func (c CostRollup) TotalTokens() int { return c.SelfTokens + c.SubTokens }

// HasSubs reports whether this run spawned any sub-agent runs.
func (c CostRollup) HasSubs() bool { return c.SubCount > 0 }

// childrenByParent indexes a flat run list by ParentRunID. Runs with no parent
// are omitted (they're never looked up).
func childrenByParent(all []*RunRecord) map[string][]*RunRecord {
	idx := make(map[string][]*RunRecord, len(all))
	for _, r := range all {
		if r.ParentRunID != "" {
			idx[r.ParentRunID] = append(idx[r.ParentRunID], r)
		}
	}
	return idx
}

// buildRollups computes a CostRollup for every run in the set. The walk is
// memoized (O(n) overall) and cycle-guarded so a malformed parent/child loop
// can never wedge the render path.
func buildRollups(all []*RunRecord, childIndex map[string][]*RunRecord) map[string]CostRollup {
	out := make(map[string]CostRollup, len(all))
	byID := make(map[string]*RunRecord, len(all))
	for _, r := range all {
		byID[r.ID] = r
	}
	visiting := map[string]bool{}

	var walk func(r *RunRecord) CostRollup
	walk = func(r *RunRecord) CostRollup {
		if c, ok := out[r.ID]; ok {
			return c
		}
		// Cycle guard: a run reached while already on the stack contributes
		// only its own figures, breaking the loop without double-counting.
		if visiting[r.ID] {
			return CostRollup{SelfUSD: r.TotalUSD, SelfTokens: r.Tokens, Estimated: r.CostEstimated}
		}
		visiting[r.ID] = true
		c := CostRollup{SelfUSD: r.TotalUSD, SelfTokens: r.Tokens, Estimated: r.CostEstimated}
		for _, child := range childIndex[r.ID] {
			cc := walk(child)
			c.SubUSD += cc.TotalUSD()
			c.SubTokens += cc.TotalTokens()
			c.SubCount += 1 + cc.SubCount
			c.SubRunning += cc.SubRunning
			c.Estimated = c.Estimated || cc.Estimated
			if child.Status == RunRunning {
				c.SubRunning++
			}
		}
		delete(visiting, r.ID)
		out[r.ID] = c
		return c
	}

	for _, r := range all {
		walk(r)
	}
	return out
}

// ─── Model label ──────────────────────────────────────────────────────────────

// RunModelLabel returns a compact label of the model(s) a run used, for display
// on cards and bubbles where the full provider breakdown won't fit. It prefers
// the model with the most calls from the per-provider breakdown and appends a
// "+N" suffix when the run mixed models. For live runs that haven't emitted a
// run_end breakdown yet, it falls back to the model on the most recent
// usage-bearing step. Empty when the run carries no model provenance.
func RunModelLabel(r *RunRecord) string {
	if r == nil {
		return ""
	}
	if len(r.Providers) > 0 {
		best := r.Providers[0]
		distinct := map[string]struct{}{}
		for _, p := range r.Providers {
			if p.Model != "" {
				distinct[p.Model] = struct{}{}
			}
			if p.Calls > best.Calls {
				best = p
			}
		}
		label := best.Model
		if label == "" {
			label = best.Provider
		}
		if len(distinct) > 1 {
			label += fmt.Sprintf(" +%d", len(distinct)-1)
		}
		return label
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if m := r.Steps[i].Model; m != "" {
			return m
		}
	}
	return ""
}

// ─── Spawned sub-agent view-model ───────────────────────────────────────────────

// SpawnedRun is one sub-agent run rendered inline beneath the tool call that
// spawned it. It carries its own timeline, cost rollup, model label, and nested
// children so the trace can expand a whole sub-tree without navigating away.
type SpawnedRun struct {
	Run      *RunRecord
	Timeline RunTimeline
	Live     bool
	Rollup   CostRollup
	Model    string
	// Children is this sub-agent's own spawned runs, keyed by the
	// ParentToolCallID of the tool call inside this run that spawned them.
	Children map[string][]*SpawnedRun
}

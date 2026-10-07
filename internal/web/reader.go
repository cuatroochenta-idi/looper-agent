package web

import (
	"context"
	"sort"
	"strings"
	"time"
)

// RunFilter selects the runs a list endpoint shows. A zero Since keeps every
// run; Limit caps the newest matches (0 = no cap).
type RunFilter struct {
	Since  time.Time
	Status string
	Query  string
	Limit  int
}

// RunSet is a filtered run list plus the context needed to render it. Matched
// holds the runs the filter selected; All also holds their ancestors and
// descendants, so rollups, top-level checks and conversation keys resolve.
// Records in a RunSet may be headers: Steps omitted, Input/Output cut to
// previews.
type RunSet struct {
	Matched []*RunRecord
	All     []*RunRecord
}

// RunReader is the read side the REST API and SSE deltas serve from.
type RunReader interface {
	Summary(ctx context.Context, since time.Time) (SummaryResponse, error)
	Costs(ctx context.Context, since time.Time) (CostsResponse, error)
	Runs(ctx context.Context, f RunFilter) (RunSet, error)
	// Run returns the full record of id, or nil when unknown.
	Run(ctx context.Context, id string) (*RunRecord, error)
	// Subtree returns id and its descendants as headers.
	Subtree(ctx context.Context, id string) ([]*RunRecord, error)
	// Conversation returns, as headers, every run tree touching a run whose
	// session id or run id is key.
	Conversation(ctx context.Context, key string) ([]*RunRecord, error)
	// Messages returns the records of ids with full Input/Output and only the
	// steps messagesForRun reads.
	Messages(ctx context.Context, ids []string) ([]*RunRecord, error)
}

// RunRepository is a Persistence that also serves reads and sweeps stuck runs
// itself. With one, the panel keeps only in-flight runs in memory.
type RunRepository interface {
	Persistence
	RunReader
	// SweepStuckRuns marks as unknown every running run idle for maxIdle with
	// no running descendant, and returns their ids.
	SweepStuckRuns(ctx context.Context, maxIdle time.Duration, now time.Time) ([]string, error)
}

// memoryReader serves reads from the in-memory Store; used when there is no
// RunRepository.
type memoryReader struct{ store *Store }

func (m memoryReader) Summary(_ context.Context, since time.Time) (SummaryResponse, error) {
	all := runsSince(m.store.All(), since)
	inStore := idSet(all)

	var resp SummaryResponse
	var turnSum int
	for _, run := range all {
		resp.TotalUSD += run.TotalUSD
		resp.TotalTokens += run.Tokens
		if run.CostEstimated {
			resp.CostEstimated = true
		}
		if !isTopLevel(run, inStore) {
			continue
		}
		resp.TotalRuns++
		turnSum += run.Turns
		switch run.Status {
		case RunRunning:
			resp.Running++
		case RunCompleted:
			resp.Completed++
		case RunError:
			resp.Errored++
		case RunUnknown:
			resp.Unknown++
		}
	}
	if resp.TotalRuns > 0 {
		resp.AvgTurns = float64(turnSum) / float64(resp.TotalRuns)
	}
	resp.TotalUSD = round8(resp.TotalUSD)
	return resp, nil
}

func (m memoryReader) Costs(_ context.Context, since time.Time) (CostsResponse, error) {
	type key struct{ p, m string }
	idx := map[key]int{}
	var rows []ModelCost
	var total float64
	var estimated bool
	for _, run := range runsSince(m.store.All(), since) {
		total += run.TotalUSD
		if run.CostEstimated {
			estimated = true
		}
		for _, p := range run.Providers {
			k := key{p.Provider, p.Model}
			i, ok := idx[k]
			if !ok {
				i = len(rows)
				idx[k] = i
				rows = append(rows, ModelCost{Provider: p.Provider, Model: p.Model})
			}
			row := &rows[i]
			row.Calls += p.Calls
			row.InputTokens += p.InputTokens
			row.OutputTokens += p.OutputTokens
			row.CachedTokens += p.CachedTokens
			row.CacheWriteTokens += p.CacheWriteTokens
			row.USD += p.TotalUSD
			if p.Estimated {
				row.Estimated = true
			}
		}
	}
	return CostsResponse{TotalUSD: round8(total), CostEstimated: estimated, ByModel: SortModelCosts(rows)}, nil
}

func (m memoryReader) Runs(_ context.Context, f RunFilter) (RunSet, error) {
	all := m.store.All()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var matched []*RunRecord
	for _, run := range runsSince(all, f.Since) {
		if f.Status != "" && string(run.Status) != f.Status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(run.Input), q) &&
			!strings.Contains(strings.ToLower(run.ID), q) {
			continue
		}
		matched = append(matched, run)
	}
	if f.Limit > 0 && len(matched) > f.Limit {
		matched = matched[len(matched)-f.Limit:]
	}
	return RunSet{Matched: matched, All: all}, nil
}

func (m memoryReader) Run(_ context.Context, id string) (*RunRecord, error) {
	return m.store.Find(id), nil
}

func (m memoryReader) Subtree(_ context.Context, _ string) ([]*RunRecord, error) {
	return m.store.All(), nil
}

func (m memoryReader) Conversation(_ context.Context, _ string) ([]*RunRecord, error) {
	return m.store.All(), nil
}

func (m memoryReader) Messages(_ context.Context, ids []string) ([]*RunRecord, error) {
	out := make([]*RunRecord, 0, len(ids))
	for _, id := range ids {
		if r := m.store.Find(id); r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// SortModelCosts rounds and orders cost rows by USD desc, then provider and
// model; never returns nil.
func SortModelCosts(rows []ModelCost) []ModelCost {
	for i := range rows {
		rows[i].USD = round8(rows[i].USD)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].USD != rows[j].USD {
			return rows[i].USD > rows[j].USD
		}
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Model < rows[j].Model
	})
	if rows == nil {
		rows = []ModelCost{}
	}
	return rows
}

// Preview is the list/card preview of a run's input or output text.
func Preview(s string) string { return preview(s) }

func runsSince(runs []*RunRecord, since time.Time) []*RunRecord {
	if since.IsZero() {
		return runs
	}
	out := make([]*RunRecord, 0, len(runs))
	for _, run := range runs {
		if !run.StartedAt.Before(since) {
			out = append(out, run)
		}
	}
	return out
}

// Round8 rounds a USD figure the way every API response does.
func Round8(f float64) float64 { return round8(f) }

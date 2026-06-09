package web

// ─── Live per-model usage ─────────────────────────────────────────────────────
//
// The stored RunRecord only carries run-level token totals + a per-provider
// breakdown, and both are populated at run_end. A live (still-running) run
// therefore reports InputTokens/OutputTokens as 0 even though every turn already
// has real usage. usageFromTimeline re-aggregates the per-turn data the timeline
// already computed, so the header shows real tokens live and a "which models
// participated" breakdown is available during the run — not only after it ends.

// ModelUsage is the per-(provider, model) token/call breakdown for one run.
// USD is filled from the run's run_end Providers breakdown when available —
// per-step cost is not carried on the wire, so a live run shows tokens without
// a cost split until it finishes.
type ModelUsage struct {
	Provider      string
	Model         string
	Calls         int
	FallbackCalls int
	InTokens      int
	OutTokens     int
	CachedTokens  int
	USD           float64
	HasUSD        bool
}

// RunUsage is a run's own token totals plus the per-(provider, model) breakdown,
// aggregated live from the timeline turns.
type RunUsage struct {
	InTokens     int
	OutTokens    int
	CachedTokens int
	ByModel      []ModelUsage
}

// HasTokens reports whether any usage was recorded.
func (u RunUsage) HasTokens() bool { return u.InTokens > 0 || u.OutTokens > 0 }

// usageFromTimeline aggregates per-(provider, model) usage from a run's turns,
// preserving first-seen order. The run_end provider breakdown (when present)
// enriches each row with its USD.
func usageFromTimeline(tl RunTimeline, providers []ProviderStat) RunUsage {
	type key struct{ p, m string }
	idx := map[key]int{}
	var u RunUsage
	for _, t := range tl.Turns {
		if !t.HasTokens {
			continue
		}
		k := key{t.Provider, t.Model}
		i, ok := idx[k]
		if !ok {
			i = len(u.ByModel)
			idx[k] = i
			u.ByModel = append(u.ByModel, ModelUsage{Provider: t.Provider, Model: t.Model})
		}
		m := &u.ByModel[i]
		m.Calls++
		if t.Fallback {
			m.FallbackCalls++
		}
		m.InTokens += t.InTokens
		m.OutTokens += t.OutTokens
		m.CachedTokens += t.CachedToks
		u.InTokens += t.InTokens
		u.OutTokens += t.OutTokens
		u.CachedTokens += t.CachedToks
	}
	for i := range u.ByModel {
		for _, ps := range providers {
			if ps.Provider == u.ByModel[i].Provider && ps.Model == u.ByModel[i].Model {
				u.ByModel[i].USD = ps.TotalUSD
				u.ByModel[i].HasUSD = ps.TotalUSD > 0
				break
			}
		}
	}
	return u
}

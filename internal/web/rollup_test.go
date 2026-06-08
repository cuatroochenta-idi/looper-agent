package web

import "testing"

// buildRollups must aggregate a run's own cost/tokens with the recursive total
// of every descendant, and report transitive sub-agent counts (incl. running).
func TestBuildRollups_recursive(t *testing.T) {
	runs := []*RunRecord{
		{ID: "root", TotalUSD: 0.01, Tokens: 100, Status: RunRunning},
		{ID: "a", ParentRunID: "root", TotalUSD: 0.02, Tokens: 200, Status: RunCompleted},
		{ID: "b", ParentRunID: "root", TotalUSD: 0.03, Tokens: 300, Status: RunRunning},
		// grandchild under a — must roll all the way up to root.
		{ID: "a1", ParentRunID: "a", TotalUSD: 0.04, Tokens: 400, Status: RunRunning},
	}
	r := buildRollups(runs, childrenByParent(runs))

	root := r["root"]
	if root.SelfUSD != 0.01 || root.SelfTokens != 100 {
		t.Fatalf("root self wrong: %+v", root)
	}
	if got := root.SubUSD; got < 0.0899 || got > 0.0901 {
		t.Fatalf("root SubUSD = %v, want 0.09", got)
	}
	if root.SubTokens != 900 {
		t.Fatalf("root SubTokens = %d, want 900", root.SubTokens)
	}
	if root.SubCount != 3 {
		t.Fatalf("root SubCount = %d, want 3", root.SubCount)
	}
	if root.SubRunning != 2 { // b and a1
		t.Fatalf("root SubRunning = %d, want 2", root.SubRunning)
	}
	if got := root.TotalUSD(); got < 0.0999 || got > 0.1001 {
		t.Fatalf("root TotalUSD = %v, want 0.10", got)
	}
	if !root.HasSubs() {
		t.Fatalf("root should report HasSubs")
	}

	// A leaf run has no sub-agents.
	if r["a1"].HasSubs() {
		t.Fatalf("a1 is a leaf, should not report HasSubs")
	}
	if r["a1"].TotalTokens() != 400 {
		t.Fatalf("a1 TotalTokens = %d, want 400", r["a1"].TotalTokens())
	}
}

// A malformed parent/child cycle must not wedge the rollup walk. The visiting
// guard guarantees termination; if it regressed this test would hang and trip
// the go-test timeout. We only require termination + correct self-cost (the
// exact sub totals are unspecified under a cycle).
func TestBuildRollups_cycleGuard(t *testing.T) {
	runs := []*RunRecord{
		{ID: "x", ParentRunID: "y", TotalUSD: 1, Tokens: 10},
		{ID: "y", ParentRunID: "x", TotalUSD: 2, Tokens: 20},
	}
	r := buildRollups(runs, childrenByParent(runs))
	if r["x"].SelfUSD != 1 || r["y"].SelfUSD != 2 {
		t.Fatalf("self costs wrong under cycle: %+v", r)
	}
}

func TestRunModelLabel(t *testing.T) {
	// Dominant model from the per-provider breakdown, "+N" for mixes.
	mixed := &RunRecord{Providers: []ProviderStat{
		{Provider: "openai", Model: "gpt-5", Calls: 3},
		{Provider: "anthropic", Model: "claude-opus-4", Calls: 1},
	}}
	if got := RunModelLabel(mixed); got != "gpt-5 +1" {
		t.Fatalf("mixed label = %q, want %q", got, "gpt-5 +1")
	}

	single := &RunRecord{Providers: []ProviderStat{{Provider: "openai", Model: "gpt-5", Calls: 2}}}
	if got := RunModelLabel(single); got != "gpt-5" {
		t.Fatalf("single label = %q, want %q", got, "gpt-5")
	}

	// No provider breakdown yet (live run) → fall back to last step's model.
	live := &RunRecord{Steps: []TimelineStep{
		{Kind: StepKindLLMCall},
		{Kind: StepKindLLMCall, Model: "gemini-3.5-flash"},
	}}
	if got := RunModelLabel(live); got != "gemini-3.5-flash" {
		t.Fatalf("live fallback label = %q, want %q", got, "gemini-3.5-flash")
	}

	if got := RunModelLabel(&RunRecord{}); got != "" {
		t.Fatalf("no provenance label = %q, want empty", got)
	}
}

func TestStoreCounts_includesUnknown(t *testing.T) {
	store := NewStore()
	store.Add(&RunRecord{ID: "1", Status: RunRunning})
	store.Add(&RunRecord{ID: "2", Status: RunCompleted})
	store.Add(&RunRecord{ID: "3", Status: RunError})
	store.Add(&RunRecord{ID: "4", Status: RunUnknown})
	store.Add(&RunRecord{ID: "5", Status: RunUnknown})

	all, running, completed, errored, unknown := store.Counts()
	if all != 5 || running != 1 || completed != 1 || errored != 1 || unknown != 2 {
		t.Fatalf("counts = all%d run%d done%d err%d unknown%d, want 5/1/1/1/2",
			all, running, completed, errored, unknown)
	}
}

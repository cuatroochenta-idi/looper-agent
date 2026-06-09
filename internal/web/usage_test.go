package web

import "testing"

func TestUsageFromTimeline(t *testing.T) {
	tl := RunTimeline{Turns: []TurnNode{
		{HasTokens: true, Provider: "openai", Model: "gemma-4", InTokens: 100, OutTokens: 10, CachedToks: 90},
		{HasTokens: true, Provider: "openai", Model: "gemma-4", InTokens: 200, OutTokens: 20},
		{HasTokens: true, Provider: "openai", Model: "gpt-5", InTokens: 50, OutTokens: 5, Fallback: true},
		{HasTokens: false, Provider: "openai", Model: "gpt-5"}, // no usage → ignored
	}}
	providers := []ProviderStat{
		{Provider: "openai", Model: "gemma-4", TotalUSD: 0}, // free local model
		{Provider: "openai", Model: "gpt-5", TotalUSD: 0.0012},
	}

	u := usageFromTimeline(tl, providers)

	if u.InTokens != 350 || u.OutTokens != 35 || u.CachedTokens != 90 {
		t.Fatalf("totals wrong: in=%d out=%d cached=%d", u.InTokens, u.OutTokens, u.CachedTokens)
	}
	if !u.HasTokens() {
		t.Fatalf("HasTokens should be true")
	}
	if len(u.ByModel) != 2 {
		t.Fatalf("want 2 model rows, got %d", len(u.ByModel))
	}

	g := u.ByModel[0] // first-seen = gemma
	if g.Model != "gemma-4" || g.Calls != 2 || g.InTokens != 300 || g.OutTokens != 30 || g.CachedTokens != 90 {
		t.Fatalf("gemma row wrong: %+v", g)
	}
	if g.HasUSD {
		t.Fatalf("gemma is a free model — HasUSD should be false")
	}

	gpt := u.ByModel[1]
	if gpt.Model != "gpt-5" || gpt.Calls != 1 || gpt.FallbackCalls != 1 {
		t.Fatalf("gpt row wrong: %+v", gpt)
	}
	if !gpt.HasUSD || gpt.USD != 0.0012 {
		t.Fatalf("gpt USD should be enriched from providers: %+v", gpt)
	}
}

func TestUsageFromTimeline_empty(t *testing.T) {
	u := usageFromTimeline(RunTimeline{}, nil)
	if u.HasTokens() || len(u.ByModel) != 0 {
		t.Fatalf("empty timeline should yield no usage, got %+v", u)
	}
}

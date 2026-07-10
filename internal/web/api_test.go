package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ingestEvent POSTs a single TraceEvent to /ingest and asserts a 204.
func ingestEvent(t *testing.T, h http.Handler, evType, runID string, opts func(*TraceEvent), data any) {
	t.Helper()
	ev := TraceEvent{Type: evType, RunID: runID, Ts: time.Now()}
	if opts != nil {
		opts(&ev)
	}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal data: %v", err)
		}
		ev.Data = raw
	}
	body, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ingest %s/%s: status %d, body %s", evType, runID, rec.Code, rec.Body.String())
	}
}

func getJSON(t *testing.T, h http.Handler, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: decode: %v; body %s", path, err, rec.Body.String())
	}
}

// seedParentAndChild ingests a parent run that spawns one subagent child.
func seedParentAndChild(t *testing.T, h http.Handler) {
	t.Helper()
	// Parent run: llm_call → tool_call(spawn) → tool_result → run_end.
	ingestEvent(t, h, "run_start", "parent", func(e *TraceEvent) {
		e.SessionID = "sess"
		e.Project = "demo"
	}, runStartPayload{Input: "do the thing", SystemPrompt: "you are a debug agent"})
	ingestEvent(t, h, "step", "parent", nil, stepPayload{Kind: "llm_call", Turn: 0})
	ingestEvent(t, h, "step", "parent", nil, stepPayload{
		Kind: "tool_call", Turn: 0, ToolName: "spawn_agent", ToolCallID: "tc1", ToolArgs: `{"task":"research"}`,
	})
	ingestEvent(t, h, "step", "parent", nil, stepPayload{
		Kind: "tool_result", Turn: 0, ToolCallID: "tc1", Content: "sub done",
	})
	ingestEvent(t, h, "run_end", "parent", nil, runEndPayload{
		Output: "all done", Status: "completed", Turns: 2,
		TotalUSD: 0.01, InputTokens: 100, OutputTokens: 20,
		Providers: []providerStatsPayload{{Provider: "anthropic", Model: "claude-sonnet", Calls: 1, InputTokens: 100, OutputTokens: 20, TotalUSD: 0.01}},
	})

	// Child subagent run spawned by tc1.
	ingestEvent(t, h, "run_start", "child", func(e *TraceEvent) {
		e.SessionID = "sess"
		e.ParentRunID = "parent"
		e.ParentToolCallID = "tc1"
	}, runStartPayload{Input: "sub research"})
	ingestEvent(t, h, "step", "child", nil, stepPayload{Kind: "llm_call", Turn: 0})
	ingestEvent(t, h, "run_end", "child", nil, runEndPayload{
		Output: "sub done", Status: "completed", Turns: 1,
		TotalUSD: 0.02, InputTokens: 50, OutputTokens: 10, CostEstimated: true,
		Providers: []providerStatsPayload{{Provider: "openai", Model: "gpt-5", Calls: 1, InputTokens: 50, OutputTokens: 10, TotalUSD: 0.02, Estimated: true}},
	})
}

func TestAPIRunsFlatListAndKind(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	seedParentAndChild(t, h)

	var resp struct {
		Runs []RunListItem `json:"runs"`
	}
	getJSON(t, h, "/api/state/runs", &resp)
	if len(resp.Runs) != 2 {
		t.Fatalf("runs list should be flat with 2 items, got %d", len(resp.Runs))
	}
	byID := map[string]RunListItem{}
	for _, r := range resp.Runs {
		byID[r.ID] = r
	}
	parent, child := byID["parent"], byID["child"]
	if parent.Kind != "run" {
		t.Errorf("parent kind = %q, want run", parent.Kind)
	}
	if child.Kind != "subagent" {
		t.Errorf("child kind = %q, want subagent", child.Kind)
	}
	if child.ParentRunID != "parent" || child.ParentToolCallID != "tc1" {
		t.Errorf("child parent linkage wrong: %+v", child)
	}
	if parent.SelfUSD != 0.01 {
		t.Errorf("parent self_usd = %v, want 0.01", parent.SelfUSD)
	}
	if parent.SubtreeUSD != 0.03 {
		t.Errorf("parent subtree_usd = %v, want 0.03", parent.SubtreeUSD)
	}
	if parent.SubagentCount != 1 {
		t.Errorf("parent subagent_count = %d, want 1", parent.SubagentCount)
	}
	if len(parent.Models) != 1 || parent.Models[0] != "anthropic/claude-sonnet" {
		t.Errorf("parent models = %v, want [anthropic/claude-sonnet]", parent.Models)
	}
}

func TestAPIRunDetailSpawnedRunIDs(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	seedParentAndChild(t, h)

	var d RunDetail
	getJSON(t, h, "/api/state/runs/parent", &d)
	if d.SystemPrompt != "you are a debug agent" {
		t.Errorf("system_prompt = %q", d.SystemPrompt)
	}
	if len(d.ChildIDs) != 1 || d.ChildIDs[0] != "child" {
		t.Errorf("child_ids = %v, want [child]", d.ChildIDs)
	}
	// Find the spawn tool call and assert it carries spawned_run_ids, and that
	// the child content is NOT inlined (detail only lists ids).
	var found bool
	for _, turn := range d.TurnsDetail {
		for _, tc := range turn.ToolCalls {
			if tc.ID == "tc1" {
				found = true
				if len(tc.SpawnedRunIDs) != 1 || tc.SpawnedRunIDs[0] != "child" {
					t.Errorf("tc1 spawned_run_ids = %v, want [child]", tc.SpawnedRunIDs)
				}
			}
		}
	}
	if !found {
		t.Fatalf("tool call tc1 missing from turns_detail")
	}
}

func TestAPISummaryNoDoubleCount(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	seedParentAndChild(t, h)

	var s SummaryResponse
	getJSON(t, h, "/api/state/summary", &s)
	if s.TotalRuns != 1 {
		t.Errorf("total_runs = %d, want 1 (top-level only)", s.TotalRuns)
	}
	if s.Completed != 1 {
		t.Errorf("completed = %d, want 1", s.Completed)
	}
	// self over all runs = 0.01 + 0.02; never 0.05 (subtree double count).
	if s.TotalUSD != 0.03 {
		t.Errorf("total_usd = %v, want 0.03 (self only, no double count)", s.TotalUSD)
	}
	if s.TotalTokens != 180 {
		t.Errorf("total_tokens = %d, want 180", s.TotalTokens)
	}
	if !s.CostEstimated {
		t.Errorf("cost_estimated should be ORed true (child estimated)")
	}
	if s.AvgTurns != 2 {
		t.Errorf("avg_turns = %v, want 2", s.AvgTurns)
	}
}

func TestAPICostsByModel(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	seedParentAndChild(t, h)

	var c CostsResponse
	getJSON(t, h, "/api/state/costs", &c)
	if c.TotalUSD != 0.03 {
		t.Errorf("total_usd = %v, want 0.03", c.TotalUSD)
	}
	if !c.CostEstimated {
		t.Errorf("cost_estimated should be true")
	}
	got := map[string]ModelCost{}
	for _, m := range c.ByModel {
		got[m.Provider+"/"+m.Model] = m
	}
	if got["anthropic/claude-sonnet"].USD != 0.01 {
		t.Errorf("claude usd = %v, want 0.01", got["anthropic/claude-sonnet"].USD)
	}
	if got["openai/gpt-5"].USD != 0.02 || !got["openai/gpt-5"].Estimated {
		t.Errorf("gpt-5 row wrong: %+v", got["openai/gpt-5"])
	}
}

func TestAPIChatsSuppressSubagentMessages(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	seedParentAndChild(t, h)

	var list struct {
		Chats []ChatSummary `json:"chats"`
	}
	getJSON(t, h, "/api/state/chats", &list)
	if len(list.Chats) != 1 {
		t.Fatalf("expected 1 conversation (keyed by session), got %d", len(list.Chats))
	}
	conv := list.Chats[0]
	if conv.Key != "sess" {
		t.Errorf("chat key = %q, want sess", conv.Key)
	}
	if conv.MessageCount != 2 {
		t.Errorf("message_count = %d, want 2 (subagent suppressed)", conv.MessageCount)
	}
	if conv.TotalUSD != 0.03 {
		t.Errorf("chat total_usd = %v, want 0.03 (rolled up)", conv.TotalUSD)
	}

	var detail struct {
		Chat     ChatSummary       `json:"chat"`
		Messages []ChatMessageView `json:"messages"`
	}
	getJSON(t, h, "/api/state/chats/sess", &detail)
	if len(detail.Messages) != 2 {
		t.Fatalf("thread should have 2 messages, got %d", len(detail.Messages))
	}
	if detail.Messages[0].Role != "user" || detail.Messages[1].Role != "agent" {
		t.Errorf("message roles = %q,%q, want user,agent", detail.Messages[0].Role, detail.Messages[1].Role)
	}
	for _, m := range detail.Messages {
		if m.Content == "sub research" {
			t.Errorf("subagent input must not appear as a message")
		}
	}
}

// An orphan subagent (parent not in store) must NOT be dropped: it stays kind
// "subagent" in the flat runs list and falls back to a top-level chat message.
func TestAPIOrphanSubagentFallback(t *testing.T) {
	srv, _ := NewServer()
	h := srv.Handler()
	ingestEvent(t, h, "run_start", "orphan", func(e *TraceEvent) {
		e.ParentRunID = "ghost-parent" // not in store
		e.ParentToolCallID = "tcX"
	}, runStartPayload{Input: "orphan work"})
	ingestEvent(t, h, "run_end", "orphan", nil, runEndPayload{Output: "done", Status: "completed", Turns: 1, TotalUSD: 0.05})

	var resp struct {
		Runs []RunListItem `json:"runs"`
	}
	getJSON(t, h, "/api/state/runs", &resp)
	if len(resp.Runs) != 1 || resp.Runs[0].ID != "orphan" {
		t.Fatalf("orphan must appear in the flat runs list, got %+v", resp.Runs)
	}
	if resp.Runs[0].Kind != "subagent" {
		t.Errorf("orphan kind = %q, want subagent (parent still set)", resp.Runs[0].Kind)
	}

	// Summary counts the orphan as top-level (its parent is absent).
	var s SummaryResponse
	getJSON(t, h, "/api/state/summary", &s)
	if s.TotalRuns != 1 {
		t.Errorf("orphan should count as one top-level run, got %d", s.TotalRuns)
	}

	// Chat thread: orphan produces its own messages (top-level fallback).
	var list struct {
		Chats []ChatSummary `json:"chats"`
	}
	getJSON(t, h, "/api/state/chats", &list)
	if len(list.Chats) != 1 || list.Chats[0].MessageCount == 0 {
		t.Fatalf("orphan should form a conversation with messages, got %+v", list.Chats)
	}
}

func TestAPIRunDetailNotFound(t *testing.T) {
	srv, _ := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/state/runs/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing run should 404, got %d", rec.Code)
	}
}

package web

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// End-to-end render check: detailData must build the spawned sub-tree + rollup,
// and DetailPaneBody must render the sub-agent inline (collapsible node with the
// child's own trace), surface the child's model, and show the rolled-up cost.
func TestDetailPaneRendersSubagentInline(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	s.store.Add(&RunRecord{
		ID: "parent", Status: RunRunning, StartedAt: now, TotalUSD: 0.01, Tokens: 100,
		Steps: []TimelineStep{
			{Kind: StepKindToolCall, Turn: 1, ToolName: "spawn_agent", ToolCallID: "tc1", At: now},
		},
	})
	s.store.Add(&RunRecord{
		ID: "child", ParentRunID: "parent", ParentToolCallID: "tc1", Status: RunRunning,
		StartedAt: now, TotalUSD: 0.02, Tokens: 200, Input: "research the topic",
		Providers: []ProviderStat{{Provider: "openai", Model: "gpt-5", Calls: 1}},
		Steps: []TimelineStep{
			{Kind: StepKindLLMCall, Turn: 1, At: now, Provider: "openai", Model: "gpt-5"},
		},
	})

	data := s.detailData(s.store.Find("parent"))
	var buf bytes.Buffer
	if err := DetailPaneBody(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		"spawn-node",          // collapsible inline sub-agent node
		"spawned 1 sub-agent", // the spawned label
		"open full",           // navigate-to-full link still offered
		"research the topic",  // child input rendered in the summary
		"gpt-5",               // child model label
		"rollup-note",         // parent header rollup breakdown
		"incl.",               // "...incl. N sub-agents"
	} {
		if !strings.Contains(html, want) {
			t.Errorf("detail pane HTML missing %q", want)
		}
	}

	// Parent's headline cost must be the rolled-up total (0.01 + 0.02), not just
	// its own 0.01 — the whole point of the rollup.
	if !strings.Contains(html, "0.03000") {
		t.Errorf("detail pane should show rolled-up total cost 0.03000")
	}
}

// The chat agent bubble must flag spawned sub-agents (count + live) and show
// the run's model and rolled-up cost.
func TestChatBubbleShowsSubagentChip(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{
		ID: "p", SessionID: "sess", Status: RunCompleted, StartedAt: now, Output: "done",
		TotalUSD: 0.01, Tokens: 100,
		Providers: []ProviderStat{{Provider: "openai", Model: "gpt-5", Calls: 1}},
	})
	s.store.Add(&RunRecord{
		ID: "c", SessionID: "sess", ParentRunID: "p", ParentToolCallID: "tc1",
		Status: RunRunning, StartedAt: now.Add(time.Second), Input: "sub task", TotalUSD: 0.02, Tokens: 200,
	})

	data := s.chatSidebarData("", "", "", TimeRange{})
	var buf bytes.Buffer
	if err := chatMessagesContent(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		"msg-subagents", // the chip
		"1 sub-agent",   // count
		"1 live",        // the running child
		"+sub",          // rolled-up cost marker on the parent bubble
		"gpt-5",         // parent model label
	} {
		if !strings.Contains(html, want) {
			t.Errorf("chat thread HTML missing %q", want)
		}
	}
}

// Sub-agent runs whose parent is visible must NOT appear as standalone bubbles
// in the chat thread — they nest under the parent (whose bubble flags the
// count). The parent's chip still shows; the child's own input does not.
func TestChatThreadNestsSubagentBubbles(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{
		ID: "p", SessionID: "sess", Status: RunRunning, StartedAt: now,
		Input: "orchestrate the whole task", TotalUSD: 0.01, Tokens: 100,
	})
	s.store.Add(&RunRecord{
		ID: "c", SessionID: "sess", ParentRunID: "p", ParentToolCallID: "tc1",
		Status: RunRunning, StartedAt: now.Add(time.Second),
		Input: "DELEGATED-CHILD-INPUT", TotalUSD: 0.02, Tokens: 200,
	})

	data := s.chatSidebarData("", "", "", TimeRange{})
	var buf bytes.Buffer
	if err := chatMessagesContent(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render thread: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "DELEGATED-CHILD-INPUT") {
		t.Errorf("sub-agent run must not render as a standalone bubble in the thread")
	}
	if !strings.Contains(html, "msg-subagents") || !strings.Contains(html, "1 sub-agent") {
		t.Errorf("parent bubble should still flag its sub-agent")
	}

	// The conversation list card surfaces the nested-sub-agent tally.
	var cbuf bytes.Buffer
	if err := ChatSidebarBody(data).Render(context.Background(), &cbuf); err != nil {
		t.Fatalf("render conv list: %v", err)
	}
	chtml := cbuf.String()
	for _, want := range []string{"subagents-chip", "1 sub-agent", "1 live"} {
		if !strings.Contains(chtml, want) {
			t.Errorf("conversation card missing %q", want)
		}
	}
}

// A sessionless parent that spawns a sub-agent must stay ONE conversation, keyed
// by the parent — not split into the parent's conversation plus an empty "ghost"
// keyed by the sub-agent's own id. The sub-agent's cost/count must land on the
// parent's conversation (via the rollup), matching the parent bubble.
func TestSessionlessParentNoGhostConversation(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	// No SessionID on either run — a loose parent spawning a sub-agent.
	s.store.Add(&RunRecord{
		ID: "p", Status: RunCompleted, StartedAt: now, Input: "loose parent", Output: "done",
		TotalUSD: 0.01, Tokens: 100,
	})
	s.store.Add(&RunRecord{
		ID: "c", ParentRunID: "p", ParentToolCallID: "tc1", Status: RunRunning,
		StartedAt: now.Add(time.Second), Input: "GHOST-CHILD-INPUT", TotalUSD: 0.02, Tokens: 200,
	})

	data := s.chatSidebarData("", "", "", TimeRange{})
	if len(data.Conversations) != 1 {
		t.Fatalf("expected exactly 1 conversation (no ghost), got %d", len(data.Conversations))
	}
	conv := data.Conversations[0]
	if conv.ID != "p" {
		t.Errorf("conversation should be keyed by the parent (p), got %q", conv.ID)
	}
	if conv.SubAgentCount != 1 || conv.SubAgentRunning != 1 {
		t.Errorf("parent conversation should tally the sub-agent: count=%d running=%d", conv.SubAgentCount, conv.SubAgentRunning)
	}
	// Cost must include the sub-agent (own 0.01 + sub 0.02) on the real conversation.
	if conv.TotalUSD < 0.0299 || conv.TotalUSD > 0.0301 {
		t.Errorf("conversation cost should roll up the sub-agent (~0.03), got %.5f", conv.TotalUSD)
	}
	// The sub-agent must not appear as its own bubble.
	var buf bytes.Buffer
	if err := chatMessagesContent(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "GHOST-CHILD-INPUT") {
		t.Errorf("sessionless sub-agent must not render as a standalone bubble")
	}
}

// The traces sidebar parent card must flag the sub-agents it spawned (count +
// live), so an operator sees the nesting at a glance without opening the run.
func TestSidebarCardShowsSubagentBadge(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{
		ID: "p", SessionID: "sess", Status: RunRunning, StartedAt: now, Input: "parent", TotalUSD: 0.01, Tokens: 100,
	})
	s.store.Add(&RunRecord{
		ID: "c", SessionID: "sess", ParentRunID: "p", ParentToolCallID: "tc1",
		Status: RunRunning, StartedAt: now.Add(time.Second), Input: "child", TotalUSD: 0.02, Tokens: 200,
	})

	data := s.sidebarData("", "", "", TimeRange{})
	var buf bytes.Buffer
	if err := SidebarBody(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render sidebar: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"subagents-chip", "1 sub-agent", "1 live"} {
		if !strings.Contains(html, want) {
			t.Errorf("sidebar parent card missing %q", want)
		}
	}
}

// The dashboard "Recent runs" feed must list top-level runs only and flag the
// sub-agents they spawned — not list sub-agents as standalone rows.
func TestDashboardRecentExcludesSubagents(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{
		ID: "p", Status: RunRunning, StartedAt: now, Input: "TOP-LEVEL-PARENT", TotalUSD: 0.01, Tokens: 100,
	})
	s.store.Add(&RunRecord{
		ID: "c", ParentRunID: "p", ParentToolCallID: "tc1", Status: RunRunning,
		StartedAt: now.Add(time.Second), Input: "NESTED-SUBAGENT-INPUT", TotalUSD: 0.02, Tokens: 200,
	})

	data := s.dashboardData(TimeRange{})
	if len(data.Recent) != 1 || data.Recent[0].ID != "p" {
		t.Fatalf("recent feed should list only the top-level parent, got %d rows", len(data.Recent))
	}
	var buf bytes.Buffer
	if err := DashboardBody(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "NESTED-SUBAGENT-INPUT") {
		t.Errorf("sub-agent must not appear as a standalone recent-runs row")
	}
	for _, want := range []string{"TOP-LEVEL-PARENT", "subagents-chip", "1 sub-agent", "1 live"} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard recent row missing %q", want)
		}
	}
}

// The chat-trace scroll container must carry a stable, per-run id so datastar's
// morph pins it in place across live patches (preserving the operator's scroll
// position) instead of replacing the node and snapping back to the top.
func TestChatTraceScrollContainerHasStableID(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{ID: "run-xyz", Status: RunRunning, StartedAt: now, Input: "hi"})

	data := s.detailData(s.store.Find("run-xyz"))
	var buf bytes.Buffer
	if err := ChatTraceBody(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat trace: %v", err)
	}
	if !strings.Contains(buf.String(), `id="chat-trace-scroll-run-xyz"`) {
		t.Errorf("chat-trace scroll container should carry a stable per-run id")
	}
}

// A run swept to "unknown" must render with the neutral status, not as an error.
func TestDetailPaneRendersUnknownStatus(t *testing.T) {
	s := &Server{store: NewStore(), hub: NewHub()}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	s.store.Add(&RunRecord{ID: "u", Status: RunUnknown, StartedAt: now, EndedAt: now})

	data := s.detailData(s.store.Find("u"))
	var buf bytes.Buffer
	if err := DetailPaneBody(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "badge-unknown") {
		t.Errorf("detail pane should render the unknown badge")
	}
}

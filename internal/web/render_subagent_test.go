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
		"spawn-node",            // collapsible inline sub-agent node
		"spawned 1 sub-agent",   // the spawned label
		"open full",             // navigate-to-full link still offered
		"research the topic",    // child input rendered in the summary
		"gpt-5",                 // child model label
		"rollup-note",           // parent header rollup breakdown
		"incl.",                 // "...incl. N sub-agents"
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

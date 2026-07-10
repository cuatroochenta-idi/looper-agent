package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// api.go implements the JSON REST surface documented in
// docs/tasks/2026-07-10_api_contract.md. Every shape here is snake_case and
// USD fields are rounded to 8 decimals. The in-memory Store is the single
// source of truth; rollups (self vs subtree cost) are recomputed per request
// from the full store so a subagent outside a time window still contributes to
// its parent's subtree total.

const previewLen = 200

// ─── Response shapes ──────────────────────────────────────────────────────────

// RunListItem is one row in the flat runs list. Subagents are included; the
// client builds trees from parent_run_id / parent_tool_call_id.
type RunListItem struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	ParentRunID      string    `json:"parent_run_id,omitempty"`
	ParentToolCallID string    `json:"parent_tool_call_id,omitempty"`
	Kind             string    `json:"kind"` // "run" | "subagent"
	Project          string    `json:"project,omitempty"`
	InputPreview     string    `json:"input_preview"`
	OutputPreview    string    `json:"output_preview,omitempty"`
	Status           string    `json:"status"`
	Turns            int       `json:"turns"`
	StartedAt        time.Time `json:"started_at"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	SelfUSD          float64   `json:"self_usd"`
	SubtreeUSD       float64   `json:"subtree_usd"`
	CostEstimated    bool      `json:"cost_estimated"`
	Tokens           int       `json:"tokens"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	CachedTokens     int       `json:"cached_tokens"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	SubagentCount    int       `json:"subagent_count"`
	SubagentsRunning int       `json:"subagents_running"`
	Models           []string  `json:"models"`
	FallbackCalls    int       `json:"fallback_calls"`
}

// RunDetail is a single run plus its expanded turn timeline. Subagent content
// is NOT inlined — tool calls carry spawned_run_ids and the run carries
// child_ids; the client fetches child detail lazily.
type RunDetail struct {
	RunListItem
	SystemPrompt string         `json:"system_prompt,omitempty"`
	Input        string         `json:"input"`
	Output       string         `json:"output"`
	Error        string         `json:"error,omitempty"`
	Providers    []ProviderStat `json:"providers"`
	TurnsDetail  []TurnView     `json:"turns_detail"`
	ChildIDs     []string       `json:"child_ids"`
}

// TurnView is one agentic turn in a run's detail timeline.
type TurnView struct {
	Turn          int            `json:"turn"`
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	Fallback      bool           `json:"fallback"`
	APIKeySuffix  string         `json:"api_key_suffix,omitempty"`
	AssistantText string         `json:"assistant_text,omitempty"`
	Reasoning     string         `json:"reasoning,omitempty"`
	Usage         *UsageView     `json:"usage,omitempty"`
	ToolCalls     []ToolCallView `json:"tool_calls"`
	Final         string         `json:"final,omitempty"`
	Error         string         `json:"error,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	EndedAt       time.Time      `json:"ended_at,omitzero"`
}

// UsageView is the per-turn / per-step token breakdown.
type UsageView struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// ToolCallView pairs a tool call with its result and the runs it spawned.
type ToolCallView struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	ArgsJSON      string          `json:"args_json"`
	Result        *ToolResultView `json:"result,omitempty"`
	SpawnedRunIDs []string        `json:"spawned_run_ids"`
}

// ToolResultView is a tool call's result.
type ToolResultView struct {
	Content string    `json:"content"`
	IsError bool      `json:"is_error"`
	At      time.Time `json:"at"`
}

// SummaryResponse aggregates top-level run counts + self-cost totals.
type SummaryResponse struct {
	TotalRuns     int     `json:"total_runs"`
	Running       int     `json:"running"`
	Completed     int     `json:"completed"`
	Errored       int     `json:"errored"`
	Unknown       int     `json:"unknown"`
	TotalUSD      float64 `json:"total_usd"`
	CostEstimated bool    `json:"cost_estimated"`
	TotalTokens   int     `json:"total_tokens"`
	AvgTurns      float64 `json:"avg_turns"`
}

// ModelCost is one row of the cost breakdown, aggregated over self costs.
type ModelCost struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	USD              float64 `json:"usd"`
	Estimated        bool    `json:"estimated"`
}

// CostsResponse is the aggregate cost view.
type CostsResponse struct {
	TotalUSD      float64     `json:"total_usd"`
	CostEstimated bool        `json:"cost_estimated"`
	ByModel       []ModelCost `json:"by_model"`
}

// ChatSummary is one conversation card. key = root ancestor session_id||run_id.
type ChatSummary struct {
	Key           string    `json:"key"`
	Title         string    `json:"title"`
	Project       string    `json:"project,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	MessageCount  int       `json:"message_count"`
	TotalUSD      float64   `json:"total_usd"`
	CostEstimated bool      `json:"cost_estimated"`
	Running       bool      `json:"running"`
}

// ChatMessageView is one message in a conversation thread. Subagent runs whose
// parent is in-store produce NO messages (their cost rolls up to the parent).
type ChatMessageView struct {
	RunID            string    `json:"run_id"`
	Role             string    `json:"role"` // "user" | "agent"
	Content          string    `json:"content"`
	Status           string    `json:"status"`
	Streaming        bool      `json:"streaming"`
	SubagentCount    int       `json:"subagent_count"`
	SubagentsRunning int       `json:"subagents_running"`
	USD              float64   `json:"usd"`
	CostEstimated    bool      `json:"cost_estimated"`
	At               time.Time `json:"at"`
}

// ─── Endpoints ────────────────────────────────────────────────────────────────

// apiSummary aggregates SELF costs only and counts TOP-LEVEL runs only, so
// subagents never double-count. total_usd == sum of every run's self cost
// (equivalently, sum of top-level subtree costs); counts cover top-level runs.
func (s *Server) apiSummary(w http.ResponseWriter, r *http.Request) {
	all := s.filterSince(s.store.All(), r)
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
	writeJSON(w, http.StatusOK, resp)
}

// apiRuns returns the FLAT run list (subagents included). since/status/q filter.
func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	all := s.store.All()
	rollups := buildRollups(all, childrenByParent(all))

	status := r.URL.Query().Get("status")
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	items := make([]RunListItem, 0, len(all))
	for _, run := range s.filterSince(all, r) {
		if status != "" && string(run.Status) != status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(run.Input), q) &&
			!strings.Contains(strings.ToLower(run.ID), q) {
			continue
		}
		items = append(items, runListItem(run, rollups[run.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": items})
}

// apiRunDetail returns a single run's expanded timeline.
func (s *Server) apiRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	all := s.store.All()
	var run *RunRecord
	for _, x := range all {
		if x.ID == id {
			run = x
			break
		}
	}
	if run == nil {
		writeJSONError(w, http.StatusNotFound, "run not found")
		return
	}
	childIndex := childrenByParent(all)
	rollups := buildRollups(all, childIndex)
	writeJSON(w, http.StatusOK, s.runDetail(run, childIndex, rollups))
}

// apiCosts aggregates SELF costs by (provider, model) over the whole store.
func (s *Server) apiCosts(w http.ResponseWriter, r *http.Request) {
	all := s.filterSince(s.store.All(), r)

	type key struct{ p, m string }
	idx := map[key]int{}
	var rows []ModelCost
	var total float64
	var estimated bool
	for _, run := range all {
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
	writeJSON(w, http.StatusOK, CostsResponse{
		TotalUSD: round8(total), CostEstimated: estimated, ByModel: rows,
	})
}

// apiChats returns the conversation list.
func (s *Server) apiChats(w http.ResponseWriter, r *http.Request) {
	all := s.store.All()
	byID := byIDIndex(all)
	inStore := idSet(all)
	rollups := buildRollups(all, childrenByParent(all))

	groups := map[string]*ChatSummary{}
	roots := map[string]*RunRecord{} // key -> earliest run, for title
	var order []string
	for _, run := range s.filterSince(all, r) {
		k := convKeyOf(run, byID)
		c, ok := groups[k]
		if !ok {
			c = &ChatSummary{Key: k}
			groups[k] = c
			order = append(order, k)
		}
		if run.Status == RunRunning {
			c.Running = true
		}
		if run.Project != "" && c.Project == "" {
			c.Project = run.Project
		}
		if c.StartedAt.IsZero() || run.StartedAt.Before(c.StartedAt) {
			c.StartedAt = run.StartedAt
		}
		if t := effLastSeen(run); t.After(c.LastSeenAt) {
			c.LastSeenAt = t
		}
		if r0, ok := roots[k]; !ok || run.StartedAt.Before(r0.StartedAt) {
			roots[k] = run
		}
		// Only runs that emit messages contribute to the count + cost so the
		// card agrees with the thread. Subagents under a known parent do not.
		if emitsMessages(run, inStore) {
			c.MessageCount += len(messagesForRun(run, rollups[run.ID]))
			c.TotalUSD += rollups[run.ID].TotalUSD()
			if rollups[run.ID].Estimated {
				c.CostEstimated = true
			}
		}
	}

	out := make([]ChatSummary, 0, len(order))
	for _, k := range order {
		c := groups[k]
		if root := roots[k]; root != nil {
			c.Title = preview(root.Input)
		}
		c.TotalUSD = round8(c.TotalUSD)
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	writeJSON(w, http.StatusOK, map[string]any{"chats": out})
}

// apiChatDetail returns one conversation's summary + message thread.
func (s *Server) apiChatDetail(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	all := s.store.All()
	byID := byIDIndex(all)
	inStore := idSet(all)
	rollups := buildRollups(all, childrenByParent(all))

	summary := ChatSummary{Key: key}
	var root *RunRecord
	var msgs []ChatMessageView
	for _, run := range s.filterSince(all, r) {
		if convKeyOf(run, byID) != key {
			continue
		}
		if run.Status == RunRunning {
			summary.Running = true
		}
		if run.Project != "" && summary.Project == "" {
			summary.Project = run.Project
		}
		if summary.StartedAt.IsZero() || run.StartedAt.Before(summary.StartedAt) {
			summary.StartedAt = run.StartedAt
		}
		if t := effLastSeen(run); t.After(summary.LastSeenAt) {
			summary.LastSeenAt = t
		}
		if root == nil || run.StartedAt.Before(root.StartedAt) {
			root = run
		}
		if emitsMessages(run, inStore) {
			msgs = append(msgs, messagesForRun(run, rollups[run.ID])...)
			summary.TotalUSD += rollups[run.ID].TotalUSD()
			if rollups[run.ID].Estimated {
				summary.CostEstimated = true
			}
		}
	}
	if root == nil {
		writeJSONError(w, http.StatusNotFound, "chat not found")
		return
	}
	summary.Title = preview(root.Input)
	summary.MessageCount = len(msgs)
	summary.TotalUSD = round8(summary.TotalUSD)
	// user-before-agent order is preserved by SliceStable on equal timestamps.
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].At.Before(msgs[j].At) })
	if msgs == nil {
		msgs = []ChatMessageView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": summary, "messages": msgs})
}

// apiRun starts a new in-process run. Body: {"input": "..."}. Returns {"id"}.
func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	input := strings.TrimSpace(body.Input)
	if input == "" {
		writeJSONError(w, http.StatusBadRequest, "input required")
		return
	}
	id := uuid.New().String()
	now := time.Now()
	s.store.Add(&RunRecord{
		ID:         id,
		Input:      input,
		Status:     RunRunning,
		StartedAt:  now,
		LastSeenAt: now,
		Steps:      []TimelineStep{{Kind: StepKindUserInput, Content: input, At: now}},
	})
	s.publishRunsChanged()
	s.publishChatsChanged()
	s.publishRunUpdated(id, TopicRun(id), TopicSummary)

	if s.runner == nil {
		s.store.Update(id, func(r *RunRecord) {
			r.Status = RunError
			r.EndedAt = time.Now()
		})
		s.publishRunUpdated(id, TopicRun(id), TopicRuns, TopicSummary)
		writeJSONError(w, http.StatusInternalServerError, "no runner configured")
		return
	}
	go s.executeRun(id, input)
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// ─── Run executor ────────────────────────────────────────────────────────────

// executeRun drives the in-process runner for a run started via POST /api/run.
// Streaming/reasoning chunks are emitted as live-only `chunk` SSE events and are
// NEVER persisted (memory or disk); every other step is appended and announced
// via step_appended + run_updated.
func (s *Server) executeRun(runID, input string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	steps, summary, err := s.runner(ctx, input)
	if err != nil {
		s.store.Update(runID, func(r *RunRecord) {
			r.Status = RunError
			r.EndedAt = time.Now()
			r.Steps = append(r.Steps, TimelineStep{Kind: StepKindError, Err: err.Error(), At: time.Now()})
		})
		s.publishRunUpdated(runID, TopicRun(runID), TopicRuns, TopicSummary)
		s.publishRunsChanged()
		s.publishChatsChanged()
		s.saveRun(runID)
		return
	}

	maxTurn := 0
	for step := range steps {
		if step.Turn > maxTurn {
			maxTurn = step.Turn
		}
		if step.Kind == "" || (step.Kind == StepKindFinal && step.Content == "") {
			continue
		}
		if step.Kind == StepKindStreamingChunk || step.Kind == StepKindReasoning {
			kind := "text"
			if step.Kind == StepKindReasoning {
				kind = "reasoning"
			}
			// Live only: never touch the store, never reach non-subscribers.
			s.publishChunk(runID, step.Turn, kind, step.Content)
			continue
		}
		ts := timelineStepFrom(step)
		s.store.AppendStep(runID, ts)
		s.publishStepAppended(runID, ts)
		s.publishRunUpdated(runID, TopicRun(runID), TopicRuns, TopicSummary)
		s.publishChatsChanged()
	}

	sum, ok := <-summary
	if !ok {
		sum = RunSummary{Status: "completed", Turns: maxTurn + 1}
	}
	status := RunStatus(sum.Status)
	if status == "" {
		if sum.Err != nil {
			status = RunError
		} else {
			status = RunCompleted
		}
	}
	s.store.Update(runID, func(r *RunRecord) {
		r.Status = status
		r.Output = sum.Output
		r.Turns = sum.Turns
		r.TotalUSD = sum.TotalUSD
		r.CostEstimated = sum.CostEstimated
		r.InputTokens = sum.InputTokens
		r.OutputTokens = sum.OutputTokens
		r.CachedTokens = sum.CachedTokens
		r.CacheWriteTokens = sum.CacheWriteTokens
		r.Tokens = sum.InputTokens + sum.OutputTokens
		r.EndedAt = time.Now()
		// Memory hygiene: drop any chunk steps from the live record so a
		// long-lived unviewed run doesn't retain streaming deltas in RAM.
		r.Steps = stripChunkSteps(r.Steps)
	})
	s.publishRunUpdated(runID, TopicRun(runID), TopicRuns, TopicSummary)
	s.publishRunsChanged()
	s.publishChatsChanged()
	s.saveRun(runID)
}

func (s *Server) saveRun(runID string) {
	if s.persist == nil {
		return
	}
	if run := s.store.Find(runID); run != nil {
		_ = s.persist.SaveRun(run)
	}
}

// ─── Builders ─────────────────────────────────────────────────────────────────

func runListItem(r *RunRecord, rollup CostRollup) RunListItem {
	return RunListItem{
		ID:               r.ID,
		SessionID:        r.SessionID,
		ParentRunID:      r.ParentRunID,
		ParentToolCallID: r.ParentToolCallID,
		Kind:             runKind(r),
		Project:          r.Project,
		InputPreview:     preview(r.Input),
		OutputPreview:    preview(r.Output),
		Status:           string(r.Status),
		Turns:            r.Turns,
		StartedAt:        r.StartedAt,
		EndedAt:          r.EndedAt,
		LastSeenAt:       effLastSeen(r),
		SelfUSD:          round8(rollup.SelfUSD),
		SubtreeUSD:       round8(rollup.TotalUSD()),
		CostEstimated:    rollup.Estimated,
		Tokens:           r.Tokens,
		InputTokens:      r.InputTokens,
		OutputTokens:     r.OutputTokens,
		CachedTokens:     r.CachedTokens,
		CacheWriteTokens: r.CacheWriteTokens,
		SubagentCount:    rollup.SubCount,
		SubagentsRunning: rollup.SubRunning,
		Models:           modelsOf(r),
		FallbackCalls:    r.FallbackCalls,
	}
}

func (s *Server) runDetail(run *RunRecord, childIndex map[string][]*RunRecord, rollups map[string]CostRollup) RunDetail {
	tl := BuildTimeline(run.Steps)
	if !run.EndedAt.IsZero() {
		tl.EndAt = run.EndedAt
	}

	// spawned_run_ids grouped by the tool call that spawned them; child_ids is
	// the flat list of direct children.
	spawnByTool := map[string][]string{}
	childIDs := []string{}
	for _, child := range childIndex[run.ID] {
		childIDs = append(childIDs, child.ID)
		if child.ParentToolCallID != "" {
			spawnByTool[child.ParentToolCallID] = append(spawnByTool[child.ParentToolCallID], child.ID)
		}
	}
	sort.Strings(childIDs)

	turns := make([]TurnView, 0, len(tl.Turns))
	for _, t := range tl.Turns {
		tv := TurnView{
			Turn:          t.Index,
			Provider:      t.Provider,
			Model:         t.Model,
			Fallback:      t.Fallback,
			APIKeySuffix:  t.APIKeySuffix,
			AssistantText: t.AssistantText,
			Reasoning:     t.Reasoning,
			StartedAt:     t.StartAt,
			EndedAt:       t.EndAt(),
			ToolCalls:     []ToolCallView{},
		}
		if t.HasTokens {
			tv.Usage = &UsageView{InputTokens: t.InTokens, OutputTokens: t.OutTokens, CachedTokens: t.CachedToks, CacheWriteTokens: t.CacheWriteToks}
		}
		if t.Final != nil {
			tv.Final = t.Final.Content
		}
		if t.Error != nil {
			tv.Error = t.Error.Err
		}
		for _, tn := range t.ToolNodes {
			tc := ToolCallView{
				ID:            tn.Call.ToolCallID,
				Name:          tn.Call.ToolName,
				ArgsJSON:      tn.Call.ToolArgs,
				SpawnedRunIDs: spawnByTool[tn.Call.ToolCallID],
			}
			if tc.SpawnedRunIDs == nil {
				tc.SpawnedRunIDs = []string{}
			}
			if tn.Result != nil {
				tc.Result = &ToolResultView{Content: tn.Result.Content, IsError: tn.HasError(), At: tn.Result.At}
			}
			tv.ToolCalls = append(tv.ToolCalls, tc)
		}
		turns = append(turns, tv)
	}

	providers := run.Providers
	if providers == nil {
		providers = []ProviderStat{}
	}
	detail := RunDetail{
		RunListItem: runListItem(run, rollups[run.ID]),
		Input:       run.Input,
		Output:      run.Output,
		Error:       runError(run),
		Providers:   providers,
		TurnsDetail: turns,
		ChildIDs:    childIDs,
	}
	if tl.SystemPrompt != nil {
		detail.SystemPrompt = tl.SystemPrompt.Content
	}
	return detail
}

// messagesForRun yields the user + agent messages for one run. A run always
// produces an agent message; the user message is emitted only when input text
// is available.
func messagesForRun(r *RunRecord, rollup CostRollup) []ChatMessageView {
	userText := r.Input
	if userText == "" {
		for _, s := range r.Steps {
			if s.Kind == StepKindUserInput {
				userText = s.Content
				break
			}
		}
	}
	agentText := r.Output
	if agentText == "" {
		var sb strings.Builder
		for _, s := range r.Steps {
			switch s.Kind {
			case StepKindStreamingChunk, StepKindFinal:
				sb.WriteString(s.Content)
			}
		}
		agentText = sb.String()
	}

	var out []ChatMessageView
	if userText != "" {
		out = append(out, ChatMessageView{
			RunID: r.ID, Role: "user", Content: userText,
			Status: string(r.Status), At: r.StartedAt, CostEstimated: r.CostEstimated,
		})
	}
	agentAt := r.EndedAt
	if agentAt.IsZero() {
		agentAt = effLastSeen(r)
	}
	out = append(out, ChatMessageView{
		RunID: r.ID, Role: "agent", Content: agentText,
		Status:           string(r.Status),
		Streaming:        r.Status == RunRunning,
		SubagentCount:    rollup.SubCount,
		SubagentsRunning: rollup.SubRunning,
		USD:              round8(rollup.TotalUSD()),
		CostEstimated:    rollup.Estimated,
		At:               agentAt,
	})
	return out
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func runKind(r *RunRecord) string {
	if r.ParentRunID != "" {
		return "subagent"
	}
	return "run"
}

// isTopLevel reports whether run counts as a top-level run: it has no parent,
// or its parent is not in the store (an orphan falls back to top-level).
func isTopLevel(run *RunRecord, inStore map[string]bool) bool {
	return run.ParentRunID == "" || !inStore[run.ParentRunID]
}

// emitsMessages mirrors isTopLevel: a subagent whose parent is in-store never
// produces its own chat messages.
func emitsMessages(run *RunRecord, inStore map[string]bool) bool {
	return isTopLevel(run, inStore)
}

// convKeyOf keys a run to its conversation: the root ancestor's session_id, or
// that ancestor's run id when it has no session.
func convKeyOf(r *RunRecord, byID map[string]*RunRecord) string {
	cur := r
	seen := map[string]bool{}
	for cur.ParentRunID != "" && !seen[cur.ID] {
		seen[cur.ID] = true
		p := byID[cur.ParentRunID]
		if p == nil {
			break // orphan: key by the deepest visible ancestor
		}
		cur = p
	}
	if cur.SessionID != "" {
		return cur.SessionID
	}
	return cur.ID
}

// runError returns the run's terminal error text, if any, taken from the last
// error step in the timeline.
func runError(r *RunRecord) string {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Kind == StepKindError && r.Steps[i].Err != "" {
			return r.Steps[i].Err
		}
	}
	return ""
}

func modelsOf(r *RunRecord) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range r.Providers {
		label := p.Provider + "/" + p.Model
		if p.Provider == "" {
			label = p.Model
		}
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

func effLastSeen(r *RunRecord) time.Time {
	if !r.LastSeenAt.IsZero() {
		return r.LastSeenAt
	}
	return r.StartedAt
}

func preview(s string) string {
	return Truncate(strings.TrimSpace(s), previewLen)
}

func round8(f float64) float64 {
	return math.Round(f*1e8) / 1e8
}

func idSet(all []*RunRecord) map[string]bool {
	m := make(map[string]bool, len(all))
	for _, r := range all {
		m[r.ID] = true
	}
	return m
}

func byIDIndex(all []*RunRecord) map[string]*RunRecord {
	m := make(map[string]*RunRecord, len(all))
	for _, r := range all {
		m[r.ID] = r
	}
	return m
}

// filterSince applies the ?since= query param (RFC3339 timestamp or a
// duration-style window like "15m"/"1h"/"24h") to a run slice, keeping runs
// started at or after the cutoff. An empty/invalid value keeps everything.
func (s *Server) filterSince(runs []*RunRecord, r *http.Request) []*RunRecord {
	cutoff, ok := parseSince(r.URL.Query().Get("since"))
	if !ok {
		return runs
	}
	out := make([]*RunRecord, 0, len(runs))
	for _, run := range runs {
		if !run.StartedAt.Before(cutoff) {
			out = append(out, run)
		}
	}
	return out
}

// parseSince interprets a since value. It accepts a Go duration ("15m", "1h",
// "24h") as a window relative to now, or an absolute RFC3339 timestamp.
func parseSince(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" || v == "all" {
		return time.Time{}, false
	}
	if d, err := time.ParseDuration(v); err == nil {
		return time.Now().Add(-d), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// timelineStepFrom converts a live StepEvent into a persisted TimelineStep.
func timelineStepFrom(step StepEvent) TimelineStep {
	return TimelineStep{
		Kind:             step.Kind,
		Turn:             step.Turn,
		Content:          step.Content,
		ToolName:         step.ToolName,
		ToolArgs:         step.ToolArgs,
		ToolCallID:       step.ToolCallID,
		Err:              step.Err,
		At:               time.Now(),
		InputTokens:      step.InputTokens,
		OutputTokens:     step.OutputTokens,
		CachedTokens:     step.CachedTokens,
		CacheWriteTokens: step.CacheWriteTokens,
		Provider:         step.Provider,
		Model:            step.Model,
		Fallback:         step.Fallback,
		APIKeySuffix:     step.APIKeySuffix,
	}
}

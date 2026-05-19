package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"
)

// ─── Page renders (full HTML, direct navigation) ─────────────────────────────

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := s.dashboardData(readTimeRange(r))
	page(w, r, "Dashboard — Looper Agent", "/", DashboardPage(data))
}

func (s *Server) handleRunsPage(w http.ResponseWriter, r *http.Request) {
	view := s.runsViewData("", r.URL.Query().Get("q"), "", readTimeRange(r))
	page(w, r, "Traces — Looper Agent", "/runs", RunsPage(view))
}

func (s *Server) handleRunPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.store.Find(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	view := s.runsViewData("", "", id, readTimeRange(r))
	page(w, r, "Run "+shortIDOf(id), "/runs", RunsPage(view))
}

func (s *Server) handleChatsPage(w http.ResponseWriter, r *http.Request) {
	view := s.chatViewData("", "", "", readTimeRange(r))
	page(w, r, "Chats — Looper Agent", "/chats", ChatsPage(view))
}


// ─── Partials (datastar-SSE patches) ─────────────────────────────────────────

func (s *Server) partialDashboard(w http.ResponseWriter, r *http.Request) {
	patch(w, r, "#dashboard-body", datastar.ElementPatchModeInner,
		DashboardBody(s.dashboardData(readSignalTimeRange(r))))
}

// partialSidebar reads $q / $status / $selected from the datastar signal
// payload (sent as ?datastar=<json> on GETs) and re-renders the sidebar.
func (s *Server) partialSidebar(w http.ResponseWriter, r *http.Request) {
	var sig struct {
		Q        string `json:"q"`
		Status   string `json:"status"`
		Selected string `json:"selected"`
		Since    string `json:"since"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	if sig.Since == "" {
		sig.Since = r.URL.Query().Get("since")
	}
	if sig.From == "" {
		sig.From = r.URL.Query().Get("from")
	}
	if sig.To == "" {
		sig.To = r.URL.Query().Get("to")
	}
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	view := s.sidebarData(sig.Status, sig.Q, sig.Selected, tr)
	patch(w, r, "#sidebar-body", datastar.ElementPatchModeInner, SidebarBody(view))
}

func (s *Server) partialDetailPane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.store.Find(id)
	if run == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := s.detailData(run)
	patch(w, r, "#detail-pane", datastar.ElementPatchModeInner,
		DetailPaneBody(data))
}

func (s *Server) partialChatSidebar(w http.ResponseWriter, r *http.Request) {
	var sig struct {
		Q      string `json:"q"`
		Status string `json:"status"`
		Since  string `json:"since"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	patch(w, r, "#chat-sidebar-body", datastar.ElementPatchModeInner,
		ChatSidebarBody(s.chatSidebarData(sig.Status, sig.Q, "", tr)))
}

func (s *Server) partialChatThread(w http.ResponseWriter, r *http.Request) {
	var sig struct {
		Q     string `json:"q"`
		Conv  string `json:"conv"`
		Since string `json:"since"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	patch(w, r, "#chat-messages", datastar.ElementPatchModeInner,
		chatMessagesContent(s.chatSidebarData("", sig.Q, "", tr)))
}

func (s *Server) partialChatTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.store.Find(id)
	if run == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	patch(w, r, "#chat-trace", datastar.ElementPatchModeInner,
		ChatTraceBody(s.detailData(run)))
}

func (s *Server) partialTimeRefresh(w http.ResponseWriter, r *http.Request) {
	since := r.PathValue("since")
	var from, to string
	if since == "custom" {
		var sig struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		_ = datastar.ReadSignals(r, &sig)
		from = sig.From
		to = sig.To
		if from == "" {
			from = r.URL.Query().Get("from")
		}
		if to == "" {
			to = r.URL.Query().Get("to")
		}
	}
	tr := TimeRange{Since: since, From: from, To: to}

	sse := datastar.NewSSE(w, r)

	_ = sse.PatchElements(renderComponent(r, SidebarBody(s.sidebarData("", "", "", tr))),
		datastar.WithSelector("#sidebar-body"),
		datastar.WithMode(datastar.ElementPatchModeInner),
	)
	_ = sse.PatchElements(renderComponent(r, ChatSidebarBody(s.chatSidebarData("", "", "", tr))),
		datastar.WithSelector("#chat-sidebar-body"),
		datastar.WithMode(datastar.ElementPatchModeInner),
	)
	_ = sse.PatchElements(renderComponent(r, chatMessagesContent(s.chatSidebarData("", "", "", tr))),
		datastar.WithSelector("#chat-messages"),
		datastar.WithMode(datastar.ElementPatchModeInner),
	)
	_ = sse.PatchElements(renderComponent(r, DashboardBody(s.dashboardData(tr))),
		datastar.WithSelector("#dashboard-body"),
		datastar.WithMode(datastar.ElementPatchModeInner),
	)
}

// ─── JSON / control ──────────────────────────────────────────────────────────

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	input := strings.TrimSpace(r.FormValue("input"))
	if input == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}
	id := uuid.New().String()
	s.store.Add(&RunRecord{
		ID:        id,
		Input:     input,
		Status:    RunRunning,
		StartedAt: time.Now(),
		Steps: []TimelineStep{
			{Kind: StepKindUserInput, Content: input, At: time.Now()},
		},
	})
	// New card: sidebar needs to rebuild. Detail pane is wired via TopicRun.
	// TopicChats keeps the chat view in sync with the new run.
	s.hub.Publish(TopicRun(id), TopicSidebar, TopicChats)

	if s.runner == nil {
		s.store.Update(id, func(r *RunRecord) {
			r.Status = RunError
			r.EndedAt = time.Now()
		})
		http.Error(w, "no runner configured", http.StatusInternalServerError)
		return
	}

	go s.executeRun(id, input)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":  id,
		"url": "/runs/" + id,
	})
}

func (s *Server) apiListRuns(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.store.All())
}

func (s *Server) apiCosts(w http.ResponseWriter, r *http.Request) {
	runs := s.store.All()
	var usd float64
	var tok int
	for _, r := range runs {
		usd += r.TotalUSD
		tok += r.Tokens
	}
	avg := 0.0
	if len(runs) > 0 {
		avg = usd / float64(len(runs))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_runs":       len(runs),
		"total_cost_usd":   usd,
		"total_tokens":     tok,
		"avg_cost_per_run": avg,
	})
}

// ─── Run executor ────────────────────────────────────────────────────────────

func (s *Server) executeRun(runID, input string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	steps, summary, err := s.runner(ctx, input)
	if err != nil {
		s.store.Update(runID, func(r *RunRecord) {
			r.Status = RunError
			r.EndedAt = time.Now()
			r.Steps = append(r.Steps, TimelineStep{
				Kind: StepKindError, Err: err.Error(), At: time.Now(),
			})
		})
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
		s.store.AppendStep(runID, TimelineStep{
			Kind:         step.Kind,
			Turn:         step.Turn,
			Content:      step.Content,
			ToolName:     step.ToolName,
			ToolArgs:     step.ToolArgs,
			ToolCallID:   step.ToolCallID,
			Err:          step.Err,
			At:           time.Now(),
			InputTokens:  step.InputTokens,
			OutputTokens: step.OutputTokens,
			CachedTokens: step.CachedTokens,
		})
		// Per-step: detail pane + chat thread re-render. Sidebar stays put
		// so the user's card selection isn't clobbered by churn.
		s.hub.Publish(TopicRun(runID), TopicChats)
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
		r.InputTokens = sum.InputTokens
		r.OutputTokens = sum.OutputTokens
		r.CachedTokens = sum.CachedTokens
		r.Tokens = sum.InputTokens + sum.OutputTokens
		r.EndedAt = time.Now()
	})
	// Final state: detail pane gets a refresh AND sidebar gets the new
	// status / totals / final cost. Chat thread too so the agent bubble
	// flips from streaming text to the final output.
	s.hub.Publish(TopicRun(runID), TopicSidebar, TopicChats)
	// Mirror to disk so the next `looper serve` reload can replay this run.
	if r := s.store.Find(runID); r != nil {
		_ = writeRunFile(s.storeDir, r)
	}
}

// ─── View-model builders ─────────────────────────────────────────────────────

func (s *Server) dashboardData(tr TimeRange) DashboardData {
	runs := s.store.All()
	runs = s.filterByTime(runs, tr)
	var usd float64
	var tok, turns int
	for _, r := range runs {
		usd += r.TotalUSD
		tok += r.Tokens
		turns += r.Turns
	}
	avg := 0.0
	if len(runs) > 0 {
		avg = float64(turns) / float64(len(runs))
	}
	// Recent first.
	recent := make([]*RunRecord, 0, len(runs))
	for i := len(runs) - 1; i >= 0; i-- {
		recent = append(recent, runs[i])
		if len(recent) >= 12 {
			break
		}
	}
	return DashboardData{
		TotalRuns:   len(runs),
		TotalCost:   usd,
		TotalTokens: tok,
		AvgTurns:    avg,
		Recent:      recent,
	}
}

func (s *Server) runsViewData(filter, query, selectedID string, tr TimeRange) RunsViewData {
	sidebar := s.sidebarData(filter, query, selectedID, tr)
	var detail *DetailData
	if sidebar.Selected != nil {
		d := s.detailData(sidebar.Selected)
		detail = &d
	}
	return RunsViewData{Sidebar: sidebar, Detail: detail}
}

func (s *Server) sidebarData(filter, query, selectedID string, tr TimeRange) SidebarData {
	all := s.store.All()
	all = s.filterByTime(all, tr)
	q := strings.ToLower(strings.TrimSpace(query))

	// Apply filter + search.
	matched := make([]*RunRecord, 0, len(all))
	for _, r := range all {
		if filter != "" && string(r.Status) != filter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Input), q) && !strings.Contains(strings.ToLower(r.ID), q) {
			continue
		}
		matched = append(matched, r)
	}

	// Group by SessionID. Runs without one go into Loose. Sessions are sorted
	// by most-recent activity (descending); runs within a session keep
	// chronological order (oldest first → matches replay order in example 07).
	byID := map[string]*SessionGroup{}
	var loose []*RunRecord
	for _, r := range matched {
		if r.SessionID == "" {
			loose = append(loose, r)
			continue
		}
		g, ok := byID[r.SessionID]
		if !ok {
			g = &SessionGroup{ID: r.SessionID, Project: r.Project, StartedAt: r.StartedAt, EndedAt: r.EndedAt}
			byID[r.SessionID] = g
		}
		g.Runs = append(g.Runs, r)
		if r.StartedAt.Before(g.StartedAt) || g.StartedAt.IsZero() {
			g.StartedAt = r.StartedAt
		}
		if !r.EndedAt.IsZero() && r.EndedAt.After(g.EndedAt) {
			g.EndedAt = r.EndedAt
		}
		if r.Project != "" && g.Project == "" {
			g.Project = r.Project
		}
		g.TotalUSD += r.TotalUSD
		switch r.Status {
		case RunRunning:
			g.HasRunning = true
		case RunError:
			g.HasError = true
		}
	}
	groups := make([]SessionGroup, 0, len(byID))
	for _, g := range byID {
		// Chronological order inside each session — keeps replay order stable.
		sort.Slice(g.Runs, func(i, j int) bool { return g.Runs[i].StartedAt.Before(g.Runs[j].StartedAt) })
		g.Roots = buildRunTree(g.Runs)
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return mostRecentTime(groups[i]).After(mostRecentTime(groups[j]))
	})
	// Loose runs: newest first.
	sort.Slice(loose, func(i, j int) bool { return loose[i].StartedAt.After(loose[j].StartedAt) })

	allN, run, done, errN := s.store.Counts()
	var selected *RunRecord
	if selectedID != "" {
		selected = s.store.Find(selectedID)
	}
	if selected == nil {
		// Default-pick the first visible run: first group's first run, or first loose.
		switch {
		case len(groups) > 0 && len(groups[0].Runs) > 0:
			selected = groups[0].Runs[0]
		case len(loose) > 0:
			selected = loose[0]
		}
	}
	return SidebarData{
		Groups:     groups,
		Loose:      loose,
		Selected:   selected,
		Filter:     filter,
		Query:      query,
		CountAll:   allN,
		CountRun:   run,
		CountDone:  done,
		CountError: errN,
	}
}

func (s *Server) chatViewData(filter, query, selectedID string, tr TimeRange) ChatViewData {
	sidebar := s.chatSidebarData(filter, query, selectedID, tr)
	var detail *DetailData
	if sidebar.Selected != nil {
		d := s.detailData(sidebar.Selected)
		detail = &d
	}
	return ChatViewData{Sidebar: sidebar, Detail: detail}
}

func (s *Server) chatSidebarData(filter, query, selectedID string, tr TimeRange) ChatSidebarData {
	all := s.store.All()
	all = s.filterByTime(all, tr)
	q := strings.ToLower(strings.TrimSpace(query))

	matched := make([]*RunRecord, 0, len(all))
	for _, r := range all {
		if filter != "" && string(r.Status) != filter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Input), q) && !strings.Contains(strings.ToLower(r.ID), q) {
			continue
		}
		matched = append(matched, r)
	}

	byID := map[string]*ChatConversation{}
	for _, r := range matched {
		sid := r.SessionID
		if sid == "" {
			sid = r.ID
		}
		conv, ok := byID[sid]
		if !ok {
			conv = &ChatConversation{ID: sid, ShortID: ShortID(sid), Project: r.Project}
			byID[sid] = conv
		}
		// Status class aligns with the .msg-dot.s-*, .msg-row.s-* selectors
		// in styles.templ. Earlier "status-error" / "status-running" values
		// never matched the CSS — bubbles stayed neutral even on errors.
		statusClass := "s-" + string(r.Status)
		switch r.Status {
		case RunRunning:
			conv.HasRunning = true
		case RunError:
			conv.HasError = true
		}
		// Each run is one user turn + one agent turn. Emit both bubbles —
		// previously we picked one of the two, which dropped the user input
		// after reload (run_start ingest does not carry a user_input step).
		userText := r.Input
		if userText == "" {
			for _, s := range r.Steps {
				if s.Kind == StepKindUserInput {
					userText = s.Content
					break
				}
			}
		}
		if userText != "" {
			conv.Messages = append(conv.Messages, ChatMessage{
				Run: r, Association: ChatUser, Text: userText, StatusClass: statusClass,
			})
		}
		// Agent reply: final Output if the run finished; otherwise accumulate
		// streaming chunks (and any partial final_response step) so the
		// bubble grows as the model emits tokens.
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
		conv.Messages = append(conv.Messages, ChatMessage{
			Run: r, Association: ChatAgent, Text: agentText, StatusClass: statusClass,
		})
		conv.TotalUSD += r.TotalUSD
		if r.Project != "" && conv.Project == "" {
			conv.Project = r.Project
		}
	}

	conversations := make([]ChatConversation, 0, len(byID))
	for _, c := range byID {
		// SliceStable preserves the user-before-agent insertion order for
		// messages that share the same Run.StartedAt.
		sort.SliceStable(c.Messages, func(i, j int) bool {
			return c.Messages[i].Run.StartedAt.Before(c.Messages[j].Run.StartedAt)
		})
		conversations = append(conversations, *c)
	}
	sort.Slice(conversations, func(i, j int) bool {
		return latestTime(conversations[i]).After(latestTime(conversations[j]))
	})

	allN, run, done, errN := s.store.Counts()

	var selected *RunRecord
	if selectedID != "" {
		selected = s.store.Find(selectedID)
	}

	return ChatSidebarData{
		Conversations: conversations,
		Selected:      selected,
		Filter:        filter,
		Query:         query,
		CountAll:      allN,
		CountRun:      run,
		CountDone:     done,
		CountError:    errN,
	}
}

// buildRunTree wires a flat list of runs into a forest by ParentRunID.
// Runs whose parent isn't in the list (yet — e.g. the parent crossed
// session boundaries, or events arrived out of order) are promoted to
// roots so they don't get lost from the view.
func buildRunTree(runs []*RunRecord) []*RunNode {
	nodes := make(map[string]*RunNode, len(runs))
	for _, r := range runs {
		nodes[r.ID] = &RunNode{Run: r}
	}
	var roots []*RunNode
	for _, r := range runs {
		n := nodes[r.ID]
		if r.ParentRunID == "" {
			roots = append(roots, n)
			continue
		}
		parent, ok := nodes[r.ParentRunID]
		if !ok {
			roots = append(roots, n) // orphan: render as root
			continue
		}
		parent.Children = append(parent.Children, n)
	}
	// Children within each parent: chronological so the tree mirrors the
	// order tools fired.
	var sortKids func(n *RunNode)
	sortKids = func(n *RunNode) {
		sort.Slice(n.Children, func(i, j int) bool {
			return n.Children[i].Run.StartedAt.Before(n.Children[j].Run.StartedAt)
		})
		for _, c := range n.Children {
			sortKids(c)
		}
	}
	for _, r := range roots {
		sortKids(r)
	}
	return roots
}

// mostRecentTime returns the timestamp of the latest activity in a session,
// used for descending session ordering in the sidebar.
func mostRecentTime(g SessionGroup) time.Time {
	t := g.EndedAt
	if g.HasRunning || t.IsZero() {
		// Use the latest run's start time as a proxy.
		for _, r := range g.Runs {
			if r.StartedAt.After(t) {
				t = r.StartedAt
			}
		}
	}
	return t
}

func (s *Server) detailData(run *RunRecord) DetailData {
	tl := BuildTimeline(run.Steps)
	if !run.EndedAt.IsZero() {
		tl.EndAt = run.EndedAt
	}
	// Index every child run by the tool call that spawned it. The lookup is
	// scoped to children of this run only; cross-run spawns aren't a thing.
	spawned := map[string][]*RunRecord{}
	for _, r := range s.store.All() {
		if r.ParentRunID == run.ID && r.ParentToolCallID != "" {
			spawned[r.ParentToolCallID] = append(spawned[r.ParentToolCallID], r)
		}
	}
	return DetailData{
		Run:               run,
		Timeline:          tl,
		Live:              run.Status == RunRunning,
		SpawnedByToolCall: spawned,
	}
}

// ─── Render helpers ──────────────────────────────────────────────────────────

// page renders a full HTML page using the templ Base shell.
func page(w http.ResponseWriter, r *http.Request, title, currentPath string, body templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = Base(title, currentPath, body).Render(r.Context(), w)
}

// patch renders a templ component into HTML and ships it as a datastar SSE
// patch event with the given target selector and merge mode.
func patch(w http.ResponseWriter, r *http.Request, selector string, mode datastar.ElementPatchMode, comp templ.Component) {
	var buf bytes.Buffer
	if err := comp.Render(r.Context(), &buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sse := datastar.NewSSE(w, r)
	_ = sse.PatchElements(buf.String(),
		datastar.WithSelector(selector),
		datastar.WithMode(mode),
	)
}

func shortIDOf(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// defaultSince is the time window applied when neither the URL query nor the
// datastar signal supplies one. Showing only the last 15 minutes by default
// keeps the panel manageable and matches the active pill the Base template
// renders, so first-load state and post-interaction state agree.
const defaultSince = "15m"

func readTimeRange(r *http.Request) TimeRange {
	q := r.URL.Query()
	since := q.Get("since")
	if since == "" {
		since = defaultSince
	}
	return TimeRange{
		Since: since,
		From:  q.Get("from"),
		To:    q.Get("to"),
	}
}

func readSignalTimeRange(r *http.Request) TimeRange {
	var sig struct {
		Since string `json:"since"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	if sig.Since == "" {
		sig.Since = r.URL.Query().Get("since")
	}
	if sig.From == "" {
		sig.From = r.URL.Query().Get("from")
	}
	if sig.To == "" {
		sig.To = r.URL.Query().Get("to")
	}
	if sig.Since == "" {
		sig.Since = defaultSince
	}
	return TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
}

func (s *Server) filterByTime(runs []*RunRecord, tr TimeRange) []*RunRecord {
	if !tr.Active() {
		return runs
	}
	if tr.Since == "custom" {
		var fromTime, toTime time.Time
		var err error
		if tr.From != "" {
			fromTime, err = time.Parse("2006-01-02T15:04", tr.From)
			if err != nil {
				fromTime, _ = time.Parse("2006-01-02T15:04:05", tr.From)
			}
		}
		if tr.To != "" {
			toTime, err = time.Parse("2006-01-02T15:04", tr.To)
			if err != nil {
				toTime, _ = time.Parse("2006-01-02T15:04:05", tr.To)
			}
		}
		filtered := make([]*RunRecord, 0, len(runs))
		for _, r := range runs {
			if !fromTime.IsZero() && r.StartedAt.Before(fromTime) {
				continue
			}
			if !toTime.IsZero() && r.StartedAt.After(toTime) {
				continue
			}
			filtered = append(filtered, r)
		}
		return filtered
	}
	d, err := time.ParseDuration(tr.Since)
	if err != nil {
		return runs
	}
	cutoff := time.Now().Add(-d)
	filtered := make([]*RunRecord, 0, len(runs))
	for _, r := range runs {
		if r.StartedAt.After(cutoff) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func latestTime(c ChatConversation) time.Time {
	if len(c.Messages) == 0 {
		return time.Time{}
	}
	t := c.Messages[len(c.Messages)-1].Run.StartedAt
	for _, m := range c.Messages {
		if !m.Run.EndedAt.IsZero() && m.Run.EndedAt.After(t) {
			t = m.Run.EndedAt
		}
	}
	if c.HasRunning {
		return time.Now()
	}
	return t
}

func renderComponent(r *http.Request, comp templ.Component) string {
	var buf bytes.Buffer
	if err := comp.Render(r.Context(), &buf); err != nil {
		return ""
	}
	return buf.String()
}

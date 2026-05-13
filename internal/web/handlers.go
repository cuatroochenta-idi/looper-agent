package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	data := s.dashboardData()
	page(w, r, "Dashboard — Looper Agent", "/", DashboardPage(data))
}

func (s *Server) handleRunsPage(w http.ResponseWriter, r *http.Request) {
	view := s.runsViewData("", r.URL.Query().Get("q"), "")
	page(w, r, "Traces — Looper Agent", "/runs", RunsPage(view))
}

func (s *Server) handleRunPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run := s.store.Find(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	view := s.runsViewData("", "", id)
	page(w, r, "Run "+shortIDOf(id), "/runs", RunsPage(view))
}


// ─── Partials (datastar-SSE patches) ─────────────────────────────────────────

func (s *Server) partialDashboard(w http.ResponseWriter, r *http.Request) {
	patch(w, r, "#dashboard-body", datastar.ElementPatchModeInner,
		DashboardBody(s.dashboardData()))
}

// partialSidebar reads $q / $status / $selected from the datastar signal
// payload (sent as ?datastar=<json> on GETs) and re-renders the sidebar.
func (s *Server) partialSidebar(w http.ResponseWriter, r *http.Request) {
	var sig struct {
		Q        string `json:"q"`
		Status   string `json:"status"`
		Selected string `json:"selected"`
	}
	_ = datastar.ReadSignals(r, &sig)
	view := s.sidebarData(sig.Status, sig.Q, sig.Selected)
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
	})
	// New card: sidebar needs to rebuild. Detail pane is wired via TopicRun.
	s.hub.Publish(TopicRun(id), TopicSidebar)

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
		// Per-step: only the detail pane needs to re-render. Sidebar stays
		// put so the user's card selection isn't clobbered by churn.
		s.hub.Publish(TopicRun(runID))
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
	// status / totals / final cost.
	s.hub.Publish(TopicRun(runID), TopicSidebar)
	// Mirror to disk so the next `looper serve` reload can replay this run.
	if r := s.store.Find(runID); r != nil {
		_ = writeRunFile(s.storeDir, r)
	}
}

// ─── View-model builders ─────────────────────────────────────────────────────

func (s *Server) dashboardData() DashboardData {
	runs := s.store.All()
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

func (s *Server) runsViewData(filter, query, selectedID string) RunsViewData {
	sidebar := s.sidebarData(filter, query, selectedID)
	var detail *DetailData
	if sidebar.Selected != nil {
		d := s.detailData(sidebar.Selected)
		detail = &d
	}
	return RunsViewData{Sidebar: sidebar, Detail: detail}
}

func (s *Server) sidebarData(filter, query, selectedID string) SidebarData {
	all := s.store.All()
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

// Ensure unused imports are referenced when building skeleton without templ
// components yet — keeps the build green during incremental development.
var _ = fmt.Sprintf

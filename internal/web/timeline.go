package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"
)

// ─── Hierarchical timeline ────────────────────────────────────────────────────

// ToolCallNode pairs a tool_call step with its matching tool_result.
type ToolCallNode struct {
	Call   TimelineStep
	Result *TimelineStep
}

// TurnNode aggregates everything that happened in one agentic turn.
type TurnNode struct {
	Index      int
	StartAt    time.Time
	LLMCall    *TimelineStep
	ToolNodes  []ToolCallNode
	Final      *TimelineStep
	Error      *TimelineStep
	// Reasoning is the concatenation of every reasoning_chunk emitted in
	// the turn. Surfaced as a collapsible node so the operator can keep
	// the timeline tidy.
	Reasoning  string
	HasTokens  bool
	InTokens   int
	OutTokens  int
	CachedToks int
}

// EndAt returns the timestamp when the turn finished — either the Final step,
// the last tool result, or the turn's start if nothing else happened.
func (t TurnNode) EndAt() time.Time {
	if t.Final != nil {
		return t.Final.At
	}
	if t.Error != nil {
		return t.Error.At
	}
	for i := len(t.ToolNodes) - 1; i >= 0; i-- {
		if t.ToolNodes[i].Result != nil {
			return t.ToolNodes[i].Result.At
		}
	}
	return t.StartAt
}

// RunTimeline is the hierarchical view of a run consumed by templ components.
type RunTimeline struct {
	SystemPrompt *TimelineStep
	Turns        []TurnNode
	StartAt      time.Time
	EndAt        time.Time
}

// Duration returns the wall-clock duration of the run (or elapsed so far).
func (t RunTimeline) Duration() time.Duration {
	if t.StartAt.IsZero() {
		return 0
	}
	end := t.EndAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.StartAt)
}

// SpanGeom is the position+width of a span on a gantt strip, as percentages.
type SpanGeom struct {
	Offset float64
	Width  float64
}

// SpanGeom returns offset+width for a span [from,to] relative to the run.
func (t RunTimeline) SpanGeom(from, to time.Time) SpanGeom {
	d := t.Duration()
	if d <= 0 || from.IsZero() {
		return SpanGeom{}
	}
	if to.IsZero() {
		to = time.Now()
	}
	total := float64(d)
	offset := float64(from.Sub(t.StartAt)) / total * 100
	width := float64(to.Sub(from)) / total * 100
	if offset < 0 {
		offset = 0
	}
	if width < 0.5 {
		width = 0.5
	}
	if offset+width > 100 {
		width = 100 - offset
	}
	return SpanGeom{Offset: offset, Width: width}
}

// BuildTimeline groups raw steps into a hierarchy.
func BuildTimeline(steps []TimelineStep) RunTimeline {
	tl := RunTimeline{}
	if len(steps) == 0 {
		return tl
	}
	tl.StartAt = steps[0].At
	tl.EndAt = steps[len(steps)-1].At

	byTurn := make(map[int]*TurnNode)
	turnOrder := []int{}

	for i := range steps {
		s := steps[i]
		if s.Kind == StepKindSystemPrompt {
			sp := s
			tl.SystemPrompt = &sp
			continue
		}
		t, ok := byTurn[s.Turn]
		if !ok {
			t = &TurnNode{Index: s.Turn, StartAt: s.At}
			byTurn[s.Turn] = t
			turnOrder = append(turnOrder, s.Turn)
		}
		if s.InputTokens > 0 || s.OutputTokens > 0 {
			t.HasTokens = true
			if s.InputTokens > t.InTokens {
				t.InTokens = s.InputTokens
			}
			if s.OutputTokens > t.OutTokens {
				t.OutTokens = s.OutputTokens
			}
			if s.CachedTokens > t.CachedToks {
				t.CachedToks = s.CachedTokens
			}
		}
		switch s.Kind {
		case StepKindLLMCall:
			marker := s
			t.LLMCall = &marker
		case StepKindToolCall:
			t.ToolNodes = append(t.ToolNodes, ToolCallNode{Call: s})
		case StepKindToolResult:
			matched := false
			for i := range t.ToolNodes {
				if t.ToolNodes[i].Result != nil {
					continue
				}
				if t.ToolNodes[i].Call.ToolCallID != "" && t.ToolNodes[i].Call.ToolCallID == s.ToolCallID {
					r := s
					t.ToolNodes[i].Result = &r
					matched = true
					break
				}
			}
			if !matched {
				for i := range t.ToolNodes {
					if t.ToolNodes[i].Result == nil {
						r := s
						t.ToolNodes[i].Result = &r
						matched = true
						break
					}
				}
			}
		case StepKindFinal:
			f := s
			t.Final = &f
		case StepKindError:
			e := s
			t.Error = &e
		case StepKindReasoning:
			t.Reasoning += s.Content
		}
	}
	for _, idx := range turnOrder {
		tl.Turns = append(tl.Turns, *byTurn[idx])
	}
	return tl
}

// ─── View-model types used by templ components ───────────────────────────────

// DashboardData is the view model for the dashboard page.
type DashboardData struct {
	TotalRuns   int
	TotalCost   float64
	TotalTokens int
	AvgTurns    float64
	Recent      []*RunRecord
}

// SidebarData is the view model for the sidebar fragment.
type SidebarData struct {
	Groups     []SessionGroup
	Loose      []*RunRecord // runs without a SessionID (e.g. POST /api/run)
	Selected   *RunRecord
	Filter     string
	Query      string
	CountAll   int
	CountRun   int
	CountDone  int
	CountError int
}

// SessionGroup is the bucket of runs sharing one LOOPER_SESSION_ID. Sorted by
// the start time of the FIRST run; runs inside sorted chronologically.
//
// Roots is the hierarchy view: every run with empty ParentRunID becomes a
// top-level RunNode, and its Children are recursively attached by matching
// ParentRunID. Used by the sidebar to render nested cards.
type SessionGroup struct {
	ID         string
	Project    string
	Runs       []*RunRecord
	Roots      []*RunNode
	StartedAt  time.Time
	EndedAt    time.Time
	TotalUSD   float64
	HasRunning bool
	HasError   bool
}

// RunNode is a node in the session's parent/child tree. Used only for
// rendering — the flat Runs slice on SessionGroup remains the source of
// truth for aggregate stats.
type RunNode struct {
	Run      *RunRecord
	Children []*RunNode
}

// Short returns the first 8 chars of the session ID for display.
func (g SessionGroup) Short() string { return ShortID(g.ID) }

// Duration returns the elapsed time across all runs in the session.
func (g SessionGroup) Duration() time.Duration {
	if g.StartedAt.IsZero() {
		return 0
	}
	end := g.EndedAt
	if end.IsZero() || g.HasRunning {
		end = time.Now()
	}
	return end.Sub(g.StartedAt)
}

// Contains returns true if any run in the group matches the given ID.
func (g SessionGroup) Contains(runID string) bool {
	for _, r := range g.Runs {
		if r.ID == runID {
			return true
		}
	}
	return false
}

// DetailData is the view model for the run detail pane.
//
// SpawnedByToolCall keys each ParentToolCallID a child run carries (i.e. the
// tool call_id that produced it) to the slice of child RunRecords. The trace
// renderer uses it to attach sub-agent cards under the right tool node.
type DetailData struct {
	Run               *RunRecord
	Timeline          RunTimeline
	Live              bool
	SpawnedByToolCall map[string][]*RunRecord
}

// RunsViewData wraps the sidebar + detail for the runs page.
type RunsViewData struct {
	Sidebar SidebarData
	Detail  *DetailData
}

// ─── Presentation helpers (used by templ components) ─────────────────────────

// PrettyDuration formats a duration with sensible precision.
func PrettyDuration(d time.Duration) string {
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// ShortID returns the first 8 characters of an ID.
func ShortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// ArgsPreview compacts JSON to one line and truncates it.
func ArgsPreview(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return Truncate(t, 80)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Truncate(t, 80)
	}
	return Truncate(string(b), 80)
}

// Truncate caps a string with an ellipsis.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// PrettyJSON returns syntax-highlighted JSON HTML, safe to embed via templ's
// `@templ.Raw` / dangerous-unsafe injection. If the input isn't valid JSON,
// it falls back to escaped plain text in a neutral span.
func PrettyJSON(s string) template.HTML {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return template.HTML(`<span class="j-x">` + html.EscapeString(s) + `</span>`)
	}
	return template.HTML(colorizeJSON(string(b)))
}

func colorizeJSON(s string) string {
	var b strings.Builder
	n := len(s)
	i := 0

	emitString := func(j *int) {
		start := *j
		(*j)++
		for *j < n {
			if s[*j] == '\\' && *j+1 < n {
				*j += 2
				continue
			}
			if s[*j] == '"' {
				(*j)++
				break
			}
			(*j)++
		}
		tok := s[start:*j]
		k := *j
		for k < n && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
			k++
		}
		class := "j-s"
		if k < n && s[k] == ':' {
			class = "j-k"
		}
		fmt.Fprintf(&b, `<span class="%s">%s</span>`, class, html.EscapeString(tok))
	}
	emitLiteral := func(j *int, lit, class string) bool {
		if strings.HasPrefix(s[*j:], lit) {
			fmt.Fprintf(&b, `<span class="%s">%s</span>`, class, lit)
			*j += len(lit)
			return true
		}
		return false
	}
	emitNumber := func(j *int) {
		start := *j
		if s[*j] == '-' || s[*j] == '+' {
			(*j)++
		}
		for *j < n && (s[*j] >= '0' && s[*j] <= '9' || s[*j] == '.' || s[*j] == 'e' || s[*j] == 'E' || s[*j] == '+' || s[*j] == '-') {
			(*j)++
		}
		fmt.Fprintf(&b, `<span class="j-n">%s</span>`, s[start:*j])
	}
	for i < n {
		c := s[i]
		switch {
		case c == '"':
			emitString(&i)
		case c == 't':
			if !emitLiteral(&i, "true", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == 'f':
			if !emitLiteral(&i, "false", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == 'n':
			if !emitLiteral(&i, "null", "j-b") {
				b.WriteByte(c)
				i++
			}
		case c == '-' || (c >= '0' && c <= '9'):
			emitNumber(&i)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
